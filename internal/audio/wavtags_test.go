package audio

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteWavTagsRoundTrip covers the WAV tag path: an ID3v2.4 chunk and a
// RIFF LIST INFO (+CSET) chunk are attached, read back through the same
// reader the base-tag merge uses, and a retag replaces rather than
// duplicates the previous chunks.
func TestWriteWavTagsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "track.wav")
	pcm := make([]byte, 4096)
	if err := os.WriteFile(path, buildWav(16, 2, 44100, pcm), 0o644); err != nil {
		t.Fatal(err)
	}

	tags := []VorbisEntry{
		{Key: "TITLE", Value: "灰姑娘的眼泪"},
		{Key: "ARTIST", Value: "王心凌"},
		{Key: "ALBUM", Value: "BEGIN"},
		{Key: "DATE", Value: "2003"},
		{Key: "GENRE", Value: "Pop"},
		{Key: "TRACKNUMBER", Value: "01"},
		{Key: "TRACKTOTAL", Value: "10"},
	}
	if err := WriteWavTags(path, tags); err != nil {
		t.Fatal(err)
	}

	// The container must still parse and the RIFF size must cover the tags.
	w, err := OpenWav(path)
	if err != nil {
		t.Fatalf("container broken by tagging: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(binary.LittleEndian.Uint32(raw[4:]) + 8); got != int64(len(raw)) {
		t.Errorf("RIFF size says %d bytes, file is %d", got, len(raw))
	}
	if w.DataLen != int64(len(pcm)) {
		t.Errorf("data chunk is %d bytes, want %d", w.DataLen, len(pcm))
	}

	// Both chunks are present.
	id3, info := false, false
	pos := int64(12)
	for pos+8 <= int64(len(raw)) {
		id := string(raw[pos : pos+4])
		cs := int64(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		switch id {
		case "ID3 ":
			id3 = true
		case "LIST":
			if string(raw[pos+8:pos+12]) == "INFO" {
				info = true
			}
		}
		pos += 8 + cs + cs%2
	}
	if !id3 || !info {
		t.Errorf("missing tag chunks (id3=%v info=%v)", id3, info)
	}

	// Read back through the reader the tag merge uses.
	got, err := ReadWavTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["TITLE"][0] != "灰姑娘的眼泪" {
		t.Errorf("TITLE = %q, want the Chinese title intact (CSET/UTF-8)", got["TITLE"])
	}
	if got["ALBUM"][0] != "BEGIN" || got["DATE"][0] != "2003" || got["GENRE"][0] != "Pop" {
		t.Errorf("album tags read back wrong: %+v", got)
	}

	// Retag replaces: a second write must not duplicate the chunks, and the
	// new values must win.
	tags[0].Value = "新标题"
	if err := WriteWavTags(path, tags); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count, id := 0, ""
	pos = 12
	for pos+8 <= int64(len(raw)) {
		id = string(raw[pos : pos+4])
		cs := int64(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		if id == "ID3 " {
			count++
		}
		if cs < 0 || pos+8+cs > int64(len(raw)) {
			t.Fatalf("chunk %q runs past the end after retag", id)
		}
		pos += 8 + cs + cs%2
	}
	if count != 1 {
		t.Errorf("retag left %d ID3 chunks, want 1", count)
	}
	got, err = ReadWavTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["TITLE"][0] != "新标题" {
		t.Errorf("TITLE after retag = %q", got["TITLE"])
	}
}

// TestReadWavTagsIgnoresNonInfoListChunks: a LIST chunk of another kind (adtl)
// must not be mistaken for tags.
func TestReadWavTagsIgnoresOtherLists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.wav")
	if err := os.WriteFile(path, buildWav(16, 2, 44100, make([]byte, 512)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Append a "LIST adtl" chunk by hand.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("adtl"), []byte("note")...)
	if _, err := f.Write(riffChunk("LIST", payload)); err != nil {
		t.Fatal(err)
	}
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(fileSize(t, path)-8))
	f.WriteAt(sz[:], 4)
	f.Close()

	got, err := ReadWavTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("non-INFO LIST produced tags: %+v", got)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}
