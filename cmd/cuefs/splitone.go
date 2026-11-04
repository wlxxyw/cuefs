package main

import (
	"flag"
	"fmt"
	"strings"

	"cuefs/internal/audio"
)

// cmdSplitOne materialises one FLAC track from a single FLAC file with a
// hard-coded sample window. It is a diagnostic tool for the pure-Go engine
// (DESIGN §4.3.2): a `cuefs split` equivalent that needs no cue library, so
// a suspect window can be reproduced and cross-checked in isolation
// (typically with `debugmd5` on both sides).
func cmdSplitOne(args []string) error {
	fl := flag.NewFlagSet("splitone", flag.ExitOnError)
	start := fl.Int64("start", 0, "sample window start (inclusive)")
	end := fl.Int64("end", -1, "sample window end (exclusive); -1 = to EOF")
	rate := fl.Int("rate", 44100, "sample rate")
	ch := fl.Int("ch", 2, "channel count")
	bits := fl.Int("bits", 16, "bit depth")
	tags := fl.String("tags", "", "comma-separated KEY=VALUE tags for the Vorbis Comment")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 2 {
		return fmt.Errorf("splitone needs <src.flac> <out.flac>")
	}
	src, out := fl.Arg(0), fl.Arg(1)

	var entries []audio.VorbisEntry
	for _, kv := range strings.Split(*tags, ",") {
		if kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("bad tag %q (want KEY=VALUE)", kv)
		}
		entries = append(entries, audio.VorbisEntry{Key: k, Value: v})
	}
	return audio.WriteFLACTrack(audio.SplitSpec{
		Src:        src,
		Start:      *start,
		End:        *end,
		SampleRate: *rate,
		Channels:   *ch,
		Bits:       *bits,
		Tags:       entries,
	}, out)
}
