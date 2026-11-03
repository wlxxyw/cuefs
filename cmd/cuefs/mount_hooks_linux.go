//go:build linux

package main

import "flag"

var (
	// mountCmd is wired up by init() in mount_linux.go; nil means the
	// command exists but was never configured (can't happen on Linux).
	mountCmd func(args []string) error
	// collectMountFlags registers the mount flags on a flag set.
	collectMountFlags func(*flag.FlagSet)
)
