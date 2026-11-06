//go:build linux

package fs

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"cuefs/internal/library"
	"cuefs/internal/split"
)

// gcLoop runs the mount's periodic cache task (DESIGN §4.8): it re-checks
// the source mtimes it knows about, evicts by TTL and by size, and prunes
// the album dirs of vanished sources - the same rectification the `gc`
// command performs, but driven by the mount's own tables instead of a fresh
// plan scan.
func (o *Root) gcLoop() {
	// The album table starts as whatever the views have seen; registering
	// the whole source tree builds the same album set a ride through the
	// tree would produce, in the background.
	go o.gcBootstrap()
	ticker := time.NewTicker(gcTick)
	defer ticker.Stop()
	for range ticker.C {
		o.invalidatePass()
		o.ttlPass()
		o.budgetPass()
		o.gc.dropAccessWorst()
	}
}

// gcBootstrap walks the source tree once and classifies every directory
// holding a cue sheet, which registers all current albums in the source
// mtime table (reuses buildView, the path lookups use).
func (o *Root) gcBootstrap() {
	// WalkDir does not follow a symlinked root (the same trap buildPlan
	// handles); resolve once so the registration actually reaches the
	// library.
	root := filepath.Clean(o.opts.Src)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		ents, rerr := os.ReadDir(path)
		if rerr != nil {
			return nil
		}
		hasCue := false
		for _, e := range ents {
			if !e.IsDir() && library.IsCueFile(e.Name()) {
				hasCue = true
				break
			}
		}
		if !hasCue {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if _, verr := o.viewFor(filepath.ToSlash(rel)); verr != nil {
			return nil
		}
		return nil
	})
	o.gc.mu.Lock()
	n := len(o.gc.albums)
	o.gc.mu.Unlock()
	o.opts.Log("gc     : album table registered (%d album(s) tracked)", n)
}

// albumSourceMtime stats an album's cue and source files; exists=false
// marks either one gone, while a pair of fresh times means the album's
// newest state (max(cue, src), exactly what materialise stamps).
func albumSourceMtime(cuePath, srcPath string) (mtime time.Time, exists bool) {
	var cueTime, srcTime time.Time
	if info, err := os.Stat(cuePath); err != nil {
		return time.Time{}, false
	} else {
		cueTime = info.ModTime()
	}
	if info, err := os.Stat(srcPath); err != nil {
		return time.Time{}, false
	} else {
		srcTime = info.ModTime()
	}
	if srcTime.After(cueTime) {
		return srcTime, true
	}
	return cueTime, true
}

// invalidatePass checks the album table for changed and vanished sources:
// the album's cache dir is deleted as one unit, so no half-regenerated
// state can be served; deletion waits out the access grace (方案 3.1/3.2).
// On a defer decision the recorded mtime does NOT move: the album is
// retried on the next tick exactly as the plan says.
func (o *Root) invalidatePass() {
	now := time.Now()
	for cuePath, rec := range o.gc.list() {
		mtime, exists := albumSourceMtime(rec.album.CuePath, rec.album.SourcePath)
		if !exists {
			o.opts.Log("gc     : source or cue gone: %s - pruning cache %s", cuePath, rec.album.OutDir)
			if err := os.RemoveAll(rec.album.OutDir); err != nil {
				o.opts.Log("gc     : prune %s: %v", rec.album.OutDir, err)
				continue
			}
			o.gc.dropAlbum(cuePath, rec.album.OutDir)
			continue
		}
		if mtime.Equal(rec.mtime) || mtime.Before(rec.mtime) {
			continue
		}
		// The source is newer than what the cache was built from.
		if !o.gc.albumIdle(rec.album, now) {
			o.opts.Log("gc     : %s changed but a track was accessed inside %s - deferred",
				cuePath, gcAccessGrace)
			continue
		}
		o.opts.Log("gc     : %s changed (mtime %s) - pruning cache %s (regenerated on demand)", cuePath, mtime, rec.album.OutDir)
		if err := os.RemoveAll(rec.album.OutDir); err != nil {
			o.opts.Log("gc     : prune %s: %v", rec.album.OutDir, err)
			continue
		}
		o.gc.recordInvalidated(rec, mtime)
	}
}

// list snapshots the album table (gcstate.go owns the lock).
func (g *gcState) list() map[string]*gcAlbum {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]*gcAlbum, len(g.albums))
	for k, v := range g.albums {
		out[k] = v
	}
	return out
}

// recordInvalidated updates the album's mtime stamp once its cache went
// away: any re-materialised cache then belongs to the next mtime and
// invalidations keep being noticed.
func (g *gcState) recordInvalidated(rec *gcAlbum, mtime time.Time) {
	rec.mtime = mtime
}

// ttlPass removes albums nobody touched for cache_ttl, again by whole album
// dirs. Albums without cached tracks are left alone (nothing to expire),
// and tracks accessed inside the grace block the deletion.
func (o *Root) ttlPass() {
	ttl := o.opts.CacheTTL
	if ttl <= 0 {
		return
	}
	now := time.Now()
	for cuePath, rec := range o.gc.list() {
		if _, err := os.Stat(rec.album.OutDir); err != nil {
			continue
		}
		access := o.gc.albumLastAccess(rec.album, now)
		if access.IsZero() || now.Sub(access) <= ttl {
			continue
		}
		if !o.gc.albumIdle(rec.album, now) {
			continue
		}
		o.opts.Log("gc ttl : %s: untouched for %s - pruning cache %s",
			cuePath, now.Sub(access).Round(time.Second), rec.album.OutDir)
		if err := os.RemoveAll(rec.album.OutDir); err != nil {
			o.opts.Log("gc ttl : prune %s: %v", rec.album.OutDir, err)
			continue
		}
		o.gc.dropAlbum(cuePath, rec.album.OutDir)
	}
}

// budgetPass brings the cache tree under cache_max_gb by evicting whole
// albums, least-recently-accessed first. Nothing happens while it fits.
func (o *Root) budgetPass() {
	maxGB := o.opts.CacheMaxGB
	if maxGB <= 0 {
		return
	}
	limit := uint64(maxGB) << 30
	total := o.cacheDirSize()
	if total <= limit {
		return
	}

	type evictable struct {
		cue    string
		path   string
		bytes  uint64
		access time.Time
	}
	var cands []evictable
	for cuePath, rec := range o.gc.list() {
		if _, err := os.Stat(rec.album.OutDir); err != nil {
			continue
		}
		cands = append(cands, evictable{
			cue:    cuePath,
			path:   rec.album.OutDir,
			bytes:  dirBytes(rec.album.OutDir),
			access: o.gc.albumLastAccess(rec.album, time.Now()),
		})
	}
	sort.Slice(cands, func(i, j int) bool {
		return albumAccessLess(cands[i].access, cands[j].access)
	})
	if len(cands) == 0 {
		o.opts.Log("gc size: over budget (cache %.1f MiiB > %d GiB) but no albums are trackable", float64(total)/(1<<20), maxGB)
	}
	var freed uint64
	var evicted int
	for _, c := range cands {
		if total <= limit {
			break
		}
		if err := os.RemoveAll(c.path); err != nil {
			o.opts.Log("gc size: prune %s: %v", c.path, err)
			continue
		}
		o.gc.dropAlbum(c.cue, c.path)
		total -= c.bytes
		freed += c.bytes
		evicted++
		o.opts.Log("gc size: evicted %s (%.1f MiiB, last touched %s)", c.path,
			float64(c.bytes)/(1<<20), c.access.Round(time.Second))
	}
	o.opts.Log("gc size: evicted %d album(s), freed %.1f MiiB; cache now %.1f MiiB of budget %d GiB",
		evicted, float64(freed)/(1<<20), float64(total)/(1<<20), maxGB)
}

// albumAccessLess ranks older access first; albums never touched go first
// (zero time sorts before everything).
func albumAccessLess(a, b time.Time) bool {
	if a.IsZero() != b.IsZero() {
		return a.IsZero()
	}
	return a.Before(b)
}

// dirBytes sums one album dir's file sizes (metadata-only walk).
func dirBytes(dir string) uint64 {
	var total uint64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += uint64(info.Size())
		}
		return nil
	})
	return total
}

// cacheDirSize sums the whole cache tree (split's shared walk).
func (o *Root) cacheDirSize() uint64 {
	_, bytes, err := split.CacheSize(o.opts.Cache)
	if err != nil {
		// Unreadable pieces read short; the next tick rechecks anyway.
		o.opts.Log("gc size: walk %s: %v", o.opts.Cache, err)
	}
	return bytes
}
