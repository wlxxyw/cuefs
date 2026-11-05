//go:build linux

package fs

import (
	"os"
	"path/filepath"
	"testing"
)

// writeApePair lays down a classic single-image pair: the cue and its APE.
func writeApePair(t *testing.T) string {
	t.Helper()
	const cue = `TITLE "APE Album"
PERFORMER "Artist"
FILE "album.ape" WAVE
  TRACK 01 AUDIO
    TITLE "One"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Two"
    INDEX 01 02:00:00
`
	src := t.TempDir()
	for _, f := range []struct {
		name, body string
	}{
		{"album.ape.cue", cue},
		{"album.ape", "MAC\x11\x00\x00\x00not-a-real-monkeys-audio"},
	} {
		if err := os.WriteFile(filepath.Join(src, f.name), []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

// TestOverlayApeModes checks that ape_mode governs the whole pair, not just
// the audio: hide drops the cue sheet along with the ape source, while
// passthrough restores the cue to the view (a bare .ape is unplayable).
func TestOverlayApeModes(t *testing.T) {
	src := writeApePair(t)
	for _, tc := range []struct {
		mode             string
		wantCueHidden    bool
		wantSourceHidden bool
		wantTracks       int
	}{
		{"", true, false, 0},             // convert: probe fails, faulty albums stay visible
		{"convert", true, false, 0},      // ditto
		{"hide", true, true, 0},          // the pair disappears together
		{"passthrough", false, false, 0}, // cue + ape remain, unsplit
	} {
		o := Options{
			Src:         src,
			Cache:       t.TempDir(),
			ApeMode:     tc.mode,
			MaxTitleLen: 60,
			Log:         func(string, ...any) {},
		}
		o.defaults()
		root := &Root{opts: o}
		v, err := root.buildView("")
		if err != nil {
			t.Fatalf("buildView(%q): %v", tc.mode, err)
		}
		if n := len(v.tracks); n != tc.wantTracks {
			t.Errorf("%q: tracks = %d, want %d", tc.mode, n, tc.wantTracks)
		}
		if _, ok := v.hide["album.ape.cue"]; ok != tc.wantCueHidden {
			t.Errorf("%q: cue hidden = %v, want %v", tc.mode, ok, tc.wantCueHidden)
		}
		if _, ok := v.hide["album.ape"]; ok != tc.wantSourceHidden {
			t.Errorf("%q: ape hidden = %v, want %v", tc.mode, ok, tc.wantSourceHidden)
		}
	}
}
