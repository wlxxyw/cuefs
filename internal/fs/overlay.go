//go:build linux

// Package fs implements the read-only FUSE overlay (DESIGN §4.7 / M2): every
// directory of the source library is presented as-is, except that album files
// with a same-basename cue sheet are replaced by their tracks.
//
// Track bytes come from the cache tree (the layout `cuefs split` produces); a
// missing entry is materialised on demand and atomically written into that
// tree, which makes the mount point self-hydrating.
package fs

import (
	"context"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"cuefs/internal/audio"
	"cuefs/internal/cue"
	"cuefs/internal/library"
	"cuefs/internal/plan"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Options configures the overlay.
type Options struct {
	// Src is the read-only music library root; Cache is the tree of
	// generated tracks (the layout `cuefs split` produces).
	Src   string
	Cache string

	// Detection and naming, straight from the configuration.
	Detect          cue.DetectOptions
	SplitExtensions []string
	ApeExt          string // extension of APE-converted tracks (others follow the source)
	NameTemplate    string
	MaxTitleLen     int
	IgnoreTags      []string

	// Synthesis knobs. FfmpegBin may be empty, in which case FLAC and APE
	// tracks can only be served from the cache.
	FfmpegBin        string
	FfprobeBin       string
	CompressionLevel int
	Workers          int

	Log func(format string, args ...any)
}

func (o *Options) defaults() {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if len(o.SplitExtensions) == 0 {
		o.SplitExtensions = []string{".flac", ".wav", ".ape"}
	}
	if o.ApeExt = plan.NormaliseExtension(o.ApeExt); o.ApeExt == "" {
		o.ApeExt = ".flac"
	}
	if o.NameTemplate == "" {
		o.NameTemplate = "{num:02d} - {title}{ext}"
	}
	if o.MaxTitleLen <= 0 {
		o.MaxTitleLen = 60
	}
	if o.CompressionLevel <= 0 {
		o.CompressionLevel = 5
	}
	if o.Workers < 1 {
		o.Workers = 1
	}
}

// dirView describes one overlay directory: the names that must be hidden
// (cue sheets and the album sources the tracks replace) and the names of the
// virtual tracks.
type dirView struct {
	relDir string
	srcDir string

	albums []*plan.Album
	hide   map[string]bool
	tracks map[string]*trackSpec
}

// trackSpec is one virtual file: what it is and where its bytes live once
// materialised.
type trackSpec struct {
	album *plan.Album
	track *plan.Track

	srcPath   string
	cachePath string
}

// Root is the overlay's root node; it hangs a set of virtual track files and
// a set of hidden names on top of a loopback view of Src.
type Root struct {
	*fs.LoopbackRoot
	opts Options

	viewsMu sync.Mutex
	views   map[string]*pendingView

	synth  chan struct{} // bounds concurrent synthesis jobs
	onceMu sync.Mutex
	once   map[string]*sync.Once
}

type overlayNode struct {
	*fs.LoopbackNode
	root *Root
}

type pendingView struct {
	once sync.Once
	view *dirView
	err  error
}

// Mount attaches the overlay at mountpoint and starts serving. Unmount
// detaches again.
// Mount attaches the overlay at mountpoint and starts serving. Unmount
// detaches again.
//
// fuseExtra carries raw fusermount3 options; allowOther is the most common
// one and is spelled out for readability.
func Mount(mountpoint string, o Options, fuseExtra []string, allowOther bool) (*fuse.Server, error) {
	o.defaults()
	rootData := &fs.LoopbackRoot{Path: filepath.Clean(o.Src)}
	root := &Root{
		LoopbackRoot: rootData,
		opts:         o,
		views:        map[string]*pendingView{},
		synth:        make(chan struct{}, o.Workers),
		once:         map[string]*sync.Once{},
	}
	rootData.NewNode = func(_ *fs.LoopbackRoot, _ *fs.Inode, _ string, _ *syscall.Stat_t) fs.InodeEmbedder {
		// Virtual track names never reach the loopback lookup, because
		// overlayNode.Lookup intercepts them; whatever this hook produces is
		// a passthrough node.
		return &overlayNode{
			LoopbackNode: &fs.LoopbackNode{RootData: rootData},
			root:         root,
		}
	}
	rootData.RootNode = &overlayNode{
		LoopbackNode: &fs.LoopbackNode{RootData: rootData},
		root:         root,
	}

	sec := 5 * time.Second
	o.Log("mount   : ape→%q, template %q, cache %q, src %q", o.ApeExt, o.NameTemplate, o.Cache, o.Src)
	return fs.Mount(mountpoint, rootData.RootNode, &fs.Options{
		EntryTimeout: &sec,
		AttrTimeout:  &sec,
		MountOptions: fuse.MountOptions{
			// Read-only, with the permission checks done by the kernel from
			// the mode bits the nodes report.
			Options: []string{"ro", "default_permissions", "fsname=cuefs"},
			Name:    "cuefs",
		},
	})
}

// rootInode is the overlay's root inode; the reference point of Inode.Path().
func (o *Root) rootInode() *fs.Inode { return o.RootNode.EmbeddedInode() }

// viewFor returns (and memoizes) the description of one overlay directory.
func (o *Root) viewFor(relDir string) (*dirView, error) {
	relDir = normaliseRel(relDir)
	o.viewsMu.Lock()
	p, ok := o.views[relDir]
	if !ok {
		p = &pendingView{}
		o.views[relDir] = p
	}
	o.viewsMu.Unlock()
	p.once.Do(func() {
		p.view, p.err = o.buildView(relDir)
		if p.err != nil {
			// An unreadable directory keeps the passthrough view: that is the
			// safe failure mode, and it keeps the tree reachable.
			p.view = &dirView{
				relDir: relDir,
				srcDir: filepath.Join(o.opts.Src, relDir),
				hide:   map[string]bool{},
				tracks: map[string]*trackSpec{},
			}
		}
	})
	return p.view, p.err
}

func normaliseRel(rel string) string {
	rel = filepath.ToSlash(rel)
	switch rel {
	case ".", "":
		return ""
	}
	return strings.TrimPrefix(rel, "./")
}

// buildView classifies one src directory.
func (o *Root) buildView(relDir string) (*dirView, error) {
	srcDir := filepath.Join(o.opts.Src, relDir)
	ents, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, err
	}
	v := &dirView{
		relDir: relDir,
		srcDir: srcDir,
		hide:   map[string]bool{},
		tracks: map[string]*trackSpec{},
	}

	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !library.IsCueFile(name) {
			continue
		}
		v.hide[name] = true // cue sheets never enter the virtual tree (D3)
		cuePath := filepath.Join(srcDir, name)
		entry := library.Classify(cuePath, o.opts.SplitExtensions, o.opts.Detect)
		if entry.Kind != library.KindSplit {
			for _, w := range entry.Warnings {
				o.opts.Log("overlay : %s: %s", cuePath, w)
			}
			continue
		}
		f, err := audio.ProbeAuto(entry.SourcePath, o.opts.FfprobeBin)
		if err != nil {
			// Without timing facts there is nothing to split; the album file
			// stays in place so nothing is lost.
			o.opts.Log("overlay : %s: %v", entry.SourcePath, err)
			continue
		}
		album, err := plan.Build(&entry, f, plan.Options{
			SourceRoot: o.opts.Src, OutputRoot: o.opts.Cache,
			ApeExt: o.opts.ApeExt, NameTemplate: o.opts.NameTemplate,
			MaxTitleLen: o.opts.MaxTitleLen, IgnoreTags: o.opts.IgnoreTags,
		})
		if err != nil {
			o.opts.Log("overlay : %s: %v", cuePath, err)
			continue
		}
		v.albums = append(v.albums, album)
		v.hide[filepath.Base(album.SourcePath)] = true
		for _, t := range album.Tracks {
			if _, dup := v.tracks[t.Name]; dup {
				o.opts.Log("overlay : duplicate track name in %s: %s", srcDir, t.Name)
				continue
			}
			v.tracks[t.Name] = &trackSpec{
				album: album, track: t,
				srcPath:   entry.SourcePath,
				cachePath: filepath.Join(album.OutDir, t.Name),
			}
		}
	}
	return v, nil
}

// Lookup resolves a child; virtual tracks and hidden names are intercepted
// before the loopback lookup sees them.
func (n *overlayNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	v, _ := n.root.viewFor(n.relPath())
	if v == nil {
		return nil, syscall.ENOENT
	}
	if spec, ok := v.tracks[name]; ok {
		return n.makeTrack(ctx, spec, out)
	}
	if v.hide[name] {
		return nil, syscall.ENOENT
	}
	return n.LoopbackNode.Lookup(ctx, name, out)
}

// The overlay is read-only; reject the whole write family up front instead
// of letting the loopback back-end attempt anything.
func (n *overlayNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EROFS
}

func (n *overlayNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	return nil, nil, 0, syscall.EROFS
}

func (n *overlayNode) Mknod(ctx context.Context, name string, mode, rdev uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EROFS
}

func (n *overlayNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return syscall.EROFS
}

func (n *overlayNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return syscall.EROFS
}

func (n *overlayNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	return syscall.EROFS
}

func (n *overlayNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EROFS
}

// OpendirHandle takes precedence over Readdir in the bridge, and the passthrough
// directory stream would bypass the merge: build the merged stream here.
func (n *overlayNode) OpendirHandle(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	ds, errno := n.Readdir(ctx)
	if errno != 0 {
		return nil, 0, errno
	}
	return &mergedDirHandle{ds: ds}, 0, 0
}

// mergedDirHandle exposes a DirStream as a directory FileHandle, mirroring
// what the bridge does for nodes that only implement Readdir.
type mergedDirHandle struct{ ds fs.DirStream }

func (d *mergedDirHandle) Readdirent(ctx context.Context) (*fuse.DirEntry, syscall.Errno) {
	if !d.ds.HasNext() {
		return nil, 0
	}
	e, errno := d.ds.Next()
	if errno != 0 {
		return nil, errno
	}
	return &e, 0
}

func (d *mergedDirHandle) Releasedir(_ context.Context, _ uint32) { d.ds.Close() }

// Readdir merges the source directory's entries with the overlay's own view.
func (n *overlayNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	v, _ := n.root.viewFor(n.relPath())
	list, errno := n.LoopbackNode.Readdir(ctx)
	if errno != 0 {
		return nil, errno
	}
	var out []fuse.DirEntry
	for list.HasNext() {
		e, errno := list.Next()
		if errno != 0 {
			list.Close()
			return nil, errno
		}
		if v != nil && v.hide[e.Name] {
			continue
		}
		out = append(out, e)
	}
	list.Close()

	if v != nil {
		for _, spec := range v.tracks {
			out = append(out, fuse.DirEntry{Name: spec.track.Name, Mode: syscall.S_IFREG})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return fs.NewListDirStream(out), 0
}

// relPath is the node's path inside the overlay, identical to the path it
// has in the cache tree.
func (n *overlayNode) relPath() string {
	return filepath.ToSlash(n.EmbeddedInode().Path(n.root.rootInode()))
}

// makeTrack creates the inode of one virtual track and fills the lookup's
// attribute data, which the kernel caches together with the entry.
func (n *overlayNode) makeTrack(ctx context.Context, spec *trackSpec, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	node := &trackNode{root: n.root, spec: *spec}
	if errno := node.ensure(); errno != 0 {
		return nil, errno
	}
	st, err := statPath(spec.cachePath)
	if err != nil {
		return nil, fs.ToErrno(err)
	}
	out.Attr.FromStat(st)
	return n.NewInode(ctx, node, fs.StableAttr{
		Mode: syscall.S_IFREG,
		Ino:  stableIno(spec.cachePath),
	}), 0
}

// stableIno derives a stable inode number for a virtual track, so repeated
// lookups reuse the same kernel-side object.
func stableIno(path string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	v := h.Sum64()
	if v < 2 {
		v = 2
	}
	return v & (1<<62 - 1)
}
