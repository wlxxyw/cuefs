// Package plan turns a parsed cue sheet plus the source file's stream facts
// into the concrete list of generated track files (DESIGN §4.4).
package plan

import (
	"fmt"
	"path/filepath"
	"strings"

	"cuefs/internal/audio"
	"cuefs/internal/cue"
	"cuefs/internal/library"
)

// Track is one file to be produced.
type Track struct {
	Num   int
	Title string
	Name  string // output file name, without the directory
	Start int64  // first sample (inclusive)
	End   int64  // last sample (exclusive); <0 means "to end of source"
	Tags  []Tag
}

// Album is everything that must be produced for one cue file.
type Album struct {
	CuePath    string
	SourcePath string
	CueRelDir  string // directory of the cue, relative to the source root
	OutDir     string

	Codec      string
	SampleRate int
	Channels   int
	Bits       int
	HasVideo   bool // embedded cover art is available

	Tracks []*Track

	Extractor bool // no image next to it, so the plan extracts one
	Warnings  []string
}

// Options parameterises planning.
type Options struct {
	SourceRoot   string
	OutputRoot   string
	Extension    string
	NameTemplate string
	MaxTitleLen  int
	IgnoreTags   []string
}

func (o *Options) defaults() {
	if o.Extension == "" {
		o.Extension = ".flac"
	} else if !strings.HasPrefix(o.Extension, ".") {
		o.Extension = "." + o.Extension
	}
	if o.NameTemplate == "" {
		o.NameTemplate = "{num:02d} - {title}{ext}"
	}
}

// Build assembles the plan for one classified cue entry. The audio probe is
// required; a source that could not be probed is an error, not a guess.
func Build(e *library.Entry, f *audio.Format, opts Options) (*Album, error) {
	opts.defaults()
	if e.Sheet == nil {
		return nil, fmt.Errorf("cue %s was never parsed", e.CuePath)
	}
	if f == nil {
		return nil, fmt.Errorf("probe missing for %s", e.SourcePath)
	}

	rel, err := filepath.Rel(opts.SourceRoot, e.CuePath)
	if err != nil {
		return nil, fmt.Errorf("cue %s is outside %s: %w", e.CuePath, opts.SourceRoot, err)
	}
	relDir := filepath.Dir(filepath.ToSlash(rel))
	ignore, err := IgnoreTagsRe(opts.IgnoreTags)
	if err != nil {
		return nil, err
	}

	album := &Album{
		CuePath:    e.CuePath,
		SourcePath: e.SourcePath,
		OutDir:     filepath.ToSlash(filepath.Join(opts.OutputRoot, relDir)),
		CueRelDir:  relDir,
		Codec:      f.Codec,
		SampleRate: f.SampleRate,
		Channels:   f.Channels,
		Bits:       f.BitsPerSample,
		HasVideo:   f.HasVideo,
		Extractor:  f.HasVideo,
		Warnings:   append([]string(nil), e.Warnings...),
	}

	taken := map[string]int{}
	for _, tr := range e.Sheet.AudioTracks() {
		skip := func(reason string) {
			album.Warnings = append(album.Warnings,
				fmt.Sprintf("track %02d %s; skipped", tr.Num, reason))
		}
		if !tr.StartKnown {
			skip("has no usable start index")
			continue
		}
		start, err := cue.Samples(tr.Start, f.SampleRate)
		if err != nil {
			skip(err.Error())
			continue
		}
		end := int64(-1)
		if tr.EndKnown {
			end, err = cue.Samples(tr.End, f.SampleRate)
			if err != nil {
				skip(err.Error())
				continue
			}
			if end <= start {
				skip(fmt.Sprintf("is empty (%s < %s)", tr.End, tr.Start))
				continue
			}
		}

		name := TrackFileName(opts.NameTemplate, tr.Num, tr.Title,
			opts.MaxTitleLen, opts.Extension, taken)
		album.Tracks = append(album.Tracks, &Track{
			Num:   tr.Num,
			Title: tr.Title,
			Name:  name,
			Start: start,
			End:   end,
			Tags:  TrackTags(e.Sheet, tr, ignore),
		})
	}

	if len(album.Tracks) == 0 {
		return nil, fmt.Errorf("no usable tracks parsed from %s", e.CuePath)
	}
	return album, nil
}

// Summary is a one-line human description of a planned album.
func (a *Album) Summary() string {
	return fmt.Sprintf("%d tracks, %s/%d Hz/%d ch/%d bit", len(a.Tracks),
		a.Codec, a.SampleRate, a.Channels, a.Bits)
}

// Warn appends a warning to the album's report.
func (a *Album) Warn(w string) { a.Warnings = append(a.Warnings, w) }

// AtrimSpec returns the ffmpeg input seek offset and the atrim filter string
// for this track (DESIGN §4.3; the combo that was verified sample-exact).
func (t *Track) AtrimSpec(rate int) (offset string, filter string) {
	whole := t.Start / int64(rate)
	rem := t.Start - whole*int64(rate)
	f := fmt.Sprintf("atrim=start_sample=%d", rem)
	if t.End > 0 {
		f = fmt.Sprintf("%s:end_sample=%d", f, t.End-whole*int64(rate))
	}
	return fmt.Sprintf("%.20g", float64(whole)), f + ",asetpts=PTS-STARTPTS"
}

// CueBase is the cue file's stem, for logs.
func (a *Album) CueBase() string {
	base := filepath.Base(a.CuePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
