//go:build linux

package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"cuefs/internal/config"
	"cuefs/internal/cue"
	"cuefs/internal/fs"
	"time"
)

// newWorkFlags collects the flags that `split`, `retag`, `verify` and
// `mount` share.
type mountFlags struct {
	src        string
	mp         string
	cache      string
	fuseOpts   string
	allowOther bool
	synthesize bool
	quiet      bool
	workers    int
	refMode    string
}

func init() {
	var mfs mountFlags
	mountCmd = func(args []string) error { return cmdMount(args, &mfs) }
	collectMountFlags = func(s *flag.FlagSet) {
		s.StringVar(&mfs.src, "src", "", "music library root (default: config src)")
		s.StringVar(&mfs.mp, "mount", "", "mount point (default: config mount)")
		s.StringVar(&mfs.cache, "cache", "", "tree of generated tracks (default: config cache)")
		s.StringVar(&mfs.fuseOpts, "fuse-opts", "", "extra comma-separated fusermount3 options")
		s.BoolVar(&mfs.allowOther, "allow-other", false,
			"pass allow_other to fusermount3 (needs user_allow_other in /etc/fuse.conf)")
		s.BoolVar(&mfs.synthesize, "synthesize", false,
			"materialise cache misses with the configured external binary instead of erroring")
		s.BoolVar(&mfs.quiet, "quiet", false, "suppress per-file log lines")
		s.IntVar(&mfs.workers, "workers", -1, "max concurrent synthesis jobs (default: config workers)")
	}
}

func cmdMount(args []string, m *mountFlags) error {
	c := newCommonFlags("mount")
	collectMountFlags(c.fs)
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	if err := c.load(); err != nil {
		return err
	}
	if m.src == "" {
		m.src = c.cfg.Src
	}
	if m.mp == "" {
		m.mp = c.cfg.Mount
	}
	if m.cache == "" {
		m.cache = c.cfg.Cache
	}
	if m.src == "" || m.mp == "" || m.cache == "" {
		return fmt.Errorf("mount needs --src/--mount/--cache (or src/mount/cache in the config)")
	}
	if m.workers > 0 {
		c.cfg.Workers = m.workers
	}
	log := logf
	if m.quiet {
		log = func(string, ...any) {}
	}

	o := fs.Options{
		Src:               m.src,
		Cache:             config.ExpandHome(m.cache),
		Detect:            cue.DetectOptions{Force: c.encoding, List: c.cfg.Cue.EncodingList},
		SplitExtensions:   c.cfg.Split.Extensions,
		ApeMode:           c.cfg.Split.ApeMode,
		ApeExt:            c.cfg.ApeExt(),
		NameTemplate:      c.cfg.NameTemplate,
		MaxTitleLen:       c.cfg.TitleMaxLen,
		IgnoreTags:        c.cfg.Cue.IgnoreTags,
		FfprobeBin:        c.cfg.FfprobeBin,
		CompressionLevel:  c.cfg.Split.CompressionLevel,
		Workers:           c.cfg.Workers,
		FakeStreamInfoMD5: c.cfg.Split.FakeStreamInfoMD5,
		CacheMaxGB:        c.cfg.CacheMaxGB,
		CacheTTL:          time.Duration(c.cfg.CacheTTL.Duration),
		Log:               log,
	}
	if m.synthesize {
		// APE (and any FLAC that the pure-Go engine cannot handle) is
		// synthesised with the external binary on demand.
		o.FfmpegBin = c.cfg.FfmpegBin
	} else {
		log("mount   : flac/wav tracks keep the source's extension (pure Go); ape tracks convert to %s and need --synthesize + ffmpeg_bin", o.ApeExt)
	}

	if info, err := os.Stat(m.mp); err != nil && os.IsNotExist(err) {
		return fmt.Errorf("mount point %s does not exist", m.mp)
	} else if info != nil && !info.IsDir() {
		return fmt.Errorf("mount point %s is not a directory", m.mp)
	}

	server, err := fs.Mount(m.mp, o, splitCsl(m.fuseOpts), m.allowOther)
	if err != nil {
		return err
	}
	log("mounted : %s -> %s (cache %s)", m.mp, m.src, o.Cache)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	if err := server.Unmount(); err != nil {
		return fmt.Errorf("unmount %s: %w", m.mp, err)
	}
	log("unmounted")
	return nil
}

func splitCsl(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
