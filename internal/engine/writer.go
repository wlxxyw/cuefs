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

func isExt(path, want string) bool {
	return strings.EqualFold(filepath.Ext(path), want)
}

// Facts are the source file's stream facts, needed to place sample indices
// and to author the output header.
type Facts struct {
	SampleRate int
	Channels   int
	Bits       int
}

// WriteTrack produces one track file (audio + metadata) at out. The file is
// written through a .part file and renamed, so an interrupted run leaves the
// library clean. Callers own the cache directory's mtime policy afterwards.
func WriteTrack(src string, facts Facts, t *plan.Track, out string, o Options) error {
	o.defaults()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(out), err)
	}
	switch {
	case isExt(src, ".flac") && isExt(out, ".flac"):
		return writeFlac(src, facts, t, out, o)
	case isExt(src, ".wav") && isExt(out, ".wav"):
		return writeWav(src, t, out)
	case isExt(src, ".ape") && isExt(out, ".flac"):
		// APE → FLAC is the one path that always needs ffmpeg (DESIGN
		// D-series: ffmpeg is demoted to APE-only). WAV sources also fall
		// back here when the output is FLAC, since a RIFF passthrough
		// cannot author FLAC metadata.
		if o.FfmpegBin == "" {
			return fmt.Errorf("ape/wav → flac needs ffmpeg_bin (only flac→flac and wav→wav are pure Go)")
		}
		r := Runner{FfmpegBin: o.FfmpegBin, CompressionLevel: o.CompressionLevel}
		if err := r.Split(src, facts.SampleRate, t, out); err != nil {
			return err
		}
		if err := audio.WriteVorbisComment(out, Entries(t.Tags)); err != nil {
			// The audio is fine; report but keep the file.
			return fmt.Errorf("audio written but metadata rewrite failed: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported output %q for source %q (only flac→flac, wav→wav and ape→flac are supported)",
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

// writeFlac cuts a FLAC source by sample index entirely in Go (DESIGN
// §4.3.1): frames fully inside the range are copied byte-for-byte and the
// two boundary frames are decoded and re-encoded as VERBATIM subframes. This
// is what removes ffmpeg from the FLAC path; it stays for APE only.
//
// If the pure-Go path cannot handle the file (an exotic subframe type, a
// damaged frame chain) and ffmpeg_bin is configured, it is used as a
// fallback, which keeps odd files splittable.
func writeFlac(src string, facts Facts, t *plan.Track, out string, o Options) error {
	err := audio.WriteFLACTrack(audio.SplitSpec{
		Src:        src,
		Start:      t.Start,
		End:        t.End,
		SampleRate: facts.SampleRate,
		Channels:   facts.Channels,
		Bits:       facts.Bits,
		Tags:       Entries(t.Tags),
	}, out)
	if err == nil {
		return nil
	}
	if o.FfmpegBin == "" {
		return err
	}
	r := Runner{FfmpegBin: o.FfmpegBin, CompressionLevel: o.CompressionLevel}
	if ferr := r.Split(src, facts.SampleRate, t, out); ferr != nil {
		return fmt.Errorf("pure-go split failed (%v) and ffmpeg fallback failed: %w", err, ferr)
	}
	if terr := audio.WriteVorbisComment(out, Entries(t.Tags)); terr != nil {
		// The audio is fine; report but keep the file.
		return fmt.Errorf("audio written but metadata rewrite failed: %w", terr)
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
