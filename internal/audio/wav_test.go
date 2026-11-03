package audio

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// buildWav renders a WAV file with the given PCM payload.
func buildWav(bits, channels, rate int, pcm []byte) []byte {
	align := (bits + 7) / 8 * channels
	var b bytes.Buffer
	b.WriteString("RIFF")
	putU32(&b, 0) // patched below
	b.WriteString("WAVE")
	chunk := &bytes.Buffer{}
	uint16Bytes := func(v uint16) []byte {
		var t [2]byte
		binary.LittleEndian.PutUint16(t[:], v)
		return t[:]
	}
	uint32Bytes := func(v uint32) []byte {
		var t [4]byte
		binary.LittleEndian.PutUint32(t[:], v)
		return t[:]
	}
	chunk.Write(uint16Bytes(1)) // PCM
	chunk.Write(uint16Bytes(uint16(channels)))
	chunk.Write(uint32Bytes(uint32(rate)))
	chunk.Write(uint32Bytes(uint32(rate * align)))
	chunk.Write(uint16Bytes(uint16(align)))
	chunk.Write(uint16Bytes(uint16(bits)))
	putChunk(&b, "fmt ", chunk.Bytes())
	putChunk(&b, "data", pcm)
	out := b.Bytes()
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out
}

func TestOpenWavAndSplit(t *testing.T) {
	// 8 samples of 16-bit stereo, i.e. 32-byte payload.
	pcm := make([]byte, 8*4)
	for i := range pcm {
		pcm[i] = byte(i + 1)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "album.wav")
	if err := os.WriteFile(src, buildWav(16, 2, 8000, pcm), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := OpenWav(src)
	if err != nil {
		t.Fatalf("OpenWav: %v", err)
	}
	if w.Info.SampleRate != 8000 || w.Info.Channels != 2 || w.Info.Bits != 16 || w.Info.BlockAlign != 4 {
		t.Fatalf("info = %+v", w.Info)
	}
	if w.DataLen != int64(len(pcm)) {
		t.Fatalf("data len = %d, want %d", w.DataLen, len(pcm))
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	out := filepath.Join(dir, "track.wav")
	// Samples [2,6): bytes 8..24 of the payload.
	if err := w.Split(out, 2, 6); err != nil {
		t.Fatalf("split: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// The output must start with a fresh header followed by the exact bytes.
	w2, err := OpenWav(out)
	if err != nil {
		t.Fatalf("OpenWav(out): %v", err)
	}
	if w2.DataLen != 16 {
		t.Fatalf("out data len = %d, want 16", w2.DataLen)
	}
	if !bytes.Equal(got[w2.DataOffset:int(w2.DataOffset)+16], pcm[8:24]) {
		t.Errorf("payload mismatch")
	}
	if binary.LittleEndian.Uint32(got[4:8]) != uint32(len(got)-8) {
		t.Errorf("RIFF size field is %d, want %d",
			binary.LittleEndian.Uint32(got[4:8]), len(got)-8)
	}

	// An open-ended split runs to the end of the payload.
	if err := w.Split(out, 4, -1); err != nil {
		t.Fatalf("tail split: %v", err)
	}
	w3, _ := OpenWav(out)
	if w3.DataLen != 16 { // 8-4 samples * 4 bytes
		t.Fatalf("tail len = %d, want 16", w3.DataLen)
	}

	// Malformed input must be rejected rather than produce a broken file.
	bad := filepath.Join(dir, "broken.wav")
	if err := os.WriteFile(bad, []byte("not a riff file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWav(bad); err == nil {
		t.Error("OpenWav on a non-RIFF file must fail")
	}
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
}
