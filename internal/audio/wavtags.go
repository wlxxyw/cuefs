package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"cuefs/internal/cue"
)

// A WAV output cannot carry a Vorbis Comment, so the split's tags go into
// the two containers readers actually implement:
//
//   - an ID3v2.4 chunk ("ID3 "), which is what taglib reads and therefore
//     what Navidrome sees (TRCK/TPOS fold track and disc totals);
//   - a RIFF "LIST INFO" chunk plus a "CSET" declaration of UTF-8, which is
//     what ffmpeg, Explorer and most other tools read. Without CSET a reader
//     falls back to the RIFF default of Latin1 and Chinese text garbles.
//
// Both are appended after the data chunk - taglib itself appends them the
// same way - so the cheap path only patches the RIFF size field and seeks.
// Only when a previous tag chunk has to be replaced (retag) is the file
// rebuilt through a .part file.

// csetUTF8 is the Windows code page number for UTF-8 (65001), written
// little-endian as a RIFF CSET chunk.
const csetUTF8 = 65001

// wavInfoID maps the Vorbis keys RIFF INFO can express onto their MCI ids.
var wavInfoID = map[string]string{
	"TITLE": "INAM", "ARTIST": "IART", "ALBUM": "IPRD",
	"DATE": "ICRD", "GENRE": "IGNR", "COMMENT": "ICMT",
}

// id3Frame maps a Vorbis key onto its ID3v2.4 frame id. Keys without a
// dedicated frame become TXXX (user-defined text) frames with the key as the
// description; track and disc totals are folded into TRCK/TPOS instead.
func id3Frame(key string) string {
	switch key {
	case "TITLE":
		return "TIT2"
	case "ARTIST":
		return "TPE1"
	case "ALBUMARTIST":
		return "TPE2"
	case "ALBUM":
		return "TALB"
	case "DATE":
		return "TDRC"
	case "GENRE":
		return "TCON"
	case "COMPOSER":
		return "TCOM"
	case "ISRC":
		return "TSRC"
	case "COMMENT":
		return "COMM"
	case "TRACKNUMBER", "TRACKTOTAL", "DISCNUMBER", "TOTALDISCS":
		return ""
	default:
		return "TXXX"
	}
}

// groupEntries folds the plan's one-entry-per-value list into one value per
// key, preserving first-seen order so the output stays byte-stable.
func groupEntries(entries []VorbisEntry) (order []string, values map[string][]string) {
	values = map[string][]string{}
	for _, e := range entries {
		k := strings.ToUpper(strings.TrimSpace(e.Key))
		if k == "" {
			continue
		}
		if _, ok := values[k]; !ok {
			order = append(order, k)
		}
		values[k] = append(values[k], e.Value)
	}
	return order, values
}

func syncsafe(v uint32) []byte {
	return []byte{byte(v>>21) & 0x7F, byte(v>>14) & 0x7F, byte(v>>7) & 0x7F, byte(v & 0x7F)}
}

// buildID3 renders an ID3v2.4 tag payload (no "ID3" header yet).
func buildID3(entries []VorbisEntry) []byte {
	order, values := groupEntries(entries)
	joined := func(k string) string { return strings.Join(values[k], "; ") }

	var frames bytes.Buffer
	frame := func(id string, payload []byte) {
		frames.WriteString(id)
		frames.Write(syncsafe(uint32(len(payload))))
		frames.Write([]byte{0, 0}) // no flags
		frames.Write(payload)
	}
	text := func(id, val string) {
		// v2.4 text frames: encoding byte 3 = UTF-8, multiple values
		// separated by NUL.
		frame(id, append([]byte{0x03}, strings.Join(values[val], "\x00")...))
	}
	_ = text

	for _, k := range order {
		vs := values[k]
		if len(vs) == 0 || strings.TrimSpace(strings.Join(vs, "")) == "" {
			continue
		}
		switch id3Frame(k) {
		case "": // folded below, never emitted standalone
			continue
		case "COMM":
			// <enc> <lang 3 bytes> <short description, NUL-terminated> <text>
			payload := append([]byte{0x03}, "eng"...)
			payload = append(payload, 0x00, 0x00)
			payload = append(payload, strings.Join(vs, "; ")...)
			frame("COMM", payload)
		case "TXXX":
			payload := append([]byte{0x03}, k...)
			payload = append(payload, 0x00)
			payload = append(payload, strings.Join(vs, "; ")...)
			frame("TXXX", payload)
		default:
			payload := append([]byte{0x03}, strings.Join(vs, "\x00")...)
			frame(id3Frame(k), payload)
		}
	}
	// TRCK / TPOS fold the totals the plan keeps as separate keys.
	if num := joined("TRACKNUMBER"); num != "" {
		v := num
		if t := joined("TRACKTOTAL"); t != "" {
			v += "/" + t
		}
		frame("TRCK", append([]byte{0x03}, v...))
	}
	if disc := joined("DISCNUMBER"); disc != "" {
		v := disc
		if t := joined("TOTALDISCS"); t != "" {
			v += "/" + t
		}
		frame("TPOS", append([]byte{0x03}, v...))
	}

	out := make([]byte, 0, 10+frames.Len())
	out = append(out, "ID3"...)
	out = append(out, 0x04, 0x00, 0x00) // v2.4, no flags
	out = append(out, syncsafe(uint32(frames.Len()))...)
	return append(out, frames.Bytes()...)
}

// buildInfo renders a RIFF LIST chunk payload ("INFO" plus sub-chunks).
func buildInfo(entries []VorbisEntry) []byte {
	order, values := groupEntries(entries)
	var b bytes.Buffer
	b.WriteString("INFO")
	for _, k := range order {
		id, ok := wavInfoID[k]
		if !ok {
			continue
		}
		val := strings.Join(values[k], "; ")
		if val == "" {
			continue
		}
		b.WriteString(id)
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], uint32(len(val)))
		b.Write(sz[:])
		b.WriteString(val)
		if len(val)%2 == 1 {
			b.WriteByte(0) // chunks are word aligned
		}
	}
	return b.Bytes()
}

func csetPayload() []byte {
	p := make([]byte, 8)
	binary.LittleEndian.PutUint16(p, csetUTF8)
	return p
}

// riffChunk wraps a payload into a "id size payload (+pad)" chunk.
func riffChunk(id string, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload)+1)
	copy(out, id)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(payload)))
	out = append(out, payload...)
	if len(payload)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

type riffChunkPos struct {
	id       string
	headerAt int64 // offset of the 8-byte chunk header
	next     int64 // offset of the following chunk
	info     bool  // for LIST: whether the payload starts with "INFO"
}

// walkRiff lists every chunk of a plain RIFF/WAVE file with its positions.
func walkRiff(f *os.File, size int64) ([]riffChunkPos, error) {
	hdr := make([]byte, 12)
	if _, err := f.ReadAt(hdr, 0); err != nil {
		return nil, err
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a plain RIFF/WAVE file (magic %q)", hdr[0:4])
	}
	var out []riffChunkPos
	pos := int64(12)
	for pos+8 <= size {
		head := make([]byte, 8)
		if _, err := f.ReadAt(head, pos); err != nil {
			return nil, err
		}
		id := string(head[0:4])
		cs := int64(binary.LittleEndian.Uint32(head[4:8]))
		if cs < 0 || pos+8+cs > size {
			return nil, fmt.Errorf("chunk %q claims %d bytes past the end", id, cs)
		}
		c := riffChunkPos{id: id, headerAt: pos, next: pos + 8 + cs}
		if cs%2 == 1 {
			c.next++ // word aligned
		}
		if id == "LIST" && cs >= 4 {
			var tag [4]byte
			if _, err := f.ReadAt(tag[:], pos+8); err == nil {
				c.info = string(tag[:]) == "INFO"
			}
		}
		out = append(out, c)
		pos = c.next
	}
	return out, nil
}

// WriteWavTags attaches the plan's tags to a WAV file as an ID3v2.4 chunk
// and a RIFF LIST INFO (+CSET) chunk. Files without tag chunks get them
// appended with a patched RIFF size field; files that already carry tag
// chunks are rebuilt through a .part file so retag replaces rather than
// duplicates them.
func WriteWavTags(path string, entries []VorbisEntry) error {
	if len(entries) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	size := st.Size()

	chunks, err := walkRiff(f, size)
	if err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	hasOld := false
	for _, c := range chunks {
		if c.id == "ID3 " || c.id == "id3 " || c.id == "CSET" ||
			(c.id == "LIST" && c.info) {
			hasOld = true
			break
		}
	}

	added := riffChunk("ID3 ", buildID3(entries))
	added = append(added, riffChunk("LIST", buildInfo(entries))...)
	added = append(added, riffChunk("CSET", csetPayload())...)

	if !hasOld {
		// Cheap path: grow the RIFF size field and append. The file size is
		// the truth about how much data follows the field; a larger stale
		// field keeps its value so nothing that a reader counted is lost.
		var sz [4]byte
		if _, err := f.ReadAt(sz[:], 4); err != nil {
			f.Close()
			return fmt.Errorf("%s: read RIFF size: %w", path, err)
		}
		base := size - 8
		if field := int64(binary.LittleEndian.Uint32(sz[:])); field > base {
			base = field
		}
		binary.LittleEndian.PutUint32(sz[:], uint32(base+int64(len(added))))
		if _, err := f.WriteAt(sz[:], 4); err != nil {
			f.Close()
			return fmt.Errorf("%s: patch RIFF size: %w", path, err)
		}
		if _, err := f.WriteAt(added, size); err != nil {
			f.Close()
			return fmt.Errorf("%s: append tag chunks: %w", path, err)
		}
		return f.Close()
	}

	// Rebuild: stream every chunk except the old tag chunks, then append.
	var keptTotal int64 = 4 // "WAVE"
	for _, c := range chunks {
		if c.id == "ID3 " || c.id == "id3 " || c.id == "CSET" ||
			(c.id == "LIST" && c.info) {
			continue
		}
		keptTotal += c.next - c.headerAt
	}
	newSize := keptTotal + int64(len(added))

	tmp := path + ".part"
	dst, err := os.Create(tmp)
	if err != nil {
		return err
	}
	fail := func(format string, a ...any) error {
		dst.Close()
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf(format, a...)
	}
	var hdr [12]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil {
		return fail("read header: %v", err)
	}
	binary.LittleEndian.PutUint32(hdr[4:], uint32(newSize))
	if _, err := dst.Write(hdr[:]); err != nil {
		return fail("write header: %v", err)
	}
	for _, c := range chunks {
		if c.id == "ID3 " || c.id == "id3 " || c.id == "CSET" ||
			(c.id == "LIST" && c.info) {
			continue
		}
		buf := make([]byte, c.next-c.headerAt)
		if _, err := f.ReadAt(buf, c.headerAt); err != nil {
			return fail("copy chunk %q: %v", c.id, err)
		}
		if _, err := dst.Write(buf); err != nil {
			return fail("write chunk %q: %v", c.id, err)
		}
	}
	if _, err := dst.Write(added); err != nil {
		return fail("append tag chunks: %v", err)
	}
	if err := dst.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	// Windows cannot rename over a file that is still open.
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ReadWavTags returns a WAV file's LIST INFO tags keyed upper-case. Text is
// decoded as UTF-8 when the file says so (CSET) or the bytes validate, and
// through the library's encoding detection otherwise - the same order cue
// detection uses, so a GB18030 rip reads the same way its cue does.
func ReadWavTags(path string) (map[string][]string, error) {
	out := map[string][]string{}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	chunks, err := walkRiff(f, st.Size())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	utf8Declared := false
	for _, c := range chunks {
		if c.id == "CSET" && c.next-c.headerAt >= 10 {
			var p [8]byte
			if _, err := f.ReadAt(p[:], c.headerAt+8); err == nil {
				utf8Declared = binary.LittleEndian.Uint16(p[:]) == csetUTF8
			}
		}
	}
	for _, c := range chunks {
		if c.id != "LIST" || !c.info {
			continue
		}
		cs := c.next - c.headerAt - 8
		payload := make([]byte, cs)
		if _, err := f.ReadAt(payload, c.headerAt+8); err != nil {
			return nil, err
		}
		pos := 4 // skip "INFO"
		for pos+8 <= len(payload) {
			id := string(payload[pos : pos+4])
			n := int(binary.LittleEndian.Uint32(payload[pos+4 : pos+8]))
			pos += 8
			if n < 0 || pos+n > len(payload) {
				break
			}
			val := string(payload[pos : pos+n])
			pos += n
			if n%2 == 1 && pos < len(payload) {
				pos++ // pad byte
			}
			k := infoKeyFor(id)
			if k == "" {
				continue
			}
			out[k] = append(out[k], decodeWavText([]byte(val), utf8Declared))
		}
	}
	return out, nil
}

// infoKeyFor maps the MCI ids we write back onto Vorbis-style keys.
func infoKeyFor(id string) string {
	for k, v := range wavInfoID {
		if v == id {
			return k
		}
	}
	return ""
}

func decodeWavText(b []byte, declared bool) string {
	if declared || utf8.Valid(b) {
		return string(b)
	}
	text, _, err := cue.Decode(b, cue.DetectOptions{})
	if err == nil {
		return text
	}
	return string(b)
}
