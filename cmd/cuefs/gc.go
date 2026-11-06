package main

import (
	"cuefs/internal/config"
	"cuefs/internal/split"
)

// cmdGc is the explicit maintenance entry point: it prunes cache-tree files
// whose sources left the plan (orphaned split output), removes crashed-run
// .part leftovers, removes directories those deletions emptied, and reports
// the tree's size against cache_max_gb.
//
// It deliberately does NOT evict on TTL or size: eviction needs fresh access
// times, which an offline gc cannot know (a file may be being read right
// now, and the kernel's relatime quantises atime to days anyway). Those two
// jobs belong to the mount's periodic task (internal/fs), which keeps its
// own access table.
func cmdGc(args []string) error {
	c, w := newWorkFlags("gc", false)
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
	// gc prunes the CACHE tree: default --out to config cache (expanded),
	// not config mount like the writing commands do.
	if o.OutRoot == c.cfg.Mount || o.OutRoot == "" {
		o.OutRoot = config.ExpandHome(c.cfg.Cache)
	}
	o.Workers = 1

	st, werr := split.Prune(o)
	if werr != nil {
		return werr
	}
	st.Report()
	return nil
}
