package plan

import (
	"fmt"
	"regexp"
	"strings"

	"cuefs/internal/cue"
)

// Tag is one metadata entry of a generated track file.
type Tag struct {
	Key   string
	Value string
}

const TRACKTOTAL = "TRACKTOTAL"

// tagOrder fixes the layout of the Vorbis comment stream so two cuefs runs
// over the same input produce identical tags. Anything outside this list is
// appended alphabetically.
var tagOrder = []string{
	"TRACKNUMBER", TRACKTOTAL, "TITLE", "ARTIST", "ALBUM", "ALBUMARTIST",
	"COMPOSER", "DATE", "GENRE", "DISCNUMBER", "TOTALDISCS", "ISRC",
	"CATALOGNUMBER", "COMMENT",
}

// MemberSep is how a single key carrying several cue values is serialised.
// The ffmpeg writer records one value per key, so a track that lists two
// performers becomes 'ARTIST=A; B'. FLAC's Vorbis Comment can hold several
// values per key, and plan.Expand / audio.WriteVorbisComment use that.
const memberSep = "; "

// IgnoreTagsRe builds the matcher for cue.ignore_tags, whose entries are glob
// patterns matched case-insensitively against the tag name.
func IgnoreTagsRe(ignore []string) (*regexp.Regexp, error) {
	if len(ignore) == 0 {
		return nil, nil
	}
	parts := make([]string, 0, len(ignore))
	for _, p := range ignore {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, globToRegexp(p))
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	r, err := regexp.Compile("(?i)^(?:" + strings.Join(parts, "|") + ")$")
	if err != nil {
		return nil, fmt.Errorf("cue.ignore_tags: %w", err)
	}
	return r, nil
}

// globToRegexp turns a tag glob such as "CUE_TRACK*" into a regexp fragment.
func globToRegexp(g string) string {
	var b strings.Builder
	for _, r := range g {
		switch r {
		case '*':
			b.WriteString(".*")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

// baseExcluded are the keys the plan derives itself or that are technical
// noise for a track file. An album file's own title is the ALBUM's name and
// its track numbering describes the album as a whole; neither may leak into
// a track's tags. CUESHEET is libFLAC's embedded copy of the cue sheet -
// kilobytes of text, never useful per track.
var baseExcluded = map[string]bool{
	"TITLE": true, "TRACKNUMBER": true, "TRACKTOTAL": true, "TOTALTRACKS": true,
	"CUESHEET": true,
}

// albumTagMap is the unfiltered album-level tag set: the source audio
// file's tags as the base (DESIGN §2, the trackfs rule that is tuned for
// Navidrome), then the cue sheet's album-level fields overriding per key.
//
// A cue sheet with only FILE/TRACK/INDEX lines therefore still produces
// complete tags whenever the album file carries them, and vice versa: a rich
// cue fills an album file that was ripped without tags.
func albumTagMap(sheet *cue.Sheet, base map[string][]string) map[string][]string {
	t := map[string][]string{}
	for k, vs := range base {
		if baseExcluded[strings.ToUpper(k)] {
			continue
		}
		for _, v := range vs {
			if v = strings.TrimSpace(v); v != "" {
				t[k] = append(t[k], v)
			}
		}
	}

	// cue-level fields, replacing whatever the file carried for that key.
	cueTags := map[string][]string{}
	add := func(k, v string) {
		if v = collapseWS(v); v != "" {
			cueTags[k] = append(cueTags[k], v)
		}
	}
	add("ALBUM", sheet.Title)
	add("ALBUMARTIST", join(sheet.Performers...))
	add("ARTIST", join(sheet.Performers...))
	add("COMPOSER", join(sheet.Songwriters...))
	add("CATALOGNUMBER", sheet.Catalog)

	// REM key=value pairs; anything unrecognised is dropped, not guessed.
	// A ";" inside a value stays part of it here: filterTags re-joins with
	// "; " and Expand re-splits the multi-value keys into real comments.
	for key, val := range sheet.Rem {
		switch strings.ToUpper(key) {
		case "DATE", "GENRE", "DISCNUMBER", "TOTALDISCS", "COMMENT":
			add(strings.ToUpper(key), val)
		}
	}
	for k, vs := range cueTags {
		t[k] = vs
	}
	return t
}

// AlbumTags collects the album-level tags every track inherits: the source
// file's tags as the base, overridden by the cue sheet per key (DESIGN §2).
// Real cue sheets carry plenty of padding ("她说   CD2"), which we normalise
// on the way in rather than copying verbatim into Navidrome's database.
func AlbumTags(sheet *cue.Sheet, base map[string][]string, ignore *regexp.Regexp) []Tag {
	return filterTags(albumTagMap(sheet, base), ignore)
}

// TrackTags merges the album tags with the track's own fields; the track
// overrides TITLE/ARTIST/COMPOSER, which is precisely the rule trackfs uses
// and what Navidrome's scanner behaves best with.
func TrackTags(sheet *cue.Sheet, tr *cue.Track, base map[string][]string, ignore *regexp.Regexp) []Tag {
	tags := albumTagMap(sheet, base)

	if tr.Num > 0 {
		tags["TRACKNUMBER"] = []string{fmt.Sprintf("%02d", tr.Num)}
	}
	tags[TRACKTOTAL] = []string{fmt.Sprintf("%02d", len(sheet.AudioTracks()))}
	if tr.Title != "" {
		tags["TITLE"] = []string{collapseWS(tr.Title)}
	}
	if len(tr.Performers) > 0 {
		tags["ARTIST"] = []string{join(tr.Performers...)}
	}
	if len(tr.Songwriters) > 0 {
		tags["COMPOSER"] = []string{join(tr.Songwriters...)}
	}
	if tr.Isrc != "" {
		tags["ISRC"] = []string{strings.TrimSpace(tr.Isrc)}
	}
	return filterTags(tags, ignore)
}

// filterTags flattens the tag map into the ordered, deduplicated form.
func filterTags(t map[string][]string, ignore *regexp.Regexp) []Tag {
	extra := make([]string, 0, len(t))
	for k := range t {
		if !contains(tagOrder, k) {
			extra = append(extra, k)
		}
	}
	sortStrings(extra)

	out := make([]Tag, 0, len(t))
	for _, k := range append(append([]string(nil), tagOrder...), extra...) {
		vals := t[k]
		if len(vals) == 0 {
			continue
		}
		if ignore != nil && ignore.MatchString(k) {
			continue
		}
		joined := strings.Join(nonEmpty(vals), memberSep)
		if joined == "" {
			continue
		}
		out = append(out, Tag{Key: k, Value: joined})
	}
	return out
}

// MultiValueKeys are the keys where a cue sheet's "A; B" convention refers to
// several values rather than to a value containing a semicolon. Only those
// are split into multiple Vorbis Comment entries.
var MultiValueKeys = map[string]bool{
	"ARTIST": true, "ALBUMARTIST": true, "GENRE": true, "COMPOSER": true,
	"PERFORMER": true, "CONDUCTOR": true, "LYRICIST": true, "MOOD": true,
}

// Expand turns each multi-value tag into one entry per value, so that a FLAC
// writer can produce real multi-value comments instead of "A; B" strings.
func Expand(tags []Tag) []Tag {
	out := make([]Tag, 0, len(tags))
	for _, tag := range tags {
		if !MultiValueKeys[tag.Key] {
			out = append(out, tag)
			continue
		}
		for _, v := range strings.Split(tag.Value, memberSep) {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, Tag{Key: tag.Key, Value: v})
			}
		}
	}
	return out
}

func nonEmpty(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if c := collapseWS(v); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// join renders a cue field that carries several values into a single string.
func join(vs ...string) string { return strings.Join(nonEmpty(vs), memberSep) }

// collapseWS trims and squeezes runs of whitespace down to one space.
func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
