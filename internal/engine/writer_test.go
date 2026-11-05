package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cuefs/internal/plan"
)

// writeTestWav produces a small 16-bit stereo WAV: 8 samples = 32 bytes of PCM.
func writeTestWav(t *testing.T, dir, name string) string {
	t.Helper()
	pcm := make([]byte, 8*4)
	for i := range pcm {
		pcm[i] = byte(i + 1)
	}
	// A minimal RIFF/WAVE, authored locally so that the test does not depend
	// on the audio package's fixtures (writer only needs a valid byte range).
	riff := func() []byte {
		align := 4
		b := []byte("RIFF\x00\x00\x00\x00WAVE")

		putChunk := func(id string, payload []byte) {
			chunk := append([]byte(id),
				byte(len(payload)), byte(len(payload)>>8),
				byte(len(payload)>>16), byte(len(payload)>>24))
			chunk = append(chunk, payload...)
			b = append(b, chunk...)
		}
		putChunk("fmt ", []byte{
			1, 0, // PCM
			2, 0, // channels
			128, 31, 0, 0, // 8000 Hz
			128, 31, 0, 0, // byte rate
			byte(align), 0, // block align
			16, 0, // bits
		})
		putChunk("data", pcm)
		size := uint32(len(b) - 8)
		b[4], b[5], b[6], b[7] = byte(size), byte(size>>8), byte(size>>16), byte(size>>24)
		return b
	}()
	src := filepath.Join(dir, name)
	if err := os.WriteFile(src, riff, 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestWriteTrackDispatch(t *testing.T) {
	dir := t.TempDir()
	src := writeTestWav(t, dir, "album.wav")
	track := &plan.Track{Num: 1, Title: "T", Start: 2, End: 6}

	// wav→wav works, purely in Go.
	out := filepath.Join(dir, "track.wav")
	if err := WriteTrack(src, Facts{8000, 2, 16}, track, out, Options{}); err != nil {
		t.Fatalf("wav→wav: %v", err)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("wav→wav output missing: %v", err)
	}

	// Every other combination is a config mistake and must fail loudly,
	// _before_ any cache file is created.
	for _, outName := range []string{"track.flac", "track.m4a"} {
		out := filepath.Join(dir, outName)
		err := WriteTrack(src, Facts{8000, 2, 16}, track, out, Options{})
		if err == nil {
			t.Fatalf("wav→%s must fail", filepath.Ext(outName))
		}
		if !strings.Contains(err.Error(), "only flac→flac, wav→wav") {
			t.Errorf("wav→%s: error %q is not actionable", filepath.Ext(outName), err)
		}
		if _, serr := os.Stat(out); serr == nil {
			t.Errorf("wav→%s must not leave a file behind", outName)
		}
	}
}
