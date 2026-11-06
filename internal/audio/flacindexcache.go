package audio

import (
	"os"
	"sync"
	"time"
)

// IndexResolver supplies the frame index of a source file. IndexFlacFrames
// is the single-shot resolver; IndexCache is the shared one.
type IndexResolver interface {
	Index(path string) (*FlacIndex, error)
}

// defaultIndexCacheCap bounds how many source indexes one cache keeps. A
// run processes albums one after another, so a small cap holds enough: each
// entry is ~frame count × sizeof(FlacFrame), about 0.6 MB for a 30,850-frame
// album.
const defaultIndexCacheCap = 32

// IndexCache shares one FlacIndex per source file across a run's callers.
// Without it a split of a 300 MB album re-walks the whole file once per
// track - the same bytes out of the page cache, N times (measured: a cold
// 306 MB walk costs ~38 s of disk time, so a 10-track album pays 10× that
// before any encoding starts; with the cache it pays it once, and the
// workers cutting concurrently wait on ONE walk instead of racing N of
// them).
//
// Freshness: an entry stores the size and mtime taken at walk time and a
// lookup re-stats the path, serving the index only while both still match,
// so a file rewritten mid-run (retag, a source refresh) is re-walked rather
// than served stale. Walk results that carried a partial chain and an error
// are NOT stored: callers rely on re-walking to observe a later repair, and
// such files are rare enough that the repeat walk is noise.
type IndexCache struct {
	mu      sync.Mutex
	cap     int
	entries map[string]*indexEntry
	walking map[string]*indexWalk
}

// indexEntry is one cached walk plus the identity of the bytes it describes.
type indexEntry struct {
	idx     *FlacIndex
	size    int64
	modTime time.Time
	used    time.Time
}

// indexWalk is the in-flight walk one goroutine leads while the rest wait
// for its shared result.
type indexWalk struct {
	done chan struct{}
	res  walkResult
}

type walkResult struct {
	idx *FlacIndex
	err error
}

// NewIndexCache builds a cache holding up to capacity entries (<= 0 keeps
// the default).
func NewIndexCache(capacity int) *IndexCache {
	if capacity <= 0 {
		capacity = defaultIndexCacheCap
	}
	return &IndexCache{
		cap:     capacity,
		entries: map[string]*indexEntry{},
		walking: map[string]*indexWalk{},
	}
}

// indexWalkFile is the walk a cache performs; a package var so tests can
// instrument it.
var indexWalkFile = IndexFlacFrames

// Index returns the path's index, walking the file once and sharing that
// result with every concurrent and later Index call while the file's
// size+mtime agree. The (index, error) contract is exactly
// IndexFlacFrames'.
func (c *IndexCache) Index(path string) (*FlacIndex, error) {
	c.mu.Lock()
	if e, ok := c.entries[path]; ok {
		e.used = time.Now()
		// The stat rides under the lock: it costs microseconds (dentry
		// cache), while a mis-timed delete/re-walk round costs a whole
		// 38 s walk on a big album.
		if st, err := os.Stat(path); err == nil &&
			st.Size() == e.size && st.ModTime().Equal(e.modTime) {
			idx := e.idx
			c.mu.Unlock()
			return idx, nil
		}
		delete(c.entries, path)
	}

	// Join an in-flight walk, or become its leader.
	if w, ok := c.walking[path]; ok {
		c.mu.Unlock()
		<-w.done
		return w.res.idx, w.res.err
	}
	w := &indexWalk{done: make(chan struct{})}
	c.walking[path] = w
	c.mu.Unlock()

	res := walkResult{}
	st, err := os.Stat(path)
	if err != nil {
		res.err = err
	} else {
		res.idx, res.err = indexWalkFile(path)
	}

	c.mu.Lock()
	delete(c.walking, path)
	if res.err == nil {
		c.entries[path] = &indexEntry{
			idx:     res.idx,
			size:    st.Size(),
			modTime: st.ModTime(),
			used:    time.Now(),
		}
		for len(c.entries) > c.cap {
			var oldKey string
			var oldUsed time.Time
			for k, e := range c.entries {
				if oldKey == "" || e.used.Before(oldUsed) {
					oldKey, oldUsed = k, e.used
				}
			}
			delete(c.entries, oldKey)
		}
	}
	// Publish BEFORE closing: waiters see an immutable result after the
	// close's happens-before edge.
	w.res = res
	close(w.done)
	c.mu.Unlock()
	return res.idx, res.err
}

// Len reports how many indexes the cache currently holds (tests).
func (c *IndexCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
