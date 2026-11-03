package audio

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestIndexFlacFrames uses synthetic frames that are valid by construction
// (correct CRC-8 in the header, correct CRC-16 at the end) and that embed a
// false sync word in a frame body to force the scanner to resynchronise.
func TestIndexFlacFrames(t *testing.T) {
	si := streamInfoBytes(192, 44100, 2, 16, 576) // 3 frames of 192 samples
	body := []byte{0x11, 0x22, 0x33, 0x44, 0xFF, 0xF8, 0x55, 0x66, 0x77}

	frames := [][]byte{
		flacFrame(1, 9, 1, 0x00, body), // 192 samples, 44100 Hz, 2 ch
		flacFrame(1, 9, 1, 0x01, body),
		flacFrame(1, 9, 1, 0x02, body),
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "album.flac")
	if err := os.WriteFile(path, buildFlac(si, nil, frames), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := IndexFlacFrames(path)
	if err != nil {
		t.Fatalf("IndexFlacFrames: %v", err)
	}
	if len(idx.Frames) != 3 {
		t.Fatalf("frames = %d, want 3 (%+v)", len(idx.Frames), idx.Frames)
	}
	for i, f := range idx.Frames {
		if f.Samples != 192 {
			t.Errorf("frame %d has %d samples", i, f.Samples)
		}
		if f.Start != int64(i)*192 {
			t.Errorf("frame %d starts at %d, want %d", i, f.Start, int64(i)*192)
		}
		if f.Length != len(frames[i]) {
			t.Errorf("frame %d length %d, want %d", i, f.Length, len(frames[i]))
		}
	}
	if idx.TotalSamples != 576 {
		t.Errorf("total samples = %d, want 576", idx.TotalSamples)
	}
	if idx.Trailer != 0 {
		t.Errorf("trailer = %d, want 0", idx.Trailer)
	}

	// The frame offsets must point at the real sync words inside the file.
	raw, _ := os.ReadFile(path)
	for i, f := range idx.Frames {
		if !bytes.HasPrefix(raw[f.Offset:], []byte{0xFF, 0xF8}) {
			t.Errorf("frame %d does not start at a sync word", i)
		}
	}
}

// A corrupted frame (bad CRC-16 at the end) must be rejected, and the false
// syncs must not be mistaken for frame starts.
// TestIndexFlacFramesRejectsDamage: the frame chain is strict, so a frame
// whose CRC-16 does not validate stops the walk - exactly what a split
// backend wants, because a hole in the chain cannot be papered over.
func TestIndexFlacFramesRejectsDamage(t *testing.T) {
	si := streamInfoBytes(192, 44100, 2, 16, 192)
	good := flacFrame(1, 9, 1, 0x00, []byte{0x01, 0x02, 0x03})
	broken := flacFrame(1, 9, 1, 0x01, []byte{0x04, 0x05, 0x06})
	broken[len(broken)-2] ^= 0xFF // flip a CRC byte

	dir := t.TempDir()
	path := filepath.Join(dir, "album.flac")
	if err := os.WriteFile(path, buildFlac(si, nil, [][]byte{good, broken}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := IndexFlacFrames(path); err == nil {
		t.Fatal("a damaged frame must break the frame chain walk")
	}
}

// TestParseFrameHeaderCoversWidths checks the coded-number widths and the
// optional block size fields.
func TestParseFrameHeaderCoversWidths(t *testing.T) {
	si := StreamInfo{MinBlocksize: 192, MaxBlocksize: 192, SampleRate: 44100, Present: true}
	cases := []struct {
		name      string
		bsCode    int
		coded     []byte
		want      int
		headerLen int
	}{
		{"192 fixed", 1, []byte{0x00}, 192, 6},
		{"1152 fixed", 3, []byte{0x07}, 1152, 6},
		{"4608 fixed", 5, []byte{0x07}, 4608, 6},
		{"8-bit size", 6, []byte{0x00, 0xFF}, 256, 7},
		{"16-bit size", 7, []byte{0x00, 0xFF, 0xFF}, 65536, 8},
		{"2-byte number", 1, []byte{0xC0, 0x80}, 192, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// sr code 9 (44100), channel+bits nibble 0x10 (2ch, from STREAMINFO)
			hdr := append([]byte{0xFF, 0xF8, byte(c.bsCode)<<4 | 9, 0x10}, c.coded...)
			hdr = append(hdr, crc8(hdr))
			n, f, err := parseFrameHeader(hdr, &si)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if n != c.headerLen {
				t.Errorf("header length = %d, want %d", n, c.headerLen)
			}
			if f.Samples != c.want {
				t.Errorf("samples = %d, want %d", f.Samples, c.want)
			}
		})
	}
}
