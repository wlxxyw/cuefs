package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// WavFile is a parsed WAV/RF64 container. Everything it knows comes down to
// one question: where is the PCM payload and how long is it. Splitting is a
// pure byte copy, which is why WAV needs no decoder at all.
type WavFile struct {
	Path       string
	Fmt        []byte // the full "fmt " chunk, verbatim (incl. extensible bits)
	DataOffset int64  // absolute offset of the PCM payload
	DataLen    int64
	Info       WavInfo
}

// WavInfo holds the fields the splitter needs.
type WavInfo struct {
	FormatTag  int
	Channels   int
	SampleRate int
	ByteRate   int
	BlockAlign int
	Bits       int
}

// OpenWav parses a RIFF/WAVE or RF64 file. Chunks other than fmt/data are
// skipped, an odd-size chunk's pad byte is respected, and RF64's ds64 record
// overrides a 0xFFFFFFFF data chunk.
func OpenWav(path string) (*WavFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()

	hdr := make([]byte, 12)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	riff64 := string(hdr[0:4]) == "RF64"
	if !riff64 && string(hdr[0:4]) != "RIFF" {
		return nil, fmt.Errorf("%s: not a RIFF file (magic %q)", path, hdr[0:4])
	}
	if string(hdr[8:12]) != "WAVE" {
		return nil, fmt.Errorf("%s: not a WAVE file", path)
	}

	w := &WavFile{Path: path}
	var ds64Size int64
	pos := int64(12)
	for pos+8 <= size {
		head := make([]byte, 8)
		if _, err := f.ReadAt(head, pos); err != nil {
			return nil, fmt.Errorf("%s: chunk header at %d: %w", path, pos, err)
		}
		id := string(head[0:4])
		cs := int64(binary.LittleEndian.Uint32(head[4:8]))
		payload := pos + 8
		if cs == -1 && riff64 && id == "data" {
			if ds64Size == 0 {
				return nil, fmt.Errorf("%s: data chunk has RIFF size -1 but no ds64 record", path)
			}
			cs = ds64Size
		}
		switch id {
		case "fmt ":
			if cs < 16 {
				return nil, fmt.Errorf("%s: fmt chunk is only %d bytes", path, cs)
			}
			w.Fmt = make([]byte, cs)
			if _, err := f.ReadAt(w.Fmt, payload); err != nil {
				return nil, fmt.Errorf("%s: read fmt chunk: %w", path, err)
			}
			w.Info.FormatTag = int(binary.LittleEndian.Uint16(w.Fmt[0:2]))
			w.Info.Channels = int(binary.LittleEndian.Uint16(w.Fmt[2:4]))
			w.Info.SampleRate = int(binary.LittleEndian.Uint32(w.Fmt[4:8]))
			w.Info.ByteRate = int(binary.LittleEndian.Uint32(w.Fmt[8:12]))
			w.Info.BlockAlign = int(binary.LittleEndian.Uint16(w.Fmt[12:14]))
			w.Info.Bits = int(binary.LittleEndian.Uint16(w.Fmt[14:16]))
		case "ds64":
			if cs < 28 {
				return nil, fmt.Errorf("%s: ds64 chunk is %d bytes", path, cs)
			}
			rec := make([]byte, 28)
			if _, err := f.ReadAt(rec, payload); err != nil {
				return nil, err
			}
			ds64Size = int64(binary.LittleEndian.Uint64(rec[8:16]))
		case "data":
			w.DataOffset = payload
			w.DataLen = cs
			if payload+w.DataLen > size && !riff64 {
				// Some writers omit the size: fall back to the file tail.
				w.DataLen = size - payload
			}
			return w, nil
		}
		adv := 8 + cs
		if cs%2 == 1 { // chunks are word aligned
			adv++
		}
		pos += adv
	}
	return nil, fmt.Errorf("%s: no data chunk found", path)
}

// Validate rejects anything the splitter cannot reproduce faithfully.
func (w *WavFile) Validate() error {
	switch w.Info.FormatTag {
	case 1, 3, 0xFFFE: // PCM, IEEE float, WAVE_FORMAT_EXTENSIBLE
	default:
		return fmt.Errorf("%s: unsupported format tag 0x%04X", w.Path, w.Info.FormatTag)
	}
	if w.Info.Channels < 1 || w.Info.Bits < 1 || w.Info.SampleRate < 1 {
		return fmt.Errorf("%s: malformed fmt chunk %+v", w.Path, w.Info)
	}
	if want := (w.Info.Bits + 7) / 8 * w.Info.Channels; w.Info.BlockAlign != want {
		return fmt.Errorf("%s: block align %d, want %d", w.Path, w.Info.BlockAlign, want)
	}
	return nil
}

// maxDataChunk is what still fits a plain RIFF header (length field minus
// overhead). Anything bigger needs RF64 output.
const maxDataChunk = int64(1<<32 - 16)

// Split writes track [start,end) of the PCM payload into out, byte for byte,
// with a fresh RIFF header. end < 0 means "to the end of the source".
// Samples are sliced by BlockAlign, so the output is sample-exact and needs
// no decoder. The file is written through a .part file and renamed.
func (w *WavFile) Split(out string, start, end int64) error {
	if err := w.Validate(); err != nil {
		return err
	}
	if start < 0 {
		return fmt.Errorf("negative start %d", start)
	}
	sampleSize := int64(w.Info.BlockAlign)
	if start*sampleSize > w.DataLen {
		return fmt.Errorf("start %d is past the end of the data chunk", start)
	}
	dataStart := w.DataOffset + start*sampleSize
	dataLen := w.DataLen - start*sampleSize
	if end >= 0 {
		if end < start {
			return fmt.Errorf("end %d before start %d", end, start)
		}
		dataLen = (end - start) * sampleSize
		if dataStart+dataLen > w.DataOffset+w.DataLen {
			return fmt.Errorf("end %d is past the end of the data chunk", end)
		}
	}
	if dataLen > maxDataChunk {
		return fmt.Errorf("%s: a %d byte data chunk needs RF64 output, which the splitter does not write", w.Path, dataLen)
	}

	riffLen := uint32(int64(4+8) + int64(len(w.Fmt)+8) + dataLen)
	payload := &bytes.Buffer{}
	payload.WriteString("RIFF")
	putU32(payload, riffLen)
	payload.WriteString("WAVE")
	putChunk(payload, "fmt ", w.Fmt)
	// The data chunk header is followed by the PCM bytes, which are copied
	// separately; its size field must already reflect them.
	payload.WriteString("data")
	putU32(payload, uint32(dataLen))

	tmp := out + ".part"
	dst, err := os.Create(tmp)
	if err != nil {
		return err
	}
	src, err := os.Open(w.Path)
	if err != nil {
		dst.Close()
		os.Remove(tmp)
		return err
	}
	fail := func(format string, args ...any) error {
		dst.Close()
		src.Close()
		os.Remove(tmp)
		return fmt.Errorf(format, args...)
	}
	if _, err := dst.Write(payload.Bytes()); err != nil {
		return fail("write header: %v", err)
	}
	if _, err := src.Seek(dataStart, io.SeekStart); err != nil {
		return fail("seek: %v", err)
	}
	if _, err := io.CopyN(dst, src, dataLen); err != nil {
		return fail("copy %d bytes: %v", dataLen, err)
	}
	if err := dst.Close(); err != nil {
		src.Close()
		os.Remove(tmp)
		return err
	}
	if err := src.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, out)
}

func putU32(b *bytes.Buffer, v uint32) {
	var t [4]byte
	binary.LittleEndian.PutUint32(t[:], v)
	b.Write(t[:])
}

// putChunk appends a "id size payload" chunk; nil payload writes only the
// header (used for the data chunk, whose bytes follow separately).
func putChunk(b *bytes.Buffer, id string, data []byte) {
	b.WriteString(id)
	putU32(b, uint32(len(data)))
	b.Write(data)
	if len(data)%2 == 1 { // chunks are word aligned
		b.WriteByte(0)
	}
}
