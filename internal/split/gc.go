package split

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// gcStalePart bounds how long a .part leftover must be idle before gc
// deletes it: a concurrent run's live parts are younger than any album
// split takes, so this only ever catches crash debris.
const gcStalePart = 24 * time.Hour

// GcStats accounts for one prune pass over the cache tree.
type GcStats struct {
	// Orphans: files (and empty directories) whose sources left the plan.
	OrphanFiles int
	OrphanDirs  int
	OrphanBytes uint64
	// Crash debris: .part leftovers older than gcStalePart. Live parts of
	// a running write are younger and stay.
	PartFiles int
	PartBytes uint64
	// The cache tree as it stands after (or, in dry mode, before) the pass.
	CacheFiles int
	CacheBytes uint64
	// The size budget as parsed from the config, and whether the tree
	// exceeds it right now. Report only: eviction itself needs fresh
	// access times and is the mount's periodic task.
	BudgetGB   int
	OverBudget bool
	// The TTL as parsed from the config (report only).
	TTL time.Duration
}

// relative returns the slash-normalised path of path under root, or "" when
// path cannot be expressed relative to root.
func relative(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// joinRel joins two slash-relative parts.
func joinRel(dir, name string) string {
	if dir == "." || dir == "" {
		return filepath.ToSlash(name)
	}
	return dir + "/" + filepath.ToSlash(name)
}

// expectedFiles computes every file the current plan is entitled to keep in
// the cache tree: generated tracks and the per-album cover. Keys are
// slash-relative to the cache root.
func expectedFiles(o Options, p *Plan) map[string]bool {
	expected := map[string]bool{}
	for _, a := range p.Albums {
		rel := relative(o.OutRoot, a.OutDir)
		if rel == "" {
			continue
		}
		for _, t := range a.Tracks {
			expected[joinRel(rel, t.Name)] = true
		}
		if a.Extractor {
			expected[joinRel(rel, "cover.jpg")] = true
		}
	}
	return expected
}

// mirrorWalk adds the expected passthrough mirrors: it replays mirror()'s
// rules over the source tree, which is what a split run writes beside the
// tracks. Replaying the rules keeps the prune's expected set byte-compatible
// with those writes.
//
// It deliberately ignores Options.SkipPassthrough: a track-only run must not
// turn previously written passthrough files into prunable orphans.
func mirrorWalk(o Options, p *Plan, expected map[string]bool) error {
	fsys := os.DirFS(o.SrcRoot)
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // keep the pass readable
		}
		if d.IsDir() {
			if !p.AllDirs && !p.SelDirs[path] {
				return fs.SkipDir
			}
			return nil
		}
		abs := filepath.Join(o.SrcRoot, path)
		if p.Hidden[abs] {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".cue") && !p.PassthroughCues[abs] {
			return nil
		}
		if p.Sampled[abs] {
			return nil
		}
		expected[filepath.ToSlash(path)] = true
		return nil
	})
}

// CacheSize walks the cache tree metadata-only and returns its totals.
func CacheSize(root string) (files int, bytes uint64, err error) {
	err = filepath.WalkDir(root, func(_ string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // unreadable entries do not abort the report
		}
		if d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			files++
			bytes += uint64(info.Size())
		}
		return nil
	})
	return files, bytes, err
}

// Prune deletes every cache-tree file the current plan does not claim (the
// layout mirror() would not recreate) plus crashed-run .part leftovers, then
// removes directories left empty by those deletions. It never touches the
// TTL or the size budget: those need fresh access times, which only the
// mount's own bookkeeping has - Prune instead reports the tree's size and
// the budget so callers can decide.
//
// o.Only is forced empty: an --only run must never prune the rest of the
// tree. o.Dry turns every deletion into a report line and leaves the tree
// intact.
func Prune(o Options) (*GcStats, error) {
	o.defaults()
	o.Only = ""
	st := &GcStats{}
	st.BudgetGB = o.CacheMaxGB
	st.TTL = o.CacheTTL

	p, err := BuildPlan(o)
	if err != nil {
		return st, err
	}
	return prunePass(o, p, st)
}

// prunePass performs one sweep against a caller-provided plan - the
// BuildPlan-free shape the unit tests (and only they) use.
func prunePass(o Options, p *Plan, st *GcStats) (*GcStats, error) {
	if o.Log == nil { // unit tests drive this with a bare Options
		o.Log = func(string, ...any) {}
	}
	expected := expectedFiles(o, p)
	if err := mirrorWalk(o, p, expected); err != nil {
		return st, err
	}

	out := o.OutRoot
	var dirs []string
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // keep going; report covers what it could read
		}
		if path == out {
			return nil
		}
		rel := relative(out, path)
		if rel == "" {
			return nil
		}
		if d.IsDir() {
			// Deletions may leave directories empty; remember the candidates
			// (parents before children) and sweep from the deepest one down.
			dirs = append(dirs, path)
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".part") {
			if info, ierr := d.Info(); ierr == nil && time.Since(info.ModTime()) > gcStalePart {
				st.PartFiles++
				st.PartBytes += uint64(info.Size())
				if !o.Dry {
					o.Log("gc     : stale part %s (%d bytes)", rel, info.Size())
					return os.Remove(path)
				}
				o.Log("gc-dry : stale part %s (%d bytes)", rel, info.Size())
			}
			return nil // live part of a running write
		}
		if expected[rel] {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || !info.Mode().IsRegular() {
			return nil
		}
		st.OrphanFiles++
		st.OrphanBytes += uint64(info.Size())
		if o.Dry {
			o.Log("gc-dry : orphan %s (%d bytes)", rel, info.Size())
			return nil
		}
		o.Log("gc     : orphan %s (%d bytes)", rel, info.Size())
		st.OrphanBytes += uint64(info.Size())
		return os.Remove(path)
	})
	if err != nil {
		return st, err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		ents, derr := os.ReadDir(dir)
		if derr != nil || len(ents) > 0 || dir == out {
			continue
		}
		if o.Dry {
			o.Log("gc-dry : empty dir %s", relative(out, dir))
			continue
		}
		if rerr := os.Remove(dir); rerr == nil {
			st.OrphanDirs++
			o.Log("gc     : empty dir %s", relative(out, dir))
		}
	}

	st.CacheFiles, st.CacheBytes, _ = CacheSize(out)
	if o.CacheMaxGB > 0 && int64(st.CacheBytes) > int64(o.CacheMaxGB)<<30 {
		st.OverBudget = true
	}
	return st, err
}

// Report renders the prune pass plus the budget state for the CLI.
func (s *GcStats) Report() {
	if s.OrphanFiles > 0 || s.PartFiles > 0 {
		fmt.Printf("gc prune: removed %d orphan file(s) (%.1f MiiB), %d empty dir(s), %d stale .part (%.1f MiiB)\n",
			s.OrphanFiles, float64(s.OrphanBytes)/(1<<20), s.OrphanDirs,
			s.PartFiles, float64(s.PartBytes)/(1<<20))
	}
	fmt.Printf("gc cache: %d file(s), %.1f MiiB\n",
		s.CacheFiles, float64(s.CacheBytes)/(1<<20))
	if s.BudgetGB > 0 {
		if s.OverBudget {
			fmt.Printf("gc budget: OVER budget %d GiB - the mount's periodic task evicts least-recently-accessed album(s)\n",
				s.BudgetGB)
		} else {
			fmt.Printf("gc budget: %.1f MiiB spare under %d GiB\n",
				float64(int64(s.BudgetGB)<<30-int64(s.CacheBytes))/(1<<20), s.BudgetGB)
		}
	} else {
		fmt.Printf("gc budget: none configured (cache_max_gb)\n")
	}
	ttl := "none"
	if s.TTL > 0 {
		ttl = s.TTL.String()
	}
	fmt.Printf("gc ttl   : %s (enforced by the mount's periodic task; gc cannot see access times)\n", ttl)
}
