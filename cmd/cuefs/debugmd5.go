package main

import (
	"encoding/hex"
	"flag"
	"fmt"

	"cuefs/internal/audio"
)

// cmdDebugMD5 is a low-level tool: it indexes a FLAC file, decodes the
// frames covering a sample window and prints the window's PCM md5 the same
// way verify's pure reference does. Swap-out for when verify reports a
// mismatch and you need to see the intermediate values.
func cmdDebugMD5(args []string) error {
	fl := flag.NewFlagSet("debugmd5", flag.ExitOnError)
	start := fl.Int64("start", 0, "sample window start (inclusive)")
	end := fl.Int64("end", 0, "sample window end (exclusive)")
	bits := fl.Int("bits", 16, "bit depth for the md5 width")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 1 {
		return fmt.Errorf("debugmd5 needs exactly one FLAC file")
	}
	path := fl.Arg(0)

	idx, err := audio.IndexFlacFrames(path)
	if err != nil {
		return err
	}
	i0, i1, ok := audio.CoveringFrames(idx, *start, *end)
	if !ok {
		return fmt.Errorf("no frames cover [%d,%d)", *start, *end)
	}
	samples, err := audio.DecodeIndexRange(path, idx.Frames[i0:i1+1])
	if err != nil {
		return err
	}
	sum, err := audio.ChecksumRange(samples, audio.SplitSpec{
		Start: *start, End: *end, Bits: *bits,
	}, idx.Frames[i0:i1+1])
	if err != nil {
		return err
	}
	fmt.Printf("frames %d..%d (%d frame(s))\n", i0, i1, i1-i0+1)
	fmt.Printf("window  [%d, %d) -> %d sample(s)\n", *start, *end, *end-*start)
	fmt.Printf("pcm  md5: %s\n", hex.EncodeToString(sum))
	return nil
}
