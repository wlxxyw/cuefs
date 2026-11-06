//go:build linux

package fs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cuefs/internal/plan"
)

// gcTick is the periodic bookkeeping cadence. Everything the task does is
// metastable bookkeeping: it can wait a minute.
const gcTick = time.Minute

// gcAccessGrace keeps a track standing for a short while after the source
// changed: a reader holding it open finishes before the row vanishes,责 and
// Navidrome's scanner does not see half a second of 404s.
const gcAccessGrace = 5 * time.Minute

// accessCap bounds the access table: when it grows past the cap, the oldest
// half is dropped at the next tick - such entries are "cold, and their
// generated file's own mtime takes over as the last-access estimate".
const accessCap = 8192

// gcState keeps the two tables 方案二 distinguishes (DESIGN §4.8):
//
//   - the SOURCE mtime table, keyed per album (one cue + one source file).
//     Complete and never evicted: losing an entry here would make a later
//     source change invisible; it is also tiny (one entry per album).
//   - the access table, keyed per generated track. Lossy by design - a
//     missing entry is simply cold, with the file's own mtime standing in
//     as the lastAccess estimate (batch-split files keep their write time
//     that way, say, and freshly synthesised ones their materialise stamp).
type gcState struct {
	mu sync.Mutex
	// albumMtime does double duty: it IS the table, and its records carry
	// the album plan needed to enumerate each album's cache files.
	albums map[string]*gcAlbum
	access map[string]time.Time
}

// gcAlbum is one tracker: the album plan (paths, track names) plus the
// source mtime at registration time.
type gcAlbum struct {
	album *plan.Album
	mtime time.Time
}

// newGCState starts the state with the tables empty.
func newGCState() *gcState {
	return &gcState{albums: map[string]*gcAlbum{}, access: map[string]time.Time{}}
}

// register puts one album under bookkeeping (called by buildView as albums
// are classified; a re-scan of the same cue keeps the existing mtime - the
// periodic task owns it from then on).
func (g *gcState) register(album *plan.Album) {
	if album == nil {
		return
	}
	mtime := trackMtime(album.CuePath, album.SourcePath)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.albums[album.CuePath]; ok {
		return
	}
	g.albums[album.CuePath] = &gcAlbum{album: album, mtime: mtime}
}

// recordAccess marks a generated track as touched.
func (g *gcState) recordAccess(cachePath string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.access == nil {
		g.access = map[string]time.Time{}
	}
	g.access[cachePath] = now
}

// dropAlbum forgets an album (and its tracks' access rows) entirely.
func (g *gcState) dropAlbum(cuePath, albumDir string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.albums, cuePath)
	prefix := filepath.ToSlash(albumDir) + string(os.PathSeparator)
	for p := range g.access {
		s := filepath.ToSlash(p)
		if s == filepath.ToSlash(albumDir) || strings.HasPrefix(s, prefix+"/") {
			delete(g.access, p)
		}
	}
}

// trackIdle reports whether one generated file may go: its recorded access
// or the file's own mtime (whichever is later) is older than the grace.
func (g *gcState) trackIdle(cachePath string, now time.Time) bool {
	var access time.Time
	g.mu.Lock()
	access = g.access[cachePath]
	g.mu.Unlock()
	if info, err := os.Stat(cachePath); err == nil && info.ModTime().After(access) {
		access = info.ModTime()
	}
	if access.IsZero() {
		return true // nothing there: as idle as it gets
	}
	return now.Sub(access) >= gcAccessGrace
}

// albumIdle folds the track states into the album's eviction decision:
// the whole album goes only when none of it was touched inside the grace.
func (g *gcState) albumIdle(album *plan.Album, now time.Time) bool {
	for _, t := range album.Tracks {
		if !g.trackIdle(filepath.Join(album.OutDir, t.Name), now) {
			return false
		}
	}
	return true
}

// dropAccessWorst halves the access table when it overflows the cap: such
// entries mean cold, and their files' mtimes remain the access estimate.
func (g *gcState) dropAccessWorst() {
	if len(g.access) <= accessCap {
		return
	}
	type kt struct {
		path string
		at   time.Time
	}
	all := make([]kt, 0, len(g.access))
	for p, at := range g.access {
		all = append(all, kt{p, at})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	for _, k := range all[:len(all)/2] {
		delete(g.access, k.path)
	}
}

// lastAccess album-level folding for the eviction ranking: the newest touch
// among the album's on-disk track files, with the file mtime fallback.
func (g *gcState) albumLastAccess(album *plan.Album, now time.Time) time.Time {
	var newest time.Time
	for _, t := range album.Tracks {
		p := filepath.Join(album.OutDir, t.Name)
		at := time.Time{}
		g.mu.Lock()
		at = g.access[p]
		g.mu.Unlock()
		if info, err := os.Stat(p); err == nil && info.ModTime().After(at) {
			at = info.ModTime()
		}
		if at.After(newest) {
			newest = at
		}
	}
	return newest
}
