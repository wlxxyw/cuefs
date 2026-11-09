// Command cuefs exposes album-sized audio files as a virtual filesystem of
// individual tracks, so that Navidrome (and similar players) can index them.
//
// This build ships the M0 milestone: cue parsing, encoding detection and the
// probe/dump diagnostics. See docs/DESIGN.md for the full plan.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cuefs/internal/audio"
	"cuefs/internal/config"
	"cuefs/internal/cue"
	"cuefs/internal/library"
	"cuefs/internal/split"
)

const version = "0.2.0-m4.10"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "probe":
		err = cmdProbe(args)
	case "scan":
		err = cmdScan(args)
	case "split":
		err = cmdSplit(args)
	case "gc":
		err = cmdGc(args)
	case "verify":
		err = cmdVerify(args)
	case "mount":
		err = mountCmd(args)
	case "retag":
		err = cmdRetag(args)
	case "flacinfo":
		err = cmdFlacinfo(args)
	case "dump":
		err = cmdDump(args)
	case "debugmd5":
		err = cmdDebugMD5(args)
	case "splitone":
		err = cmdSplitOne(args)
	case "version", "--version", "-V":
		fmt.Println("cuefs", version)
	case "help", "--help", "-h":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "cuefs: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cuefs:", err)
		os.Exit(1)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `cuefs - CUE virtual filesystem for Navidrome

Usage:
  cuefs <command> [flags] <path>

Commands:
  probe     Report how a cue file's encoding was detected and whether it parses
  scan      Classify every cue file under a directory (read-only)
  split     Materialise tracks + passthrough into an output tree
  gc        Prune cache orphans (sources gone) and report the size budget
  verify    Re-decode the produced tracks and compare PCM md5 against the source
  retag     Rewrite the metadata stream of the generated tracks in place
  flacinfo  Dump a FLAC file's metadata blocks and frame chain (no decoding)
  mount     Read-only FUSE overlay: src with tracks (Linux only)
  dump      Print the parsed cue structure (tracks, times, tags)
  debugmd5  Diagnostic: a sample window's PCM md5 via the pure-Go decoder
  splitone  Diagnostic: cut one FLAC track from one FLAC file by sample index
  version   Print the version

Run "cuefs <command> -h" for command flags.
`)
}

// --- shared flag plumbing --------------------------------------------------

type commonFlags struct {
	fs           *flag.FlagSet
	configPath   string
	encoding     string
	encodingList string
	jsonOut      bool
	detect       cue.DetectOptions
	cfg          *config.Config
	loadedFrom   string
}

func newCommonFlags(name string) *commonFlags {
	c := &commonFlags{fs: flag.NewFlagSet(name, flag.ContinueOnError)}
	c.fs.StringVar(&c.configPath, "config", "", "path to cuefs.yaml (default: search standard locations)")
	c.fs.StringVar(&c.encoding, "encoding", "", "force a cue encoding instead of detecting (e.g. gb18030)")
	c.fs.StringVar(&c.encodingList, "encoding-list", "", "comma-separated encodings to try, in order")
	c.fs.BoolVar(&c.jsonOut, "json", false, "emit JSON instead of human-readable text")
	return c
}

// load resolves configuration and turns it into cue detection options.
func (c *commonFlags) load() error {
	cfg, file, err := config.Load(c.configPath)
	if err != nil {
		return err
	}
	c.cfg, c.loadedFrom = cfg, file

	force := c.encoding
	if force == "" {
		force = cfg.Cue.Encoding
	}
	list := cfg.Cue.EncodingList
	if c.encodingList != "" {
		list = nil
		for _, e := range strings.Split(c.encodingList, ",") {
			if e = strings.TrimSpace(e); e != "" {
				list = append(list, e)
			}
		}
	}
	c.detect = cue.DetectOptions{Force: force, List: list}
	return nil
}

// optionsFor applies the per-directory encoding override (DESIGN §4.2) to the
// detection options for a specific cue file.
func (c *commonFlags) optionsFor(path string) cue.DetectOptions {
	opts := c.detect
	abs, err := filepath.Abs(path)
	if err != nil {
		return opts
	}
	dir := filepath.Dir(abs)
	for p, ov := range c.cfg.Cue.CueOverrides {
		if ov.Encoding == "" {
			continue
		}
		op, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if dir == op || strings.HasPrefix(dir, op+string(os.PathSeparator)) {
			opts.Force = ov.Encoding
			break
		}
	}
	return opts
}

// --- probe -----------------------------------------------------------------

type probeOut struct {
	Path     string            `json:"path"`
	Source   string            `json:"source,omitempty"`
	Kind     library.Kind      `json:"kind"`
	Reason   string            `json:"reason"`
	Encoding *cue.DetectResult `json:"encoding,omitempty"`
	Parsed   bool              `json:"parsed"`
	Summary  string            `json:"summary,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
	Err      string            `json:"error,omitempty"`
}

func cmdProbe(args []string) error {
	c := newCommonFlags("probe")
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	if err := c.load(); err != nil {
		return err
	}
	if c.fs.NArg() != 1 {
		return fmt.Errorf("probe needs exactly one .cue file")
	}
	path := c.fs.Arg(0)

	e := library.Classify(path, c.cfg.Split.Extensions, c.optionsFor(path))
	out := probeOut{
		Path: path, Source: e.SourcePath, Kind: e.Kind, Reason: e.Reason,
		Err: e.Err, Warnings: e.Warnings,
	}
	if e.Sheet != nil {
		out.Parsed = true
		out.Summary = e.Sheet.Summary()
		out.Encoding = e.Sheet.Encoding
	}
	if c.jsonOut {
		return printJSON(out)
	}

	fmt.Printf("file       : %s\n", path)
	if e.SourcePath != "" {
		fmt.Printf("source     : %s\n", e.SourcePath)
	} else {
		fmt.Printf("source     : (none - album file with this basename does not exist)\n")
	}
	fmt.Printf("kind       : %s (%s)\n", e.Kind, e.Reason)
	if out.Encoding != nil {
		fmt.Printf("encoding   : %s (origin=%s, confidence=%.2f)\n",
			out.Encoding.Encoding, out.Encoding.Origin, out.Encoding.Confidence)
		if len(out.Encoding.Candidates) > 0 {
			fmt.Println("candidates :")
			for _, cd := range out.Encoding.Candidates {
				status := "invalid"
				if cd.Valid {
					status = "valid  "
				}
				fmt.Printf("  %-14s %-10s %s %7.1f  %s\n", cd.Name, cd.Origin, status, cd.Score, cd.Reason)
			}
		}
	}
	if len(c.loadedFrom) > 0 {
		fmt.Printf("config     : %s\n", c.loadedFrom)
	}
	if len(out.Warnings) > 0 {
		fmt.Println("warnings   :")
		for _, w := range out.Warnings {
			fmt.Println("  -", w)
		}
	}
	if out.Err != "" {
		fmt.Println("error      :", out.Err)
	}
	if out.Parsed {
		fmt.Println("parsed     :", out.Summary)
	} else {
		fmt.Println("parsed     : FAILED")
	}
	return nil
}

// --- scan ------------------------------------------------------------------

type scanOut struct {
	Root    string          `json:"root"`
	Entries []library.Entry `json:"entries"`
	Summary library.Summary `json:"summary"`
}

// cmdScan classifies every cue file under a directory. It is strictly
// read-only (DESIGN D15): it stats and reads cue files, nothing else.
func cmdScan(args []string) error {
	c := newCommonFlags("scan")
	all := c.fs.Bool("all", false, "list passthrough and error entries too, not just split candidates")
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	if err := c.load(); err != nil {
		return err
	}
	if c.fs.NArg() != 1 {
		return fmt.Errorf("scan needs exactly one directory")
	}
	root := library.ResolveRoot(c.fs.Arg(0))

	out := scanOut{Root: root}
	err := library.Scan(root, c.cfg.Split.Extensions, c.optionsFor(root), func(e library.Entry) {
		out.Summary.Add(e)
		if *all || e.Kind == library.KindSplit {
			out.Entries = append(out.Entries, e)
		}
	})
	if err != nil {
		return err
	}

	if c.jsonOut {
		return printJSON(out)
	}

	for _, e := range out.Entries {
		rel, rerr := filepath.Rel(root, e.CuePath)
		if rerr != nil {
			rel = e.CuePath
		}
		src := "-"
		if e.SourcePath != "" {
			if s, err := filepath.Rel(root, e.SourcePath); err == nil {
				src = s
			} else {
				src = e.SourcePath
			}
		}
		note := e.Reason
		if e.Err != "" {
			note = e.Err
		}
		fmt.Printf("%-11s tracks=%-3d %-46s <- %s\n", e.Kind, e.Tracks, rel, src)
		if e.Kind != library.KindSplit && note != e.Reason {
			fmt.Printf("            %s\n", note)
		}
	}
	s := out.Summary
	fmt.Printf("\n%d cue file(s): %d split candidate(s) (%d track(s) to generate), %d passthrough, %d error(s)\n",
		s.Cues, s.Split, s.Tracks, s.Passthrough, s.Errors)
	return nil
}

// --- dump ------------------------------------------------------------------

func cmdDump(args []string) error {
	c := newCommonFlags("dump")
	duration := c.fs.String("duration", "", "audio duration as mm:ss:ff, used to resolve the last track")
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	if err := c.load(); err != nil {
		return err
	}
	if c.fs.NArg() != 1 {
		return fmt.Errorf("dump needs exactly one .cue file")
	}
	path := c.fs.Arg(0)

	var end cue.Time
	if *duration != "" {
		t, err := cue.ParseTime(*duration)
		if err != nil {
			return err
		}
		end = t
	}

	s, err := cue.Load(path, c.optionsFor(path), func(f *cue.File) (cue.Time, bool) {
		if *duration == "" {
			return cue.Time{}, false
		}
		return end, true
	})
	if err != nil {
		return err
	}

	if c.jsonOut {
		return printJSON(s)
	}

	fmt.Printf("cue    : %s\n", path)
	if s.Encoding != nil {
		fmt.Printf("encoding: %s (origin=%s, confidence=%.2f)\n",
			s.Encoding.Encoding, s.Encoding.Origin, s.Encoding.Confidence)
	}
	if s.Title != "" {
		fmt.Printf("album  : %s\n", s.Title)
	}
	if len(s.Performers) > 0 {
		fmt.Printf("artist : %s\n", strings.Join(s.Performers, "; "))
	}
	if s.Catalog != "" {
		fmt.Printf("catalog: %s\n", s.Catalog)
	}
	if len(s.Rem) > 0 {
		rem := make(map[string][]string, len(s.Rem))
		for k, v := range s.Rem {
			rem[k] = []string{v}
		}
		keys := cue.SortedTagNames(rem)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+rem[k][0])
		}
		fmt.Printf("rem    : %s\n", strings.Join(parts, ", "))
	}
	fmt.Println()

	for _, f := range s.Files {
		fmt.Printf("FILE %q (%s)\n", f.Name, f.Type)
		if f.DurationKnown {
			fmt.Printf("  duration: %s (%.2fs)\n", f.Duration, f.Duration.DurationSeconds())
		}
		for _, t := range f.Tracks {
			mark := " "
			if !t.Type.IsAudio() {
				mark = "D" // data track
			}
			line := fmt.Sprintf("  %s %02d  %-6s %s -> %s", mark, t.Num, t.Type, dash(t.Start, t.StartKnown), dash(t.End, t.EndKnown))
			if t.StartKnown && t.EndKnown {
				line += fmt.Sprintf("  (%.2fs)", t.Duration().DurationSeconds())
			}
			if t.Title != "" {
				line += "  " + t.Title
			}
			if len(t.Performers) > 0 {
				line += "  [" + strings.Join(t.Performers, "; ") + "]"
			}
			fmt.Println(line)
			if idx := formatIndexes(t.Index); idx != "" {
				fmt.Printf("      index: %s\n", idx)
			}
		}
	}
	fmt.Println()

	if tags := s.AlbumTags(); len(tags) > 0 {
		fmt.Println("album tags:")
		printTags(tags)
	}
	if len(s.Warnings) > 0 {
		fmt.Println("warnings:")
		for _, w := range s.Warnings {
			fmt.Println("  -", w)
		}
	}
	return nil
}

func dash(t cue.Time, known bool) string {
	if !known {
		return "  ??:??:??"
	}
	return t.String()
}

func formatIndexes(idx map[int]cue.Time) string {
	if len(idx) == 0 {
		return ""
	}
	keys := make([]int, 0, len(idx))
	for k := range idx {
		keys = append(keys, k)
	}
	// small maps: insertion sort keeps output stable without extra imports
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("INDEX %02d %s", k, idx[k]))
	}
	return strings.Join(parts, ", ")
}

func printTags(tags map[string][]string) {
	for _, k := range cue.SortedTagNames(tags) {
		fmt.Printf("  %-14s %s\n", k, strings.Join(tags[k], " | "))
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// cmdFlacinfo dumps a FLAC file's structure without decoding anything: the
// metadata block chain, the frame chain (which must add up with STREAMINFO's
// total_samples - that cross-check is what makes it a useful diagnostic) and,
// optionally, how a cue sheet's track boundaries relate to the frame grid.
func cmdFlacinfo(args []string) error {
	fs := flag.NewFlagSet("flacinfo", flag.ContinueOnError)
	cuePath := fs.String("cue", "", "cue sheet to check track boundary alignment against")
	frames := fs.Int("frames", 0, "print the first N frames with their byte offsets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("flacinfo needs exactly one .flac file")
	}
	path := fs.Arg(0)

	blocks, si, framesOffset, err := audio.ReadMetaBlocks(path)
	if err != nil {
		return err
	}

	fmt.Printf("file    : %s\n", path)
	fmt.Println("blocks  :")
	for _, b := range blocks {
		fmt.Printf("  %-15s %7d byte(s) at %d last=%v\n", b.Name(), len(b.Data), b.Offset, b.Last)
	}
	fmt.Printf("frames  : metadata chain ends at %d\n", framesOffset)
	idx, walkErr := audio.IndexFlacFrames(path)
	if walkErr != nil {
		fmt.Println("WARNING :", walkErr)
	}
	fmt.Printf("          walked %d frame(s), first frame starts at %d\n",
		len(idx.Frames), idx.FramesOffset+idx.LeadingJunk)
	if *frames > 0 {
		n := *frames
		if n > len(idx.Frames) {
			n = len(idx.Frames)
		}
		for i := 0; i < n; i++ {
			f := idx.Frames[i]
			fmt.Printf("  frame %4d: offset %10d len %5d samples %5d start %10d (%s)\n",
				i+1, f.Offset, f.Length, f.Samples, f.Start, f.Coding)
		}
	}
	if idx.LeadingJunk != 0 {
		fmt.Printf("  NOTE: %d stray byte(s) between the metadata chain and the first frame\n",
			idx.LeadingJunk)
	}
	var sum int64
	for _, f := range idx.Frames {
		sum += int64(f.Samples)
	}
	if len(idx.Frames) > 0 {
		fmt.Printf("  block size   : %d..%d samples\n", pmin(idx.Frames), pmax(idx.Frames))
		fmt.Printf("  total samples: %d (frame chain), %d (STREAMINFO)\n", sum, si.TotalSamples)
		if sum != si.TotalSamples {
			fmt.Println("  MISMATCH between the frame chain and STREAMINFO")
		}
		if si.TotalSamples > 0 && si.SampleRate > 0 {
			fmt.Printf("  duration     : %.3f s\n", float64(si.TotalSamples)/float64(si.SampleRate))
		}
	}
	fmt.Printf("trailer : %d byte(s) after the last frame\n", idx.Trailer)

	if *cuePath == "" {
		return nil
	}
	sheet, err := cue.Load(*cuePath, cue.DetectOptions{Force: "auto"}, nil)
	if err != nil {
		return err
	}
	fmt.Printf("\ntrack boundaries vs frame grid (%d..%d samples per frame):\n",
		pmin(idx.Frames), pmax(idx.Frames))
	aligned := 0
	for _, t := range sheet.AudioTracks() {
		start, err := cue.Samples(t.Start, si.SampleRate)
		if err != nil {
			return err
		}
		i := frameContaining(idx.Frames, start)
		fi := idx.Frames[i]
		off := start - fi.Start
		mark := "aligned"
		if off != 0 {
			mark = fmt.Sprintf("off by %d sample(s) (%.1f ms)",
				off, 1000*float64(off)/float64(si.SampleRate))
		} else {
			aligned++
		}
		fmt.Printf("  %02d  start %10d -> frame %d/%d (%7d samples)  %s\n",
			t.Num, start, i+1, len(idx.Frames), fi.Samples, mark)
	}
	if aligned == 0 {
		fmt.Println("\nno cue boundary is frame-aligned, so a byte-for-byte FLAC split")
		fmt.Println("cannot represent these tracks: the boundary frames have to be re-encoded.")
	}
	return nil
}

func pmin(f []audio.FlacFrame) int {
	m := 0
	for _, x := range f {
		if m == 0 || x.Samples < m {
			m = x.Samples
		}
	}
	return m
}

func pmax(f []audio.FlacFrame) int {
	m := 0
	for _, x := range f {
		if x.Samples > m {
			m = x.Samples
		}
	}
	return m
}

// frameContaining returns the index of the frame that holds the sample.
func frameContaining(frames []audio.FlacFrame, sample int64) int {
	lo, hi := 0, len(frames)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if frames[mid].Start <= sample {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo < 0 {
		lo = 0
	}
	return lo
}

// --- split / verify --------------------------------------------------------

// logf writes to stderr so that stdout keeps only the summary.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

type workFlags struct {
	src             string
	out             string
	only            string
	force           bool
	link            bool
	skipPassthrough bool
	dry             bool
	quiet           bool
	workers         int
	refMode         string
}

func newWorkFlags(name string, verify bool) (*commonFlags, *workFlags) {
	c := newCommonFlags(name)
	w := &workFlags{workers: -1}
	c.fs.StringVar(&w.src, "src", "", "root of the source music library (default: config src)")
	c.fs.StringVar(&w.out, "out", "", "root of the tree to write into (default: config mount)")
	c.fs.StringVar(&w.only, "only", "", "act only on albums whose name contains this substring")
	c.fs.BoolVar(&w.force, "force", false, "rebuild tracks that already exist")
	c.fs.BoolVar(&w.quiet, "quiet", false, "suppress per-track log lines")
	c.fs.IntVar(&w.workers, "workers", -1, "parallel jobs (default: config workers)")
	if verify {
		c.fs.StringVar(&w.refMode, "ref", "auto", "reference decoder for verify: auto | ffmpeg | pure")
	}
	if !verify {
		c.fs.BoolVar(&w.link, "link", false,
			"hardlink passthrough files instead of copying them (same filesystem only)")
		c.fs.BoolVar(&w.skipPassthrough, "skip-passthrough", false,
			"skip files that do not need splitting (covers/logs/m3u/...) and list them as skipped; split tracks only")
		c.fs.BoolVar(&w.dry, "dry", false, "plan and report without writing anything")
	}
	return c, w
}

// options turns flags + config into a split.Options.
func (c *commonFlags) options(w *workFlags) (split.Options, error) {
	if w.src == "" {
		w.src = c.cfg.Src
	}
	if w.out == "" {
		w.out = c.cfg.Mount
	}
	if w.src == "" {
		return split.Options{}, fmt.Errorf("no --src and no src in the config")
	}
	if w.out == "" {
		return split.Options{}, fmt.Errorf("no --out and no mount in the config")
	}
	if w.workers > 0 {
		c.cfg.Workers = w.workers
	}
	log := logf
	if w.quiet {
		log = func(string, ...any) {}
	}
	return split.Options{
		SrcRoot:           w.src,
		OutRoot:           w.out,
		SplitExtensions:   c.cfg.Split.Extensions,
		EncodingList:      c.cfg.Cue.EncodingList,
		ForceEncoding:     c.encoding,
		ApeExt:            c.cfg.ApeExt(),
		ApeMode:           c.cfg.Split.ApeMode,
		NameTemplate:      c.cfg.NameTemplate,
		MaxTitleLen:       c.cfg.TitleMaxLen,
		IgnoreTags:        c.cfg.Cue.IgnoreTags,
		FfmpegBin:         c.cfg.FfmpegBin,
		FfprobeBin:        c.cfg.FfprobeBin,
		CompressionLevel:  c.cfg.Split.CompressionLevel,
		FakeStreamInfoMD5: c.cfg.Split.FakeStreamInfoMD5,
		Workers:           c.cfg.Workers,
		Reference:         w.refMode,
		Force:             w.force,
		Link:              w.link,
		SkipPassthrough:   w.skipPassthrough,
		Only:              w.only,
		Dry:               w.dry,
		CacheMaxGB:        c.cfg.CacheMaxGB,
		CacheTTL:          time.Duration(c.cfg.CacheTTL.Duration),
		Log:               log,
	}, nil
}

// cmdSplit materialises a library: one generated FLAC per track, plus every
// other file hardlinked or copied into the output tree (skipped entirely
// with --skip-passthrough).
func cmdSplit(args []string) error {
	c, w := newWorkFlags("split", false)
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	if err := c.load(); err != nil {
		return err
	}
	o, err := c.options(w)
	if err != nil {
		return err
	}
	st, err := split.Materialize(o)
	if err != nil {
		return err
	}
	st.Report(os.Stdout)
	return nil
}

// cmdRetag rewrites only the metadata stream of the generated tracks (no
// audio work), which upgrades a library split by an older cuefs.
func cmdRetag(args []string) error {
	c, w := newWorkFlags("retag", true)
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	if err := c.load(); err != nil {
		return err
	}
	o, err := c.options(w)
	if err != nil {
		return err
	}
	o.Workers = 1 // metadata rewrite is I/O bound single task per file
	st, err := split.Retag(o)
	if err != nil {
		return err
	}
	st.Report(os.Stdout)
	return nil
}

// cmdVerify re-decodes every produced track and compares PCM md5 against the
// source window the plan claims it came from.
//
// --ref chooses the reference decoder:
//
//	auto    ffmpeg if configured, else the pure-Go decoder (default)
//	ffmpeg  independent implementation (the strongest check)
//	pure    shares code with the splitter; only useful without ffmpeg
func cmdVerify(args []string) error {
	c, w := newWorkFlags("verify", true)
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	if err := c.load(); err != nil {
		return err
	}
	o, err := c.options(w)
	if err != nil {
		return err
	}
	o.Log = logf
	st, err := split.Verify(o)
	if err != nil {
		return err
	}
	st.Report(os.Stdout)
	return nil
}
