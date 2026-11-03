package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cuefs/internal/audio"
	"cuefs/internal/plan"
)

// Options carries what WriteTrack needs; both `split` and the FUSE overlay
// funnel their synthesis requests through it so that a cache entry written by
// one is byte-identical to what the other would produce.
type Options struct {
	FfmpegBin        string // optional: FLAC/APE audio movement only
	CompressionLevel int
	Extension        string // output extension, ".flac" or ".wav"
}

func (o *Options) defaults() {
	if o.Extension = plan.NormaliseExtension(o.Extension); o.Extension == "" {
		o.Extension = ".flac"
	}
	if o.CompressionLevel <= 0 {
		o.CompressionLevel = 5
	}
}

// IsPureGo reports whether `split`ting src into o.Extension needs no external
// binary. WAV to WAV is a byte copy; everything else moves compressed audio
// and needs ffmpeg.
func IsPureGo(srcPath string, o Options) bool {
	o.defaults()
	return isExt(srcPath, ".wav") && isExt(o.Extension, ".wav")
}

func isExt(path, want string) bool {
	return strings.EqualFold(filepath.Ext(path), want)
}

// WriteTrack produces one track file (audio + metadata) at out. The file is
// written through a .part file and renamed, so an interrupted run leaves the
// library clean. Callers own the cache directory's mtime policy afterwards.
func WriteTrack(src string, rate int, t *plan.Track, out string, o Options) error {
	o.defaults()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(out), err)
	}
	switch {
	case IsPureGo(src, o):
		return writeWav(src, t, out)
	case isExt(out, ".flac"):
		return writeFlac(src, rate, t, out, o)
	default:
		return fmt.Errorf("unsupported output %q for source %q (flac needs ffmpeg; wav needs a wav source)",
			o.Extension, src)
	}
}

// writeWav cuts a WAV source by sample index: a byte copy, no decoder.
func writeWav(src string, t *plan.Track, out string) error {
	w, err := audio.OpenWav(src)
	if err != nil {
		return err
	}
	return w.Split(out, t.Start, t.End)
}

// writeFlac moves the audio with ffmpeg and then authors the metadata in Go,
// which is how multi-value Vorbis Comments stay intact (DESIGN §4.3.1).
func writeFlac(src string, rate int, t *plan.Track, out string, o Options) error {
	r := Runner{FfmpegBin: o.FfmpegBin, CompressionLevel: o.CompressionLevel}
	if r.FfmpegBin == "" {
		return fmt.Errorf("ffmpeg_bin is not configured: splitting %s into FLAC needs it, or a prepared cache entry (see cuefs split)",
			filepath.Base(src))
	}
	if err := r.Split(src, rate, t, out); err != nil {
		return err
	}
	if err := audio.WriteVorbisComment(out, Entries(t.Tags)); err != nil {
		// The audio is fine; report but keep the file.
		return fmt.Errorf("audio written but metadata rewrite failed: %w", err)
	}
	return nil
}

// Entries converts the plan's tag list into Vorbis Comment entries (one per
// value for the multi-value keys). Shared by the writer and by verify's tag
// comparison so that both sides agree byte for byte.
func Entries(tags []plan.Tag) []audio.VorbisEntry {
	expanded := plan.Expand(tags)
	out := make([]audio.VorbisEntry, 0, len(expanded))
	for _, t := range expanded {
		out = append(out, audio.VorbisEntry{Key: t.Key, Value: t.Value})
	}
	return out
}
