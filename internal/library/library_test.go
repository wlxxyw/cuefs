package library

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"cuefs/internal/cue"
)

const singleFileCue = `TITLE "Album"
PERFORMER "Artist"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "One"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Two"
    INDEX 01 04:00:00
`

const perTrackCue = `TITLE "Album"
FILE "01 - One.flac" WAVE
  TRACK 01 AUDIO
    TITLE "One"
    INDEX 01 00:00:00
FILE "02 - Two.flac" WAVE
  TRACK 01 AUDIO
    TITLE "Two"
    INDEX 01 00:00:00
`

var exts = []string{".flac", ".wav", ".ape"}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindSource(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		cue   string
		want  string // "" = not found
	}{
		{"plain", []string{"album.flac"}, "album.cue", "album.flac"},
		{"upper ext", []string{"album.FLAC"}, "album.cue", "album.FLAC"},
		{"wav", []string{"album.wav"}, "album.cue", "album.wav"},
		{"ape", []string{"album.ape"}, "album.cue", "album.ape"},
		{"double ext", []string{"album.flac"}, "album.flac.cue", "album.flac"},
		{"missing", []string{"other.flac"}, "album.cue", ""},
		{"only art", []string{"cover.jpg"}, "album.cue", ""},
		{"wrong ext", []string{"album.mp3"}, "album.cue", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range c.files {
				write(t, dir, f, "x")
			}
			cuePath := write(t, dir, c.cue, singleFileCue)

			got, ok := FindSource(cuePath, exts)
			if c.want == "" {
				if ok {
					t.Fatalf("expected no source, got %q", got)
				}
				return
			}
			if !ok {
				t.Fatalf("expected source %q", c.want)
			}
			if filepath.Base(got) != c.want {
				t.Fatalf("got %q, want %q", filepath.Base(got), c.want)
			}
		})
	}
}

func TestClassifyRules(t *testing.T) {
	t.Run("split", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "album.flac", "audio")
		e := Classify(write(t, dir, "album.cue", singleFileCue), exts, cue.DetectOptions{})
		if e.Kind != KindSplit || e.Reason != ReasonSingleFile {
			t.Fatalf("got %s/%s", e.Kind, e.Reason)
		}
		if e.CueFiles != 1 || e.Tracks != 2 {
			t.Fatalf("files=%d tracks=%d", e.CueFiles, e.Tracks)
		}
		if e.Encoding == "" {
			t.Fatal("encoding must be reported")
		}
	})

	t.Run("per-track cue is passed through", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "01 - One.flac", "audio")
		write(t, dir, "02 - Two.flac", "audio")
		e := Classify(write(t, dir, "album.cue", perTrackCue), exts, cue.DetectOptions{})
		if e.Kind != KindPassthrough || e.Reason != ReasonNoSource {
			t.Fatalf("got %s/%s", e.Kind, e.Reason)
		}
	})

	t.Run("multi-file cue with a source is not guessed", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "album.flac", "audio")
		e := Classify(write(t, dir, "album.cue", perTrackCue), exts, cue.DetectOptions{})
		if e.Kind != KindPassthrough || e.Reason != ReasonMultiFile {
			t.Fatalf("got %s/%s", e.Kind, e.Reason)
		}
	})

	t.Run("unparsable cue is an error", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "album.flac", "audio")
		e := Classify(write(t, dir, "album.cue", "not a cue sheet at all\n"), exts, cue.DetectOptions{})
		if e.Kind != KindError || e.Err == "" {
			t.Fatalf("got %s err=%q", e.Kind, e.Err)
		}
	})
}

// TestScanIsReadOnly is the regression guard for DESIGN D15: scanning must
// leave the source tree byte-for-byte identical and create nothing.
func TestScanIsReadOnly(t *testing.T) {
	root := t.TempDir()
	album := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(album, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, album, "album.flac", "pretend audio bytes")
	write(t, album, "album.cue", singleFileCue)
	write(t, album, "01 - One.flac", "already split")
	write(t, album, "01 - One.cue", perTrackCue)
	write(t, album, "cover.jpg", "jpeg")
	write(t, album, "album.log", "eac log")

	before := snapshot(t, root)

	var got []Entry
	if err := Scan(root, exts, cue.DetectOptions{}, func(e Entry) { got = append(got, e) }); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d cue files, want 2", len(got))
	}

	after := snapshot(t, root)
	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Errorf("%s changed during scan", name)
		}
	}

	// One split candidate, one passthrough.
	var split, pass int
	for _, e := range got {
		switch e.Kind {
		case KindSplit:
			split++
		case KindPassthrough:
			pass++
		}
	}
	if split != 1 || pass != 1 {
		t.Fatalf("split=%d passthrough=%d, want 1/1", split, pass)
	}

	var s Summary
	for _, e := range got {
		s.Add(e)
	}
	if s.Cues != 2 || s.Split != 1 || s.Passthrough != 1 || s.Tracks != 2 {
		t.Fatalf("summary = %+v", s)
	}
}

// snapshot maps every file under root to its content hash-ish signature.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIsCueFile(t *testing.T) {
	for _, yes := range []string{"a.cue", "A.CUE", "目录/中文名.cue"} {
		if !IsCueFile(yes) {
			t.Errorf("%q should be a cue file", yes)
		}
	}
	for _, no := range []string{"a.flac", "a.log", "cue", "a.cue.bak"} {
		if IsCueFile(no) {
			t.Errorf("%q should not be a cue file", no)
		}
	}
}

func TestScanSkipsUnreadableDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions are not enforced on Windows")
	}
	root := t.TempDir()
	good := filepath.Join(root, "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, good, "album.flac", "x")
	write(t, good, "album.cue", singleFileCue)

	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	var n int
	if err := Scan(root, exts, cue.DetectOptions{}, func(Entry) { n++ }); err != nil {
		t.Fatalf("Scan must survive unreadable directories: %v", err)
	}
	if n != 1 {
		t.Fatalf("classified %d cues, want 1", n)
	}
}

func TestApeOf(t *testing.T) {
	cases := map[string]ApeAction{
		"":            ApeConvert,
		"convert":     ApeConvert,
		" CONVERT ":   ApeConvert,
		"hide":        ApeHide,
		"Hide":        ApeHide,
		"passthrough": ApePassthrough,
		"PASSTHROUGH": ApePassthrough,
		"weird":       ApeConvert,
	}
	for in, want := range cases {
		if got := ApeOf(in); got != want {
			t.Errorf("ApeOf(%q) = %q, want %q", in, got, want)
		}
	}
	if !IsApe("x.APE") || IsApe("x.flac") || IsApe("x.apelike") {
		t.Error("IsApe must match only the .ape extension")
	}
}
