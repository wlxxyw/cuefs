package split

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeApePair lays down a classic single-image pair: the cue and its APE.
// The ape payload is bogus: hide/passthrough never probe the source, which
// is exactly what keeps these modes usable without ffprobe installed.
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
	dir := t.TempDir()
	for _, f := range []struct {
		name, body string
	}{
		{"album.ape.cue", cue},
		{"album.ape", "MAC\x11\x00\x00\x00not-a-real-monkeys-audio"},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSplitApeModes(t *testing.T) {
	src := writeApePair(t)

	for _, tc := range []struct {
		mode             string
		wantAlbums       int
		wantApe, wantCue bool
	}{
		{"hide", 0, false, false},
		{"passthrough", 0, true, true},
		{"convert", 0, true, false}, // probe fails without ffprobe; the pair survives as files
		{"", 0, true, false},
	} {
		t.Run(modeName(tc.mode), func(t *testing.T) {
			out := t.TempDir()
			o := Options{
				SrcRoot: src, OutRoot: out, ApeMode: tc.mode,
				Log: func(string, ...any) {},
			}
			p, err := BuildPlan(o)
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			if len(p.Albums) != tc.wantAlbums {
				t.Errorf("albums = %d, want %d", len(p.Albums), tc.wantAlbums)
			}

			// ape_mode=hide must register the source and, crucially, its
			// cue sheet; passthrough registers only the cue (the ape rides
			// along through the normal mirror because it is not Sampled).
			if tc.mode == "hide" {
				if !p.Hidden[filepath.Join(p.Root, "album.ape")] ||
					!p.Hidden[filepath.Join(p.Root, "album.ape.cue")] {
					t.Errorf("hidden = %v, want both pair members", p.Hidden)
				}
			}
			if tc.mode == "passthrough" && !p.PassthroughCues[filepath.Join(p.Root, "album.ape.cue")] {
				t.Errorf("passthrough cues = %v, want the cue sheet", p.PassthroughCues)
			}

			if _, err := Materialize(o); err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			has := func(name string) bool {
				_, err := os.Stat(filepath.Join(out, name))
				return err == nil
			}
			if has("album.ape") != tc.wantApe {
				t.Errorf("album.ape present = %v, want %v", has("album.ape"), tc.wantApe)
			}
			if has("album.ape.cue") != tc.wantCue {
				t.Errorf("album.ape.cue present = %v, want %v", has("album.ape.cue"), tc.wantCue)
			}
		})
	}
}

// modeName makes the test-case names readable for "" (the default).
func modeName(m string) string {
	if m == "" {
		return "convert-default"
	}
	return m
}

// TestSplitSkipPassthrough: with SkipPassthrough, everything that would be
// mirrored (the APE pair in passthrough mode plus unrelated files) stays out
// of the output tree and only shows up in the skipped counters. Running the
// same plan with the flag off copies them as before.
func TestSplitSkipPassthrough(t *testing.T) {
	src := writeApePair(t)
	extra := filepath.Join(src, "notes.txt")
	if err := os.WriteFile(extra, []byte("readme"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		skip          bool
		wantApe, want bool
		wantSkipped   int
	}{
		{false, true, true, 0},
		{true, false, false, 3},
	} {
		t.Run(fmt.Sprintf("skip=%v", tc.skip), func(t *testing.T) {
			out := t.TempDir()
			o := Options{
				SrcRoot: src, OutRoot: out, ApeMode: "passthrough",
				SkipPassthrough: tc.skip,
				Log:             func(string, ...any) {},
			}
			st, err := Materialize(o)
			if err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			has := func(name string) bool {
				_, err := os.Stat(filepath.Join(out, name))
				return err == nil
			}
			if has("album.ape") != tc.wantApe {
				t.Errorf("album.ape present = %v, want %v", has("album.ape"), tc.wantApe)
			}
			if has("album.ape.cue") != tc.want {
				t.Errorf("album.ape.cue present = %v, want %v", has("album.ape.cue"), tc.want)
			}
			if has("notes.txt") != tc.want {
				t.Errorf("notes.txt present = %v, want %v", has("notes.txt"), tc.want)
			}
			if st.OthersSkipped != tc.wantSkipped {
				t.Errorf("skipped passthrough = %d, want %d", st.OthersSkipped, tc.wantSkipped)
			}
			if tc.skip && st.Others != 0 {
				t.Errorf("copied passthrough = %d, want 0 in skip mode", st.Others)
			}
		})
	}
}
