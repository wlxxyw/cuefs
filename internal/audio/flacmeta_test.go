package audio

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// streamInfoBytes renders a STREAMINFO payload with the given facts.
func streamInfoBytes(blocksize, rate, channels, bits int, total int64) []byte {
	p := make([]byte, 34)
	binary.BigEndian.PutUint16(p[0:2], uint16(blocksize))
	binary.BigEndian.PutUint16(p[2:4], uint16(blocksize))
	v := uint64(rate)<<44 | uint64(channels-1)<<41 | uint64(bits-1)<<36
	if total < 0 {
		total = 0
	}
	v |= uint64(total) & (1<<36 - 1)
	var tail [8]byte
	binary.BigEndian.PutUint64(tail[:], v)
	copy(p[10:18], tail[:])
	return p
}

// flacFrame renders one valid FLAC frame whose body may contain fake sync
// words: header (with CRC-8) + body + CRC-16.
func flacFrame(bsCode, srCode, chanBits int, number byte, body []byte) []byte {
	hdr := []byte{0xFF, 0xF8, byte(bsCode<<4 | srCode), byte(chanBits << 4), number}
	hdr = append(hdr, crc8(hdr))
	full := append(append([]byte{}, hdr...), body...)
	c := crc16(full)
	return append(full, byte(c>>8), byte(c))
}

// buildFlac assembles a FLAC file from metadata plus a frame stream.
func buildFlac(si []byte, comments []VorbisEntry, frames [][]byte) []byte {
	var b bytes.Buffer
	b.WriteString("fLaC")
	b.Write([]byte{0x00, byte(len(si) >> 16), byte(len(si) >> 8), byte(len(si))})
	b.Write(si)
	commentsData := EncodeVorbisComment("cuefs-test", comments)
	b.Write([]byte{0x04, byte(len(commentsData) >> 16), byte(len(commentsData) >> 8), byte(len(commentsData))})
	b.Write(commentsData)
	padding := make([]byte, 8192)
	b.Write([]byte{0x81, 0x00, 0x20, 0x00}) // PADDING, 8192 bytes, last=true
	b.Write(padding)
	for _, f := range frames {
		b.Write(f)
	}
	return b.Bytes()
}

func TestMetaBlocksAndTagRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "track.flac")
	raw := buildFlac(streamInfoBytes(4096, 44100, 2, 16, 576),
		[]VorbisEntry{{Key: "TITLE", Value: "old"}}, [][]byte{{0x01, 0x02, 0x03, 0x04}})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	blocks, si, framesOffset, err := ReadMetaBlocks(path)
	if err != nil {
		t.Fatalf("ReadMetaBlocks: %v", err)
	}
	if len(blocks) != 3 || blocks[0].Kind != BlockStreamInfo ||
		blocks[1].Kind != BlockVorbisComment || blocks[2].Kind != BlockPadding {
		t.Fatalf("blocks = %+v", blocks)
	}
	if !si.Present || si.SampleRate != 44100 || si.Channels != 2 || si.TotalSamples != 576 {
		t.Fatalf("stream info = %+v", si)
	}
	lastEnd := blocks[len(blocks)-1].Offset + blocks[len(blocks)-1].Length
	if framesOffset != lastEnd {
		t.Fatalf("frames offset = %d, want %d", framesOffset, lastEnd)
	}

	if _, entries, err := DecodeVorbisComment(blocks[1].Data); err != nil || len(entries) != 1 || entries[0].Value != "old" {
		t.Fatalf("decode: %v %+v", err, entries)
	}

	// Rewrite: two entries with the same key (multi-value), and check that
	// the padding is trimmed and the frame bytes survive untouched.
	want := []VorbisEntry{
		{Key: "TITLE", Value: "第一首歌"},
		{Key: "ARTIST", Value: "A"},
		{Key: "ARTIST", Value: "B"},
		{Key: "TRACKNUMBER", Value: "01"},
	}
	if err := WriteVorbisComment(path, want); err != nil {
		t.Fatalf("WriteVorbisComment: %v", err)
	}
	blocks2, _, framesOffset2, err := ReadMetaBlocks(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	var got []VorbisEntry
	for _, b := range blocks2 {
		if b.Kind == BlockVorbisComment {
			_, got, err = DecodeVorbisComment(b.Data)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The padding shrink legitimately moves the frame region; the frames
	// themselves must still be byte-identical.
	lastEnd2 := blocks2[len(blocks2)-1].Offset + blocks2[len(blocks2)-1].Length
	if framesOffset2 != lastEnd2 {
		t.Fatalf("re-read frames offset = %d, want %d", framesOffset2, lastEnd2)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after[framesOffset2:], raw[framesOffset:]) {
		t.Error("frame bytes changed during the tag rewrite")
	}
	// The padding must have been clamped to MinPadding and be the last block.
	foundPadding := false
	for _, b := range blocks2 {
		if b.Kind == BlockPadding {
			foundPadding = true
			if len(b.Data) != MinPadding {
				t.Errorf("padding is %d bytes, want %d", len(b.Data), MinPadding)
			}
		}
	}
	if !foundPadding {
		t.Error("padding block disappeared")
	}
	if !blocks2[len(blocks2)-1].Last {
		t.Error("last block must carry the LAST flag")
	}
}

func TestVorbisCommentEncodingIsStable(t *testing.T) {
	data := EncodeVorbisComment("v", []VorbisEntry{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}})
	// each entry: 32-bit length + "KEY=value"
	want := []byte{1, 0, 0, 0, 'v', 2, 0, 0, 0,
		3, 0, 0, 0, 'A', '=', '1',
		3, 0, 0, 0, 'B', '=', '2'}
	if !bytes.Equal(data, want) {
		t.Fatalf("encoded payload is %q", data)
	}
}
