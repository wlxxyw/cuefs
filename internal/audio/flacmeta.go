package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// FLAC metadata block types (FLAC spec §3).
const (
	BlockStreamInfo    = 0
	BlockPadding       = 1
	BlockApplication   = 2
	BlockSeekTable     = 3
	BlockVorbisComment = 4
	BlockCueSheet      = 5
	BlockPicture       = 6
)

// MetaBlock is one metadata block of a FLAC file.
type MetaBlock struct {
	Kind int
	Last bool
	Data []byte

	// Offset/Length are positions inside the file, for tools that want to
	// rewrite in place.
	Offset int64
	Length int64 // header (4) + payload
}

// StreamInfo holds the fields a splitter needs from STREAMINFO.
type StreamInfo struct {
	MinBlocksize  int
	MaxBlocksize  int
	MinFramesize  int
	MaxFramesize  int
	SampleRate    int
	Channels      int
	BitsPerSample int
	TotalSamples  int64
	MD5           [16]byte
	Present       bool
}

// meterName maps block kinds to readable names.
var blockName = map[int]string{
	0: "STREAMINFO", 1: "PADDING", 2: "APPLICATION", 3: "SEEKTABLE",
	4: "VORBIS_COMMENT", 5: "CUESHEET", 6: "PICTURE", 127: "INVALID",
}

// Name describes the block kind.
func (b MetaBlock) Name() string {
	if n, ok := blockName[b.Kind]; ok {
		return n
	}
	return fmt.Sprintf("UNKNOWN[%d]", b.Kind)
}

// ReadMetaBlocks parses the metadata block chain of a FLAC file and returns
// the offset of the first audio frame.
func ReadMetaBlocks(path string) (blocks []MetaBlock, streamInfo StreamInfo, framesOffset int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, streamInfo, 0, err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	magic := make([]byte, 4)
	if _, err := io.ReadFull(br, magic); err != nil {
		return nil, streamInfo, 0, fmt.Errorf("%s: %w", path, err)
	}
	if string(magic) != "fLaC" {
		return nil, streamInfo, 0, fmt.Errorf("%s: not a FLAC file (magic %q)", path, magic)
	}
	offset := int64(4)
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return nil, streamInfo, 0, fmt.Errorf("%s: metadata block header: %w", path, err)
		}
		last := hdr[0]&0x80 != 0
		kind := int(hdr[0] & 0x7F)
		size := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
		payload := make([]byte, size)
		if _, err := io.ReadFull(br, payload); err != nil {
			return nil, streamInfo, 0, fmt.Errorf("%s: metadata block %s: %w", path, blockNameFor(kind), err)
		}
		total := int64(4 + size)
		blocks = append(blocks, MetaBlock{
			Kind: kind, Last: last, Data: payload, Offset: offset, Length: total,
		})
		if kind == BlockStreamInfo {
			streamInfo, err = parseStreamInfo(payload)
			if err != nil {
				return nil, streamInfo, 0, fmt.Errorf("%s: %w", path, err)
			}
			streamInfo.Present = true
		}
		offset += total
		if last {
			break
		}
	}
	return blocks, streamInfo, offset, nil
}

func blockNameFor(kind int) string {
	if n, ok := blockName[kind]; ok {
		return n
	}
	return fmt.Sprintf("TYPE[%d]", kind)
}

// parseStreamInfo decodes the 34 byte STREAMINFO payload (FLAC spec §5).
func parseStreamInfo(p []byte) (StreamInfo, error) {
	var si StreamInfo
	if len(p) != 34 {
		return si, fmt.Errorf("STREAMINFO is %d bytes, want 34", len(p))
	}
	si.MinBlocksize = int(binary.BigEndian.Uint16(p[0:2]))
	si.MaxBlocksize = int(binary.BigEndian.Uint16(p[2:4]))
	si.MinFramesize = int(p[4])<<16 | int(p[5])<<8 | int(p[6])
	si.MaxFramesize = int(p[7])<<16 | int(p[8])<<8 | int(p[9])

	bits := binary.BigEndian.Uint64(p[10:18]) // 20+3+5+36 = 64 bits exactly
	// bit layout (MSB first): 20 sample rate, 3 channels-1, 5 bps-1,
	// 36 total samples.
	si.SampleRate = int(bits >> 44 & 0xFFFFF)
	si.Channels = int(bits>>41&0x7) + 1
	si.BitsPerSample = int(bits>>36&0x1F) + 1
	si.TotalSamples = int64(bits >> 0 & ((1 << 36) - 1))
	if si.TotalSamples < 0 {
		si.TotalSamples = 0
	}
	copy(si.MD5[:], p[18:34])

	if si.SampleRate <= 0 {
		return si, fmt.Errorf("STREAMINFO has an invalid sample rate %d", si.SampleRate)
	}
	if si.MinBlocksize == 0 || si.MaxBlocksize < si.MinBlocksize {
		return si, fmt.Errorf("STREAMINFO has inconsistent block sizes %d..%d",
			si.MinBlocksize, si.MaxBlocksize)
	}
	return si, nil
}

// VorbisEntry is one key/value pair of a Vorbis Comment.
type VorbisEntry struct {
	Key   string
	Value string
}

// EncodeVorbisComment renders a Vorbis Comment payload (FLAC spec §5.14):
// 32-bit little-endian vendor length, vendor string, 32-bit comment count,
// then length-prefixed "KEY=value" strings in UTF-8.
func EncodeVorbisComment(vendor string, entries []VorbisEntry) []byte {
	var b strings.Builder
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(len(vendor)))
	b.Write(buf)
	b.WriteString(vendor)
	binary.LittleEndian.PutUint32(buf, uint32(len(entries)))
	b.Write(buf)
	for _, e := range entries {
		line := e.Key + "=" + e.Value
		binary.LittleEndian.PutUint32(buf, uint32(len(line)))
		b.Write(buf)
		b.WriteString(line)
	}
	return []byte(b.String())
}

// DecodeVorbisComment parses a Vorbis Comment payload.
func DecodeVorbisComment(p []byte) (vendor string, entries []VorbisEntry, err error) {
	if len(p) < 8 {
		return "", nil, errors.New("VORBIS_COMMENT truncated")
	}
	vlen := int(binary.LittleEndian.Uint32(p[0:4]))
	if 4+vlen > len(p) {
		return "", nil, errors.New("VORBIS_COMMENT vendor length overruns the block")
	}
	vendor = string(p[4 : 4+vlen])
	i := 4 + vlen
	count := int(binary.LittleEndian.Uint32(p[i : i+4]))
	i += 4
	entries = make([]VorbisEntry, 0, count)
	for n := 0; n < count; n++ {
		if i+4 > len(p) {
			return vendor, entries, fmt.Errorf("VORBIS_COMMENT: only %d of %d comments", n, count)
		}
		clen := int(binary.LittleEndian.Uint32(p[i : i+4]))
		i += 4
		if i+clen > len(p) {
			return vendor, entries, fmt.Errorf("VORBIS_COMMENT: comment %d overruns the block", n)
		}
		line := string(p[i : i+clen])
		i += clen
		key, val := line, ""
		if eq := strings.IndexByte(line, '='); eq >= 0 {
			key, val = line[:eq], line[eq+1:]
		}
		entries = append(entries, VorbisEntry{Key: key, Value: val})
	}
	return vendor, entries, nil
}

// MinPadding keeps a little padding for future in-place tag edits.
const MinPadding = 4096

// DefaultVendor is the vendor string written into the VORBIS_COMMENT block.
const DefaultVendor = "cuefs"

// WriteVorbisComment replaces (or inserts) the VORBIS_COMMENT block of a FLAC
// file, leaving every other block and all audio frames byte-identical. The
// file is rewritten atomically through a .part file, so an interrupted run
// never destroys the source.
func WriteVorbisComment(path string, entries []VorbisEntry) error {
	return writeVorbisComment(path, DefaultVendor, entries)
}
func writeVorbisComment(path, vendor string, entries []VorbisEntry) error {
	blocks, _, framesOffset, err := ReadMetaBlocks(path)
	if err != nil {
		return err
	}
	found := false
	out := make([]MetaBlock, 0, len(blocks)+1)
	for _, b := range blocks {
		switch {
		case b.Kind == BlockVorbisComment && !found:
			out = append(out, MetaBlock{Kind: BlockVorbisComment, Last: b.Last,
				Data: EncodeVorbisComment(vendor, entries)})
			found = true
		case b.Kind == BlockPadding:
			// Keep a little padding for future in-place tag edits, but do
			// not let a printer's leftover megabytes survive a rewrite.
			data := b.Data
			if len(data) > MinPadding {
				data = make([]byte, MinPadding)
			}
			out = append(out, MetaBlock{Kind: BlockPadding, Last: b.Last, Data: data})
		default:
			out = append(out, b)
		}
	}
	if !found {
		// Insert right after STREAMINFO so that the ordering stays sane.
		if len(out) > 0 && out[len(out)-1].Last {
			out[len(out)-1].Last = false
		}
		insert := MetaBlock{Kind: BlockVorbisComment, Data: EncodeVorbisComment(vendor, entries)}
		if len(out) >= 1 {
			out = append(out[:1], append([]MetaBlock{insert}, out[1:]...)...)
		} else {
			out = append(out, insert)
		}
	}
	out[len(out)-1].Last = true

	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	src, err := os.Open(path)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	w := bufio.NewWriterSize(f, 1<<16)
	if _, err := w.WriteString("fLaC"); err != nil {
		goto fail
	}
	for _, b := range out {
		// 4 byte header: last-flag + type, then a 24 bit big-endian size.
		n := len(b.Data)
		hdr := []byte{
			byte(b.Kind & 0x7F),
			byte(n >> 16 & 0x7F),
			byte(n >> 8),
			byte(n),
		}
		if b.Last {
			hdr[0] |= 0x80
		}
		if _, err := w.Write(hdr); err != nil {
			goto fail
		}
		if _, err := w.Write(b.Data); err != nil {
			goto fail
		}
	}
	if _, err := src.Seek(framesOffset, io.SeekStart); err != nil {
		goto fail
	}
	if _, err := io.Copy(w, src); err != nil {
		goto fail
	}
	src.Close()
	if err := w.Flush(); err != nil {
		goto fail
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)

fail:
	f.Close()
	src.Close()
	os.Remove(tmp)
	return fmt.Errorf("rewrite VORBIS_COMMENT of %s failed", path)
}
