//go:build !linux

package main

import (
	"errors"
	"flag"
)

var (
	mountCmd          func(args []string) error
	collectMountFlags func(*flag.FlagSet) = func(s *flag.FlagSet) {
		s.String("src", "", "ignored: mount requires Linux")
	}
)

func init() {
	mountCmd = func(args []string) error {
		return errors.New("mount requires Linux (FUSE)")
	}
	_ = flag.ContinueOnError
}
