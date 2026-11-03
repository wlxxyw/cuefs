//go:build linux

package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"cuefs/internal/engine"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// trackNode is one virtual file; its bytes live in the cache tree.
type trackNode struct {
	fs.Inode
	root *Root
	spec trackSpec

	mu sync.Mutex
}

// Materialize makes the cache entry present and returns the file's path.
// Idempotent, concurrency-safe, and the failure path allows a retry.
func (o *Root) Materialize(spec trackSpec) (string, error) {
	if info, err := os.Stat(spec.cachePath); err == nil &&
		info.Mode().IsRegular() && info.Size() > 0 {
		return spec.cachePath, nil
	}

	o.onceMu.Lock()
	once, ok := o.once[spec.cachePath]
	if !ok {
		once = &sync.Once{}
		o.once[spec.cachePath] = once
	}
	o.onceMu.Unlock()

	once.Do(func() {
		o.synth <- struct{}{}
		defer func() { <-o.synth }()
		o.doMaterialize(spec)
	})

	info, err := os.Stat(spec.cachePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		// Forget this attempt so a later lookup can try again: a transient
		// disk-full error must not poison the track for the mount's lifetime.
		o.forgetOnce(spec.cachePath)
		return "", fmt.Errorf("track not materialised at %s", spec.cachePath)
	}
	return spec.cachePath, nil
}

func (o *Root) forgetOnce(key string) {
	o.onceMu.Lock()
	delete(o.once, key)
	o.onceMu.Unlock()
}

// doMaterialize synthesizes one track into the cache and stamps it with the
// deterministic max(src, cue) mtime (DESIGN §4.4), so scanners see a stable
// fingerprint across rescans.
func (o *Root) doMaterialize(spec trackSpec) {
	if err := os.RemoveAll(spec.cachePath + ".part"); err != nil {
		o.opts.Log("overlay : %s: %v", spec.cachePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(spec.cachePath), 0o755); err != nil {
		o.opts.Log("overlay : mkdir %s: %v", filepath.Dir(spec.cachePath), err)
		return
	}
	if err := engine.WriteTrack(spec.srcPath, spec.album.SampleRate, spec.track,
		spec.cachePath, engine.Options{
			FfmpegBin:        o.opts.FfmpegBin,
			CompressionLevel: o.opts.CompressionLevel,
			Extension:        o.opts.Extension,
		}); err != nil {
		o.opts.Log("overlay : FAIL %s: %v", spec.cachePath, err)
		return
	}
	mod := trackMtime(spec.album.CuePath, spec.album.SourcePath)
	if err := os.Chtimes(spec.cachePath, mod, mod); err != nil {
		o.opts.Log("overlay : chtimes %s: %v", spec.cachePath, err)
	}
	if err := os.Chmod(spec.cachePath, 0o644); err != nil {
		o.opts.Log("overlay : chmod %s: %v", spec.cachePath, err)
	}
	o.opts.Log("write   : %s", spec.cachePath)
}

// trackMtime is the deterministic fingerprint timestamp of a track.
func trackMtime(cuePath, srcPath string) (m time.Time) {
	m = maxMtime(srcPath)
	if t := maxMtime(cuePath); t.After(m) {
		m = t
	}
	return m
}

func maxMtime(path string) time.Time {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// ensure makes sure the cache entry exists; it never caches a failure, so a
// transient disk error can be retried with the next lookup.
func (n *trackNode) ensure() syscall.Errno {
	if info, err := os.Stat(n.spec.cachePath); err == nil &&
		info.Mode().IsRegular() && info.Size() > 0 {
		return 0
	}
	if _, err := n.root.Materialize(n.spec); err != nil {
		n.root.opts.Log("overlay : %s: %v", n.spec.cachePath, err)
		return syscall.EIO
	}
	return 0
}

// Attr reports the materialised file's attributes. Exactness here is what
// keeps the reported size stable for the mount's whole lifetime (DESIGN
// §4.4), which is why the synthesis happens during Lookup/Getattr, not Read.
func (n *trackNode) Attr(_ context.Context, out *fuse.AttrOut) syscall.Errno {
	st, err := statPath(n.spec.cachePath)
	if err != nil {
		return fs.ToErrno(err)
	}
	fill(out, st)
	return 0
}

// Getattr keeps attribute queries that arrive before an Open working.
func (n *trackNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	return n.Attr(context.Background(), out)
}

// Open serves a handle onto the cache file; the overlay does not accept
// writes anywhere.
func (n *trackNode) Open(_ context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	const writeFlags = syscall.O_WRONLY | syscall.O_RDWR | syscall.O_APPEND |
		syscall.O_TRUNC | syscall.O_CREAT | syscall.O_EXCL
	if flags&writeFlags != 0 {
		return nil, 0, syscall.EROFS
	}
	if _, err := n.root.Materialize(n.spec); err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	st, err := statPath(n.spec.cachePath)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	_ = st
	f, err := os.Open(n.spec.cachePath)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	return fs.NewLoopbackFileFromOS(f), fuse.FOPEN_KEEP_CACHE, 0
}

func statPath(path string) (*syscall.Stat_t, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// fill copies a stat result into the FUSE attribute structure, the same way
// the loopback back-end does.
func fill(out *fuse.AttrOut, st *syscall.Stat_t) {
	out.FromStat(st)
}
