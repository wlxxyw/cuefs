// Package engine shells out to ffmpeg to produce and verify split tracks
// (DESIGN §4.3). Everything here is per-track and side-effect free except for
// the output file it is asked to create.
package engine

import (
	"fmt"
	"os"
	"strconv"

	"cuefs/internal/plan"
)

// Runner holds the external binaries and their tuning knobs.
type Runner struct {
	FfmpegBin        string
	CompressionLevel int
}

func (r *Runner) defaults() {
	if r.FfmpegBin == "" {
		r.FfmpegBin = "ffmpeg"
	}
	if r.CompressionLevel <= 0 {
		r.CompressionLevel = 5
	}
}

// SplitArgs renders the argument list that produces one track. Exposed for
// logging and for `-n` reporting.
//
// Exactness comes from atrim (verified sample-exact), while -t merely makes
// the pipeline stop early once the atrim end has been passed, so that a
// track in the middle of an album does not decode to the end of the file.
// -t is generous (+1 s) precisely because atrim is authoritative.
func (r *Runner) SplitArgs(src string, rate int, t *plan.Track, out string) []string {
	r.defaults()
	offset, filter := t.AtrimSpec(rate)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-ss", offset,
		"-i", src,
		"-map", "0:a:0", "-vn",
		"-map_metadata", "-1", "-map_chapters", "-1",
		"-af", filter,
	}
	if t.End > 0 {
		rate := float64(rate)
		length := float64(t.End-t.Start) / rate
		args = append(args, "-t", fmt.Sprintf("%.20g", length+1.0))
	}
	args = append(args,
		// The output is renamed to its final name afterwards, so the muxer
		// cannot be guessed from the ".part" extension. Tags are NOT written
		// here: the metadata stream is authored by cuefs itself so that
		// multi-value Vorbis comments survive (DESIGN §4.3, M4).
		"-c:a", "flac", "-f", "flac",
		"-compression_level", strconv.Itoa(r.CompressionLevel),
	)
	return append(args, out)
}

// Split runs ffmpeg for one track, writing to tmp first and renaming to out
// so that an interrupted run never leaves a truncated FLAC behind.
func (r *Runner) Split(src string, rate int, t *plan.Track, out string) error {
	r.defaults()
	if t.Tags == nil {
		t.Tags = []plan.Tag{}
	}
	tmp := out + ".part"
	os.Remove(tmp)

	// Note the -ss offset comes FIRST, before -i, so ffmpeg seeks instead of
	// decoding from byte zero (DESIGN §4.3).
	args := r.SplitArgs(src, rate, t, tmp)
	if _, filter := t.AtrimSpec(rate); filter == "" {
		return fmt.Errorf("missing atrim filter for %s", out)
	}
	if err := runCapture(r.FfmpegBin, args, ""); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, out, err)
	}
	return nil
}

// MD5OfWindow is the reference decoder side of the verifier: it re-reads the
// source with the same seek+trim combination and reports the PCM MD5. atrim
// stays authoritative; -t only stops the pipeline early.
func (r *Runner) MD5OfWindow(src string, rate int, t *plan.Track) (string, error) {
	r.defaults()
	offset, filter := t.AtrimSpec(rate)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", offset, "-i", src,
		"-map", "0:a:0", "-vn", "-af", filter,
	}
	if t.End > 0 {
		length := float64(t.End-t.Start) / float64(rate)
		args = append(args, "-t", fmt.Sprintf("%.20g", length+1.0))
	}
	return runMD5(r.FfmpegBin, append(args, "-f", "md5", "-"))
}

// MD5OfFile feeds a produced file back through the decoder.
func (r *Runner) MD5OfFile(path string) (string, error) {
	r.defaults()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", path, "-map", "0:a:0", "-vn", "-map_metadata", "-1",
		"-f", "md5", "-",
	}
	return runMD5(r.FfmpegBin, args)
}

// ExtractCover pulls the first embedded picture out of src, if any.
func (r *Runner) ExtractCover(src, out string) error {
	r.defaults()
	tmp := out + ".part"
	os.Remove(tmp)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", src, "-map", "0:v:0", "-frames:v", "1",
		"-map_metadata", "-1", "-f", "image2",
		tmp,
	}
	if err := runCapture(r.FfmpegBin, args, "extract cover from "+src); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, out)
}
