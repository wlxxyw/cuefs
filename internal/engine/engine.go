// Package engine shells out to ffmpeg to produce and verify split tracks
// (DESIGN §4.3). Everything here is per-track and side-effect free except for
// the output file it is asked to create.
package engine

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"cuefs/internal/audio"
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

// MD5OfWindow is the reference decoder side of the verifier. With
// RefMode "auto" (the default) it uses ffmpeg when that binary is
// configured - an *independent* implementation, which is what makes a
// passing verify meaningful for the pure-Go path (DESIGN §4.3.1).
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

// MD5OfWindowPure computes the source window's PCM md5 with the pure-Go
// decoder (mewkiz). It is the fallback reference when no external binary is
// configured - note that it then shares code with the splitter, so it can no
// longer catch a decoder bug on its own. Reports the window's span, the exact
// sample count the production side must cover for the comparison to be sound.
//
// A frame chain that dead-ends is tolerated as long as the covered prefix
// contains the whole window: the coverage boundary and the stats are those of
// the CRC-anchored chain, which is what a splitter would rely on too.
func MD5OfWindowPure(src string, t *plan.Track, bits int) (string, int64, error) {
	idx, err := audio.IndexFlacFrames(src)
	end := t.End
	if end < 0 {
		// The last track runs to the end of the (covered) stream.
		end = idx.TotalSamples
	}
	if err != nil {
		if len(idx.Frames) == 0 {
			return "", 0, err
		}
		last := idx.Frames[len(idx.Frames)-1]
		if end > last.Start+int64(last.Samples) {
			return "", 0, err
		}
	}
	i0, i1, ok := audio.CoveringFrames(idx, t.Start, end)
	if !ok {
		return "", 0, fmt.Errorf("no frames cover [%d,%d)", t.Start, end)
	}
	samples, err := audio.DecodeIndexRange(src, idx.Frames[i0:i1+1])
	if err != nil {
		return "", 0, err
	}
	spec := audio.SplitSpec{Start: t.Start, End: end, Bits: bits}
	sum, err := audio.ChecksumRange(samples, spec, idx.Frames[i0:i1+1])
	if err != nil {
		return "", 0, err
	}
	// ChecksumRange returns the raw digest; match MD5OfFile's hex form.
	return hex.EncodeToString(sum), end - t.Start, nil
}

// MD5OfFilePure decodes a produced FLAC file with the pure-Go decoder and
// reports the PCM md5 of its first `want` samples - the production-side
// counterpart of MD5OfWindowPure, clipped to the same span the reference
// covers (a fallback-written file may legitimately cover more than the
// pure-Go chain view of its source).
func MD5OfFilePure(path string, want int64) (string, error) {
	idx, err := audio.IndexFlacFrames(path)
	if err != nil {
		return "", err
	}
	if len(idx.Frames) == 0 {
		return "", fmt.Errorf("%s: no frames to decode", path)
	}
	if want <= 0 {
		want = idx.TotalSamples
	}
	if want > idx.TotalSamples {
		return "", fmt.Errorf("%s: covers %d sample(s), plan needs %d",
			path, idx.TotalSamples, want)
	}
	i0, i1, ok := audio.CoveringFrames(idx, 0, want)
	if !ok {
		return "", fmt.Errorf("%s: no frames cover [%d,%d)", path, 0, want)
	}
	samples, err := audio.DecodeIndexRange(path, idx.Frames[i0:i1+1])
	if err != nil {
		return "", err
	}
	spec := audio.SplitSpec{Start: 0, End: want, Bits: idx.StreamInfo.BitsPerSample}
	sum, err := audio.ChecksumRange(samples, spec, idx.Frames[i0:i1+1])
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum), nil
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
