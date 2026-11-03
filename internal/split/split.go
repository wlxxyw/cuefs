// Package split materialises a virtual track library onto disk and verifies
// what was produced. It never writes to the source tree (DESIGN D15).
package split

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cuefs/internal/audio"
	"cuefs/internal/cue"
	"cuefs/internal/engine"
	"cuefs/internal/library"
	"cuefs/internal/plan"
)

// Options configures one materialise or verify run.
type Options struct {
	SrcRoot, OutRoot string

	// Source classification
	SplitExtensions []string
	EncodingList    []string
	ForceEncoding   string

	// Naming / tags
	Extension    string // extension of the generated tracks
	NameTemplate string
	MaxTitleLen  int
	IgnoreTags   []string

	// Engine
	FfmpegBin        string
	FfprobeBin       string
	CompressionLevel int

	// Behaviour
	Workers int
	Force   bool   // rebuild even when the output file already exists
	Link    bool   // hardlink passthrough files instead of copying
	Only    string // act only on albums whose name contains this
	Dry     bool   // plan and report without writing
	Log     func(format string, args ...any)
}

func (o *Options) defaults() {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Workers < 1 {
		o.Workers = 1
	}
	if len(o.SplitExtensions) == 0 {
		o.SplitExtensions = []string{".flac", ".wav", ".ape"}
	}
	if o.Extension == "" {
		o.Extension = ".flac"
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
	if o.FfmpegBin == "" {
		o.FfmpegBin = "ffmpeg"
	}
	if o.FfprobeBin == "" {
		o.FfprobeBin = "ffprobe"
	}
}

func (o Options) detect() cue.DetectOptions {
	return cue.DetectOptions{Force: o.ForceEncoding, List: o.EncodingList}
}

// Stats summarises a materialise run.
type Stats struct {
	Albums        int
	Tracks        int
	TracksWritten int
	TracksSkipped int
	TracksFailed  int
	Covers        int
	Others        int
	TrackBytes    uint64
	OtherBytes    uint64
	Warnings      int
	Errors        int
}

// Report prints a human summary.
func (s *Stats) Report(w io.Writer) {
	fmt.Fprintf(w, "%d album(s), %d track(s): written %d, skipped %d, failed %d, cover(s) %d; passthrough %d file(s)\n",
		s.Albums, s.Tracks, s.TracksWritten, s.TracksSkipped, s.TracksFailed,
		s.Covers, s.Others)
	fmt.Fprintf(w, "generated %s, passthrough %s, %d warning(s), %d error(s)\n",
		humanBytes(s.TrackBytes), humanBytes(s.OtherBytes), s.Warnings, s.Errors)
}

// VerifyStats summarises a verify run.
type VerifyStats struct {
	Checked int
	Failed  int
	Missing int
}

// Report prints a human summary.
func (s *VerifyStats) Report(w io.Writer) {
	fmt.Fprintf(w, "checked %d track(s): OK %d, FAILED %d, MISSING %d\n",
		s.Checked+s.Failed+s.Missing, s.Checked, s.Failed, s.Missing)
}

// Plan is the output of the read-only pre-pass.
type Plan struct {
	Root    string
	Albums  []*plan.Album
	SelDirs map[string]bool // relative dirs kept during the walk
	AllDirs bool            // mirror the whole tree (no --only filter)
	Sampled map[string]bool // album sources that must not be copied
	Entries []*library.Entry
}

// BuildPlan classifies the whole source tree and reflects every split
// candidate into a concrete per-track plan. Strictly read-only.
func BuildPlan(o Options) (*Plan, error) {
	o.defaults()
	// WalkDir does not follow a symlinked root, so resolve it once: the rest
	// of the tool consistently works from the resolved path.
	if resolved, err := filepath.EvalSymlinks(o.SrcRoot); err == nil {
		o.SrcRoot = resolved
	} else if !filepath.IsAbs(o.SrcRoot) {
		// Leave it; WalkDir will surface the error itself.
	}
	only := strings.ToLower(o.Only)
	p := &Plan{
		Root:    o.SrcRoot,
		SelDirs: map[string]bool{".": true},
		AllDirs: only == "",
		Sampled: map[string]bool{},
	}
	entries := make([]*library.Entry, 0, 64)
	if err := library.Scan(o.SrcRoot, o.SplitExtensions, o.detect(),
		func(e library.Entry) {
			if only != "" {
				name := strings.ToLower(filepath.Base(e.CuePath))
				if !strings.Contains(name, only) {
					return
				}
			}
			entries = append(entries, &e)
		}); err != nil {
		return nil, err
	}
	p.Entries = entries

	for _, e := range entries {
		if !p.Sampled[e.SourcePath] {
			// Keep the cue's directory plus every ancestor selected, so
			// --only never accidentally cuts the tree in half.
			if rel, err := filepath.Rel(o.SrcRoot, filepath.Dir(e.CuePath)); err == nil {
				for cur := filepath.ToSlash(rel); ; cur = filepath.Dir(cur) {
					p.SelDirs[cur] = true
					if cur == "." || cur == "/" {
						break
					}
				}
			}
			// Reflect the source into a concrete plan.
			f, err := audio.Probe(o.FfprobeBin, e.SourcePath)
			if err != nil {
				e.Warnings = append(e.Warnings, err.Error())
				continue
			}
			album, err := plan.Build(e, f, plan.Options{
				SourceRoot: o.SrcRoot, OutputRoot: o.OutRoot,
				Extension: o.Extension, NameTemplate: o.NameTemplate,
				MaxTitleLen: o.MaxTitleLen, IgnoreTags: o.IgnoreTags,
			})
			if err != nil {
				e.Warnings = append(e.Warnings, err.Error())
				continue
			}
			album.Extractor = albumExtractorNeeded(album)
			p.Albums = append(p.Albums, album)
			p.Sampled[album.SourcePath] = true
		}
	}
	return p, nil
}

// albumExtractorNeeded is true when neither the source dir nor the target dir
// has any image and the source itself carries an embedded picture.
func albumExtractorNeeded(a *plan.Album) bool {
	if !a.HasVideo {
		return false
	}
	for _, dir := range []string{filepath.Dir(a.SourcePath), a.OutDir} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			switch strings.ToLower(filepath.Ext(e.Name())) {
			case ".jpg", ".jpeg", ".png", ".bmp", ".gif":
				return false
			}
		}
	}
	return true
}

// Materialize writes the planned library into OutRoot.
func Materialize(o Options) (*Stats, error) {
	o.defaults()
	st := &Stats{}
	p, err := BuildPlan(o)
	if err != nil {
		return st, err
	}
	o.SrcRoot = p.Root
	st.Albums = len(p.Albums)
	for _, a := range p.Albums {
		st.Tracks += len(a.Tracks)
	}
	o.Log("plan   : %d album(s), %d track(s)", st.Albums, st.Tracks)
	for _, a := range p.Albums {
		for _, w := range a.Warnings {
			o.Log("warning: %s: %s", a.CueBase(), w)
		}
	}

	// Tracks run as individual jobs, so that a large album is not a
	// serialisation point. Workers keep private counters and merge them once
	// at the end; the mutex only guards the merge itself.
	preloadCovers(o, p.Albums, st)

	type trackJob struct {
		album *plan.Album
		track *plan.Track
	}
	pool := make(chan trackJob)
	var wg sync.WaitGroup
	var mu sync.Mutex
	localStats := make([]Stats, o.Workers)
	for i := 0; i < o.Workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := range pool {
				writeTrack(o, j.album, j.track, &localStats[idx])
			}
		}(i)
	}
	for _, a := range p.Albums {
		for _, t := range a.Tracks {
			pool <- trackJob{album: a, track: t}
		}
	}
	close(pool)
	wg.Wait()

	mu.Lock()
	for i := range localStats {
		st.TracksWritten += localStats[i].TracksWritten
		st.TracksSkipped += localStats[i].TracksSkipped
		st.TracksFailed += localStats[i].TracksFailed
		st.TrackBytes += localStats[i].TrackBytes
		st.OtherBytes += localStats[i].OtherBytes
		st.Warnings += localStats[i].Warnings
		st.Errors += localStats[i].Errors
	}
	mu.Unlock()

	return st, mirror(o, p, st)
}

// preloadCovers extracts one embedded picture per album before the track
// jobs start - at most one ffmpeg call per album, cheap enough to stay
// sequential and easy to reason about.
func preloadCovers(o Options, albums []*plan.Album, st *Stats) {
	for _, album := range albums {
		if o.Dry || !album.Extractor {
			continue
		}
		if extractCover(o, album, st) {
			st.Covers++
			o.Log("cover  : %s", prettify(o, filepath.Join(album.OutDir, "cover.jpg")))
		}
	}
}

// writeTrack produces one track and accounts for it in the worker's private
// bucket. FLAC output gets its metadata from Go (multi-value Vorbis
// comments); a WAV source cut into WAV output needs no decoder at all.
func writeTrack(o Options, album *plan.Album, t *plan.Track, st *Stats) {
	if err := os.MkdirAll(album.OutDir, 0o755); err != nil {
		st.Errors++
		o.Log("error  : mkdir %s: %v", album.OutDir, err)
		return
	}
	out := filepath.Join(album.OutDir, t.Name)
	if !o.Force {
		if info, err := os.Stat(out); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			st.TracksSkipped++
			o.Log("skip   : %s (exists)", prettify(o, out))
			return
		}
	}
	if o.Dry {
		o.Log("plan   : %s <- %s (starts at sample %d)",
			prettify(o, out), album.CueBase(), t.Start)
		return
	}

	switch {
	case isExt(album.SourcePath, ".wav") && isExt(out, ".wav"):
		wav, err := audio.OpenWav(album.SourcePath)
		if err != nil {
			st.TracksFailed++
			st.Errors++
			o.Log("FAIL   : %s: %v", prettify(o, out), err)
			return
		}
		if err := wav.Split(out, t.Start, t.End); err != nil {
			st.TracksFailed++
			st.Errors++
			o.Log("FAIL   : %s: %v", prettify(o, out), err)
			return
		}
	case isExt(out, ".flac"):
		runner := engine.Runner{FfmpegBin: o.FfmpegBin, CompressionLevel: o.CompressionLevel}
		if err := runner.Split(album.SourcePath, album.SampleRate, t, out); err != nil {
			st.TracksFailed++
			st.Errors++
			st.Warnings++
			o.Log("FAIL   : %s: %v", prettify(o, out), err)
			return
		}
		if err := audio.WriteVorbisComment(out, toEntries(t.Tags)); err != nil {
			st.Warnings++
			o.Log("warning: %s tags: %v", prettify(o, out), err)
		}
	default:
		runner := engine.Runner{FfmpegBin: o.FfmpegBin, CompressionLevel: o.CompressionLevel}
		if err := runner.Split(album.SourcePath, album.SampleRate, t, out); err != nil {
			st.TracksFailed++
			st.Errors++
			st.Warnings++
			o.Log("FAIL   : %s: %v", prettify(o, out), err)
			return
		}
	}

	if info, err := os.Stat(out); err == nil {
		st.TrackBytes += uint64(info.Size())
	}
	st.TracksWritten++
	o.Log("write  : %s", prettify(o, out))
}

// Retag rewrites the metadata stream of the generated tracks in place. The
// audio is left untouched, which makes it the cheap way to upgrade a library
// that was split by an older cuefs (or by ffmpeg's tag writer).
func Retag(o Options) (*Stats, error) {
	o.defaults()
	st := &Stats{}
	p, err := BuildPlan(o)
	if err != nil {
		return st, err
	}
	st.Albums = len(p.Albums)
	for _, a := range p.Albums {
		st.Tracks += len(a.Tracks)
	}
	for _, a := range p.Albums {
		for _, t := range a.Tracks {
			out := filepath.Join(a.OutDir, t.Name)
			info, err := os.Stat(out)
			if err != nil || !info.Mode().IsRegular() {
				o.Log("skip   : %s (missing)", prettify(o, out))
				st.TracksSkipped++
				continue
			}
			if o.Dry {
				o.Log("plan   : %s (%d tag entries)", prettify(o, out), len(t.Tags))
				continue
			}
			if err := audio.WriteVorbisComment(out, toEntries(t.Tags)); err != nil {
				st.TracksFailed++
				st.Errors++
				o.Log("FAIL   : %s: %v", prettify(o, out), err)
				continue
			}
			st.TrackBytes += uint64(info.Size())
			st.TracksWritten++
			o.Log("write  : %s", prettify(o, out))
		}
	}
	return st, nil
}

// checkTags compares a produced FLAC's Vorbis Comment with the plan. Only
// FLAC outputs carry Vorbis comments; the check is skipped for anything else
// (a WAV output has no metadata stream of its own).
func checkTags(out string, tags []plan.Tag) string {
	if !isExt(out, ".flac") {
		return ""
	}
	blocks, _, _, err := audio.ReadMetaBlocks(out)
	if err != nil {
		return err.Error()
	}
	var entries []audio.VorbisEntry
	for _, b := range blocks {
		if b.Kind == audio.BlockVorbisComment {
			_, entries, err = audio.DecodeVorbisComment(b.Data)
			if err != nil {
				return err.Error()
			}
			break
		}
	}
	want := toEntries(tags)
	if len(entries) != len(want) {
		return fmt.Sprintf("got %d entries, want %d", len(entries), len(want))
	}
	for i := range want {
		if entries[i] != want[i] {
			return fmt.Sprintf("entry %d is %s=%q, want %s=%q",
				i, entries[i].Key, entries[i].Value, want[i].Key, want[i].Value)
		}
	}
	return ""
}

// toEntries maps the plan's tags onto Vorbis Comment entries, expanding the
// multi-value keys into one entry per value.
func toEntries(tags []plan.Tag) []audio.VorbisEntry {
	expanded := plan.Expand(tags)
	out := make([]audio.VorbisEntry, 0, len(expanded))
	for _, t := range expanded {
		out = append(out, audio.VorbisEntry{Key: t.Key, Value: t.Value})
	}
	return out
}

func isExt(path, want string) bool {
	return strings.EqualFold(filepath.Ext(path), want)
}

// extractCover pulls the first embedded picture into the album's directory.
func extractCover(o Options, album *plan.Album, st *Stats) bool {
	runner := engine.Runner{FfmpegBin: o.FfmpegBin}
	dst := filepath.Join(album.OutDir, "cover.jpg")
	if info, err := os.Stat(dst); err == nil && info.Size() > 0 {
		return false // already there
	}
	if err := runner.ExtractCover(album.SourcePath, dst); err != nil {
		o.Log("warning: %s: %v", dst, err)
		st.Warnings++
		return false
	}
	return true
}

// mirror copies or hardlinks every file that is neither a split-candidate
// source nor a cue sheet, keeping the output tree self-sufficient for
// Navidrome (covers, logs, m3u lists, already-split tracks).
func mirror(o Options, p *Plan, st *Stats) error {
	return filepath.WalkDir(o.SrcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(o.SrcRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if !p.AllDirs && !p.SelDirs[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.EqualFold(filepath.Ext(name), ".cue") || p.Sampled[path] {
			return nil
		}
		return passthrough(o, path, rel, st)
	})
}

// passthrough hardlinks (or copies) one unrelated file into the output tree.
func passthrough(o Options, src, rel string, st *Stats) error {
	if o.Dry {
		return nil
	}
	dst := filepath.ToSlash(filepath.Join(o.OutRoot, rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		st.Errors++
		o.Log("error  : mkdir %s: %v", filepath.Dir(dst), err)
		return nil
	}
	if info, err := os.Stat(dst); err == nil && info.Mode().IsRegular() {
		st.Others++
		if !o.Force {
			return nil
		}
	}
	if err := clearExisting(dst); err != nil {
		st.Errors++
		o.Log("error  : could not replace %s: %v", dst, err)
		return nil
	}
	if o.Link {
		if err := os.Link(src, dst); err == nil {
			st.Others++
			if info, e := os.Stat(dst); e == nil {
				st.OtherBytes += uint64(info.Size())
			}
			o.Log("link   : %s", dst)
			return nil
		}
		// Fall through and copy: cross-device links simply do not exist.
	}
	if err := copyFile(src, dst); err != nil {
		st.Errors++
		o.Log("error  : copy %s: %v", src, err)
		return nil
	}
	st.Others++
	if info, e := os.Stat(dst); e == nil {
		st.OtherBytes += uint64(info.Size())
	}
	o.Log("copied : %s", dst)
	return nil
}

// copyFile preserves mode and modification time so scanners keep the same
// change fingerprint as the source.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if info, err := in.Stat(); err == nil {
		_ = out.Chmod(info.Mode())
		_ = os.Chtimes(tmp, time.Now(), info.ModTime())
	}
	if err = out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// clearExisting removes dst and its .part leftovers so the next write starts
// from a clean slate.
func clearExisting(dst string) error {
	for _, p := range []string{dst, dst + ".part"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func prettify(o Options, p string) string {
	if rel, err := filepath.Rel(o.OutRoot, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

// Verify re-decodes every produced track and compares PCM md5 hashes against
// the source window the plan claims it came from (DESIGN §6, M1 acceptance).
func Verify(o Options) (*VerifyStats, error) {
	o.defaults()
	st := &VerifyStats{}
	p, err := BuildPlan(o)
	if err != nil {
		return st, err
	}
	o.SrcRoot = p.Root
	type check struct {
		src, out string
		rate     int
		track    *plan.Track
	}
	checks := make([]check, 0, 512)
	for _, a := range p.Albums {
		for _, t := range a.Tracks {
			checks = append(checks, check{
				src: a.SourcePath, out: filepath.Join(a.OutDir, t.Name),
				rate: a.SampleRate, track: t,
			})
		}
	}

	pool := make(chan check)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < o.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range pool {
				runner := engine.Runner{FfmpegBin: o.FfmpegBin}

				info, err := os.Stat(c.out)
				if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
					if err != nil && !os.IsNotExist(err) {
						o.Log("verify : %s: %v", prettify(o, c.out), err)
					}
					mu.Lock()
					st.Missing++
					mu.Unlock()
					continue
				}
				ref, rerr := runner.MD5OfWindow(c.src, c.rate, c.track)
				got, gerr := runner.MD5OfFile(c.out)
				mu.Lock()
				if rerr != nil {
					st.Checked++
					st.Failed++
					o.Log("FAIL   : %s reference: %v", prettify(o, c.out), rerr)
				} else if gerr != nil {
					st.Checked++
					st.Failed++
					o.Log("FAIL   : %s produced: %v", prettify(o, c.out), gerr)
				} else if ref != got {
					st.Checked++
					st.Failed++
					o.Log("FAIL   : %s ref=%s got=%s", prettify(o, c.out), ref, got)
				} else {
					st.Checked++
					o.Log("ok     : %s", prettify(o, c.out))
					if terr := checkTags(c.out, c.track.Tags); terr != "" {
						st.Failed++
						o.Log("FAIL   : %s tags: %s", prettify(o, c.out), terr)
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, c := range checks {
		pool <- c
	}
	close(pool)
	wg.Wait()
	return st, nil
}

// humanBytes renders a byte count for humans.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := int64(n) / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %c%ciB", float64(n)/float64(div), "KMGTPE"[exp], 'i')
}
