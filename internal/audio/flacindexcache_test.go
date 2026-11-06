package audio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIndexCacheSharesOneWalk: repeated Index calls over an unchanged file
// hand back the very same index object, and a file rewrite is re-walked.
func TestIndexCacheSharesOneWalk(t *testing.T) {
	dir := t.TempDir()
	si := streamInfoBytes(192, 44100, 2, 16, 192) // one whole 192-sample frame
	path := filepath.Join(dir, "album.flac")
	if err := os.WriteFile(path, buildFlac(si, nil, [][]byte{
		flacFrame(1, 9, 0x10, 0, []byte{0x11, 0x22, 0x33, 0x44, 0xFF, 0xF8, 0x55, 0x66, 0x77}),
	}), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewIndexCache(0)
	first, err := c.Index(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Index(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("cache returned two different objects for the same unchanged file")
	}
	if c.Len() != 1 {
		t.Errorf("cache holds %d entries, want 1", c.Len())
	}

	// Rewrite the file (new size): the cache must not serve the old walk.
	bigger := buildFlac(si, nil, [][]byte{
		flacFrame(1, 9, 0x10, 0, []byte{0x11, 0x22, 0x33, 0x44, 0xFF, 0xF8, 0x55, 0x66, 0x77}),
		flacFrame(1, 9, 0x10, 1, []byte{0x01, 0x02, 0x03}),
	})
	if err := os.WriteFile(path, bigger, 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := c.Index(path)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Error("stale index served after the file was rewritten")
	}

	// Silent rewrite (same size, new mtime) must also invalidate.
	if err := os.Chtimes(path, time.Now().Add(2*time.Second), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	fourth, err := c.Index(path)
	if err != nil {
		t.Fatal(err)
	}
	if fourth == first {
		t.Error("stale index served after a mtime-only rewrite")
	}
}

// TestIndexCacheEvictsOldest: capacity is bounded, least-recently-used goes.
func TestIndexCacheEvictsOldest(t *testing.T) {
	c := NewIndexCache(2)
	body := []byte{0x11, 0x22, 0x33, 0x44, 0xFF, 0xF8, 0x55, 0x66, 0x77}
	for i, name := range []string{"a", "b", "c"} {
		si := streamInfoBytes(192, 44100, 2, 16, 192)
		path := filepath.Join(t.TempDir(), name+".flac")
		if err := os.WriteFile(path, buildFlac(si, nil, [][]byte{
			flacFrame(1, 9, 0x10, byte(i), body),
		}), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Index(path); err != nil {
			t.Fatal(err)
		}
		if c.Len() > 2 {
			t.Fatalf("cache grew to %d entries with cap 2", c.Len())
		}
		time.Sleep(2 * time.Millisecond) // separate the used stamps
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
	// "a" was evicted: indexes b, c only.
	c.mu.Lock()
	for k := range c.entries {
		if filepath.Base(k) == "a.flac" {
			t.Errorf("oldest entry a.flac still cached: %s", k)
		}
	}
	c.mu.Unlock()
}

// TestIndexCacheIsNotPoisonedByErrors: an erroring walk is not stored, so a
// later successful call re-walks instead of serving a failure.
func TestIndexCacheIsNotPoisonedByErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.flac")
	if err := os.WriteFile(path, []byte("not a flac"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewIndexCache(0)
	if _, err := c.Index(path); err == nil {
		t.Fatal("expected an error walking garbage")
	}
	// Turn it into a valid file: the next call must succeed.
	si := streamInfoBytes(192, 44100, 2, 16, 192)
	if err := os.WriteFile(path, buildFlac(si, nil, [][]byte{
		flacFrame(1, 9, 0x10, 0, []byte{0x01, 0x02, 0x03}),
	}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Index(path); err != nil {
		t.Fatalf("error was cached along with the failure: %v", err)
	}
}

// countingResolver records every request, returning canned results.
type countingResolver struct {
	calls map[string]int
	idx   *FlacIndex
	err   error
}

func (r *countingResolver) Index(path string) (*FlacIndex, error) {
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[path]++
	if r.err != nil {
		return r.idx, r.err
	}
	return r.idx, nil
}

// TestIndexCacheDeduplicatesInFlightWalks: when N workers start on the same
// album, exactly ONE walk runs; the others wait for its result instead of
// racing their own.
func TestIndexCacheDeduplicatesInFlightWalks(t *testing.T) {
	si := streamInfoBytes(192, 44100, 2, 16, 192)
	path := filepath.Join(t.TempDir(), "album.flac")
	if err := os.WriteFile(path, buildFlac(si, nil, [][]byte{
		flacFrame(1, 9, 0x10, 0, []byte{0x01, 0x02, 0x03}),
	}), 0o644); err != nil {
		t.Fatal(err)
	}

	// A deliberately slow walk makes the join window real.
	orig := indexWalkFile
	var walks int
	slow := make(chan struct{})
	indexWalkFile = func(p string) (*FlacIndex, error) {
		walks++
		idx, err := orig(p)
		<-slow
		return idx, err
	}
	defer func() { indexWalkFile = orig }()
	go func() {
		time.Sleep(100 * time.Millisecond) // let every caller pile up
		close(slow)
	}()

	c := NewIndexCache(0)
	const n = 8
	type outcome struct {
		idx *FlacIndex
		err error
	}
	out := make(chan outcome, n)
	for i := 0; i < n; i++ {
		go func() {
			idx, err := c.Index(path)
			out <- outcome{idx, err}
		}()
	}
	var first *FlacIndex
	for i := 0; i < n; i++ {
		o := <-out
		if o.err != nil {
			t.Fatalf("caller %d: %v", i, o.err)
		}
		if first == nil {
			first = o.idx
		} else if first != o.idx {
			t.Error("waiters received different index objects")
		}
	}
	if walks != 1 {
		t.Errorf("%d walks ran for %d concurrent callers, want 1", walks, n)
	}
	if c.Len() != 1 {
		t.Errorf("cache holds %d entries, want 1", c.Len())
	}
}

// TestWriteFLACTrackUsesInjectedIndexer: the split path never walks the
// source itself when a resolver is supplied - even when the resolver reports
// the partial-chain contract (index, error).
func TestWriteFLACTrackUsesInjectedIndexer(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "album.flac")
	out := filepath.Join(dir, "01 - track.flac")
	// One whole 4096-sample frame: the writer's own verbatim encoder
	// produces a complete, self-validating frame - bscode 7 with the
	// 16-bit size field, CRC-8/CRC-16 in place.
	const n = 4096
	ch0 := make([]int32, n)
	ch1 := make([]int32, n)
	for i := range ch0 {
		ch0[i] = int32(i%2)*7 - 3
		ch1[i] = int32(i%3) - 1
	}
	frame, err := encodeVerbatimFrame([][]int32{ch0, ch1}, 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	si := streamInfoBytes(n, 44100, 2, 16, n) // MaxBlocksize >= 4096
	if err := os.WriteFile(src, buildFlac(si, []VorbisEntry{
		{Key: "TITLE", Value: "x"},
	}, [][]byte{frame}), 0o644); err != nil {
		t.Fatal(err)
	}

	// One real walk up front, then report BOTH the index and the injected
	// error every time - the contract a chain-broken source produces. The
	// window [0,4096) is covered by frame 0, so the split must proceed and
	// count exactly ONE call.
	real, err := IndexFlacFrames(src)
	if err != nil {
		t.Fatal(err)
	}
	r := &countingResolver{idx: real, err: errors.New("chain broken at 4400")}
	spec := SplitSpec{
		Src: src, Start: 0, End: 4096,
		SampleRate: 44100, Channels: 2, Bits: 16,
		Indexer: r,
	}
	if err := WriteFLACTrack(spec, out); err != nil {
		t.Fatalf("split failed: %v", err)
	}
	if r.calls[src] != 1 {
		t.Errorf("resolver called %d time(s), want 1", r.calls[src])
	}
}
