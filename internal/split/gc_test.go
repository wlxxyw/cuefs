package split

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"cuefs/internal/plan"
)

// buildGCLayout makes a source+cache pair that mocks a completed split:
// one planned album (with tracks + cover inside the cache), one mirrored
// passthrough file, and a couple of orphans from a deleted album.
func buildGCLayout(t *testing.T) (srcRoot, outRoot string, p *Plan) {
	t.Helper()
	srcRoot = t.TempDir()
	outRoot = t.TempDir()
	srcAlbum := filepath.Join(srcRoot, "专辑A")
	outAlbum := filepath.Join(outRoot, "专辑A")
	if err := os.MkdirAll(srcAlbum, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outAlbum, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outRoot, "专辑B"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcAlbum, "albumA.cue"), []byte("TITLE t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcAlbum, "albumA.flac"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outAlbum, "01 - a.flac"), []byte("pcm"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outAlbum, "cover.jpg"), []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	// the mirrored passthrough: keep a .m3u next to the cue
	if err := os.WriteFile(filepath.Join(srcAlbum, "lists.m3u"), []byte("ep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outAlbum, "lists.m3u"), []byte("ep"), 0o644); err != nil {
		t.Fatal(err)
	}
	// cache debris from the vanished source B: tracks plus a stale .part.
	if err := os.WriteFile(filepath.Join(outRoot, "专辑B", "01 - b.flac"), []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outRoot, "专辑B", "01 - b.flac.part"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * gcStalePart)
	if err := os.Chtimes(filepath.Join(outRoot, "专辑B", "01 - b.flac.part"), old, old); err != nil {
		t.Fatal(err)
	}
	// a live part of a running copy must stay
	if err := os.WriteFile(filepath.Join(outAlbum, "02.flac.part"), []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The plan as BuildPlan would render it for the surviving album.
	album := &plan.Album{
		CuePath:    filepath.Join(srcAlbum, "albumA.cue"),
		SourcePath: filepath.Join(srcAlbum, "albumA.flac"),
		OutDir:     outAlbum,
		Extractor:  true,
		Tracks: []*plan.Track{
			{Name: "01 - a.flac"},
		},
	}
	p = &Plan{
		Root:            srcRoot,
		Albums:          []*plan.Album{album},
		SelDirs:         map[string]bool{".": true, "专辑A": true},
		AllDirs:         true,
		Sampled:         map[string]bool{album.SourcePath: true},
		Hidden:          map[string]bool{},
		PassthroughCues: map[string]bool{},
	}
	return srcRoot, outRoot, p
}

func TestPruneOrphans(t *testing.T) {
	srcRoot, outRoot, p := buildGCLayout(t)
	o := Options{SrcRoot: srcRoot, OutRoot: outRoot}

	st, err := prunePass(o, p, &GcStats{})
	if err != nil {
		t.Fatal(err)
	}
	if st.OrphanFiles != 1 || st.PartFiles != 1 {
		t.Errorf("orphans=%d parts=%d, want 1 each (kept the live .part)",
			st.OrphanFiles, st.PartFiles)
	}
	for _, kept := range []string{"01 - a.flac", "cover.jpg", "lists.m3u", "02.flac.part"} {
		if _, err := os.Stat(filepath.Join(outRoot, "专辑A", kept)); err != nil {
			t.Errorf("%s was deleted: %v", kept, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outRoot, "专辑B")); !os.IsNotExist(err) {
		t.Errorf("orphan album dir should have been swept: %v", err)
	}
}

func TestPruneIsDryAware(t *testing.T) {
	srcRoot, outRoot, p := buildGCLayout(t)
	o := Options{SrcRoot: srcRoot, OutRoot: outRoot, Dry: true}

	st, err := prunePass(o, p, &GcStats{})
	if err != nil {
		t.Fatal(err)
	}
	if st.OrphanFiles != 1 {
		t.Errorf("dry run should still count the orphan: got %d", st.OrphanFiles)
	}
	for _, kept := range []string{
		filepath.Join(outRoot, "专辑B", "01 - b.flac"),
		filepath.Join(outRoot, "专辑B", "01 - b.flac.part"),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("dry run deleted %s", kept)
		}
	}
}
