package cue

import (
	"fmt"
	"sort"
	"strings"
)

// File types accepted in FILE statements (CDRWin spec).
const (
	FileWave     = "WAVE"
	FileMP3      = "MP3"
	FileAIFF     = "AIFF"
	FileBinary   = "BINARY"
	FileMotorola = "MOTOROLA"
)

// TrackType is the second argument of a TRACK statement. Only "AUDIO" tracks
// are splittable; everything else is CD-ROM data and must be ignored.
type TrackType string

// IsAudio reports whether the track carries audio.
func (t TrackType) IsAudio() bool {
	return t == "AUDIO" || t == ""
}

// String renders the track type.
func (t TrackType) String() string {
	if t == "" {
		return "AUDIO"
	}
	return string(t)
}

// File groups the tracks that live in one audio file. A cue sheet with
// several FILE entries describes a multi-disc release.
type File struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Tracks        []*Track `json:"tracks,omitempty"`
	Duration      Time     `json:"duration,omitempty"`
	DurationKnown bool     `json:"duration_known"`
}

// Track is one TRACK entry plus its resolved time span.
type Track struct {
	Num         int          `json:"num"`
	Type        TrackType    `json:"type"`
	Flags       []string     `json:"flags,omitempty"`
	Isrc        string       `json:"isrc,omitempty"`
	Performers  []string     `json:"performers,omitempty"`
	Songwriters []string     `json:"songwriters,omitempty"`
	Title       string       `json:"title,omitempty"`
	Index       map[int]Time `json:"index,omitempty"`
	Pregap      *Time        `json:"pregap,omitempty"`
	Postgap     *Time        `json:"postgap,omitempty"`

	// Resolved by (*Sheet).Resolve.
	Start      Time `json:"start"`
	End        Time `json:"end"`
	StartKnown bool `json:"start_known"`
	EndKnown   bool `json:"end_known"`
}

// StartIndex returns the timestamp of INDEX 01, falling back to INDEX 00
// (which means the track's audio begins with its pregap).
func (t *Track) StartIndex() (Time, bool) {
	if v, ok := t.Index[1]; ok {
		return v, true
	}
	if v, ok := t.Index[0]; ok {
		return v, true
	}
	return Time{}, false
}

// Duration returns the resolved track length; zero when unresolved.
func (t *Track) Duration() Time {
	if !t.StartKnown || !t.EndKnown {
		return Time{}
	}
	return t.End.Sub(t.Start)
}

// Sheet is a parsed cue file.
type Sheet struct {
	Catalog     string            `json:"catalog,omitempty"`
	Performers  []string          `json:"performers,omitempty"`
	Title       string            `json:"title,omitempty"`
	Songwriters []string          `json:"songwriters,omitempty"`
	Rem         map[string]string `json:"rem,omitempty"`

	Files    []*File  `json:"files"`
	Tracks   []*Track `json:"tracks"` // flattened, in file order
	Warnings []string `json:"warnings,omitempty"`

	// parseWarnings holds diagnostics produced while parsing, so that Resolve
	// can be re-run (e.g. once the audio duration becomes known) without
	// duplicating them.
	parseWarnings []string
	// baseWarnings holds diagnostics from encoding detection.
	baseWarnings []string

	// Encoding is filled in by Load/ParseBytes.
	Encoding *DetectResult `json:"encoding,omitempty"`
	// SourcePath is filled in by Load.
	SourcePath string `json:"source_path,omitempty"`
}

func (s *Sheet) warnf(format string, args ...any) {
	s.parseWarnings = append(s.parseWarnings, fmt.Sprintf(format, args...))
}

// Resolve validates ordering and fills Start/End for every audio track.
// fileEnd supplies the duration of each FILE; when it is unknown the last
// track of that file is reported as unresolved instead of guessed.
// It may be called repeatedly; parse warnings are never duplicated.
func (s *Sheet) Resolve(fileEnd func(*File) (Time, bool)) {
	s.Warnings = append(append([]string(nil), s.baseWarnings...), s.parseWarnings...)
	warn := func(format string, args ...any) {
		s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...))
	}
	for _, f := range s.Files {
		for i, t := range f.Tracks {
			if !t.Type.IsAudio() {
				continue
			}
			if st, ok := t.StartIndex(); ok {
				t.Start, t.StartKnown = st, true
			} else {
				warn("track %02d has no INDEX", t.Num)
			}

			switch {
			case i+1 < len(f.Tracks):
				next := f.Tracks[i+1]
				if st, ok := next.StartIndex(); ok {
					t.End, t.EndKnown = st, true
				} else {
					warn("track %02d: next track has no INDEX; end unknown", t.Num)
				}
			default:
				if fileEnd != nil {
					if d, ok := fileEnd(f); ok {
						t.End, t.EndKnown = d, true
						f.Duration, f.DurationKnown = d, true
					}
				}
				if !t.EndKnown {
					warn("track %02d is the last track and the audio duration is unknown", t.Num)
				}
			}

			if t.StartKnown && t.EndKnown && t.End.Less(t.Start) {
				warn("track %02d ends before it starts (%s < %s)", t.Num, t.End, t.Start)
			}
		}
	}

	// Cross-check: audio before track 1's INDEX 01 is not part of any track.
	if len(s.Tracks) > 0 {
		first := s.Tracks[0]
		if idx0, has0 := first.Index[0]; has0 && !idx0.IsZero() {
			warn("track %02d has INDEX 00 at %s: audio before INDEX 01 is not part of any track (HTOA)",
				first.Num, idx0)
		}
	}
}

// IsZero reports whether the time is exactly 00:00:00.
func (t Time) IsZero() bool { return t.MM == 0 && t.SS == 0 && t.FF == 0 }

// AudioTracks returns the splittable tracks in playback order.
func (s *Sheet) AudioTracks() []*Track {
	out := make([]*Track, 0, len(s.Tracks))
	for _, t := range s.Tracks {
		if t.Type.IsAudio() {
			out = append(out, t)
		}
	}
	return out
}

// Summary returns a one-line human description of the sheet.
func (s *Sheet) Summary() string {
	audio := s.AudioTracks()
	discs := len(s.Files)
	return fmt.Sprintf("%d disc(s), %d track(s) (%d audio)", discs, len(s.Tracks), len(audio))
}

// splitMulti splits "A; B" style multi-values the way trackfs does, because
// most rippers use ";" as an in-line delimiter.
func splitMulti(v string) []string {
	parts := strings.Split(v, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{strings.TrimSpace(v)}
	}
	return out
}

// AlbumTags maps cue-sheet album-level fields onto the tag names Navidrome
// understands (DESIGN §2, after trackfs' proven rules).
func (s *Sheet) AlbumTags() map[string][]string {
	tags := map[string][]string{}
	add := func(k string, vs ...string) {
		for _, v := range vs {
			if v = strings.TrimSpace(v); v != "" {
				tags[k] = append(tags[k], v)
			}
		}
	}
	add("ALBUMARTIST", s.Performers...)
	add("ALBUM", s.Title)
	add("COMPOSER", s.Songwriters...)
	add("CATALOGNUMBER", s.Catalog)
	for _, key := range []string{"DATE", "GENRE", "DISCNUMBER", "TOTALDISCS", "COMMENT"} {
		if v, ok := s.Rem[key]; ok {
			for _, part := range splitMulti(v) {
				add(key, part)
			}
		}
	}
	return tags
}

// TrackTags merges the album tags with the per-track cue fields, letting the
// track override the album (the rule trackfs uses and Navidrome expects).
func (s *Sheet) TrackTags(t *Track) map[string][]string {
	tags := map[string][]string{}
	for k, vs := range s.AlbumTags() {
		tags[k] = append(tags[k], vs...)
	}
	if len(t.Performers) > 0 {
		tags["ARTIST"] = append([]string(nil), t.Performers...)
	}
	if len(t.Songwriters) > 0 {
		tags["COMPOSER"] = append([]string(nil), t.Songwriters...)
	}
	if t.Title != "" {
		tags["TITLE"] = []string{t.Title}
	}
	if t.Isrc != "" {
		tags["ISRC"] = []string{t.Isrc}
	}
	if _, ok := tags["TRACKNUMBER"]; !ok {
		tags["TRACKNUMBER"] = []string{fmt.Sprintf("%02d", t.Num)}
	}
	if _, ok := tags["TRACKTOTAL"]; !ok {
		tags["TRACKTOTAL"] = []string{fmt.Sprintf("%02d", len(s.AudioTracks()))}
	}
	return tags
}

// SortedTagNames returns tag names in a stable order for display.
func SortedTagNames(tags map[string][]string) []string {
	out := make([]string, 0, len(tags))
	for k := range tags {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
