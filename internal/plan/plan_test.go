package plan

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"cuefs/internal/audio"
	"cuefs/internal/cue"
	"cuefs/internal/library"
)

func parseTagCue() (*cue.Sheet, error) {
	return cue.ParseText(tagCue)
}

func TestTrackFileName(t *testing.T) {
	got := TrackFileName("{num:02d} - {title}{ext}", 1, "第一首歌", 60, ".flac", map[string]int{})
	if got != "01 - 第一首歌.flac" {
		t.Fatalf("got %q, want %q", got, "01 - 第一首歌.flac")
	}
}

func TestTemplateNameForms(t *testing.T) {
	cases := []struct {
		template string
		num      int
		title    string
		want     string
	}{
		{"{num:02d} - {title}{ext}", 3, "Song", "03 - Song.flac"},
		{"{num:3}{ext}", 7, "Song", "007.flac"},
		{"{num}{ext}", 12, "x", "12.flac"},
		{"{title}{ext}", 4, "no number", "no number.flac"},
	}
	for _, c := range cases {
		got := TrackFileName(c.template, c.num, c.title, 60, ".flac", map[string]int{})
		if got != c.want {
			t.Errorf("template %q num=%d title=%q -> %q, want %q",
				c.template, c.num, c.title, got, c.want)
		}
	}
	// A track with no TITLE in the cue gets no TITLE tag; Navidrome falls
	// back to the track number, which stays honest about the data.
	sheet, _ := parseTagCue()
	if v := get(TrackTags(sheet, sheet.Tracks[1], nil, nil), "TITLE"); v != "" {
		t.Errorf("track 02 TITLE = %q, want empty (fixture has none)", v)
	}
	if v := get(TrackTags(sheet, sheet.Tracks[0], nil, nil), "TITLE"); v != "I Am" {
		t.Errorf("track 01 TITLE = %q, want \"I Am\"", v)
	}
}

func TestSanitizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Simple Title", "Simple Title"},
		{"你说 世界", "你说 世界"},
		{"реж.im", "реж.im"},
		{"  spaced   out  ", "spaced out"},
		{"bad:chars/in*it?", "bad_chars_in_it_"},
		{"tab\tsep", "tab sep"},
		{`traitor "quoted" <name>`, "traitor _quoted_ _name_"},
		{"trailing. ", "trailing"},
		{"...dots...", "dots"},
	}
	for _, c := range cases {
		if got := SanitizeTitle(c.in, 200); got != c.want {
			t.Errorf("SanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Bounded by runes, not bytes: long CJK titles keep whole characters.
	long := strings.Repeat("很", 200)
	if got := SanitizeTitle(long, 60); len([]rune(got)) != 60 {
		t.Errorf("length after clamp = %d, want 60", len([]rune(got)))
	}
}

func TestFileNameDedupe(t *testing.T) {
	taken := map[string]int{}
	_ = TrackFileName("{num:02d} - {title}{ext}", 1, "Same", 60, ".flac", taken)
	b := TrackFileName("{num:02d} - {title}{ext}", 1, "Same", 60, ".flac", taken)
	if b != "01 - Same (2).flac" {
		t.Fatalf("second name = %q", b)
	}
}

const tagCue = `TITLE "她说   CD2"
PERFORMER "林俊杰"
REM DATE 2010
REM GENRE POP
REM DISCID ABC
FILE "x.flac" WAVE
  TRACK 01 AUDIO
    TITLE "I Am"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    PERFORMER "林俊杰; 另一位"
    SONGWRITER "词曲作者"
    ISRC TW1234567890
    INDEX 01 04:52:36
`

func get(tags []Tag, key string) string {
	for _, t := range tags {
		if t.Key == key {
			return t.Value
		}
	}
	return ""
}

func has(tags []Tag, key string) bool {
	return get(tags, key) != ""
}

func TestTagRules(t *testing.T) {
	sheet, err := parseTagCue()
	if err != nil {
		t.Fatal(err)
	}
	album := AlbumTags(sheet, nil, nil)
	want := map[string]string{
		"ALBUM":       "她说 CD2", // padding collapsed
		"ALBUMARTIST": "林俊杰",
		"ARTIST":      "林俊杰",
		"DATE":        "2010",
		"GENRE":       "POP",
	}
	for k, want := range want {
		if got := get(album, k); got != want {
			t.Errorf("album %s = %q, want %q", k, got, want)
		}
	}
	if get(album, "DISCID") != "" {
		t.Error("unknown REM keys must be dropped, not guessed")
	}
	if get(album, "CATALOGNUMBER") != "" {
		t.Errorf("CATALOGNUMBER = %q, want empty (no CATALOG in the fixture)", get(album, "CATALOGNUMBER"))
	}

	// Track 02 overrides artist and adds composer/isrc/number.
	tr := sheet.Tracks[1]
	tags := TrackTags(sheet, tr, nil, nil)
	for k, want := range map[string]string{
		"TRACKNUMBER": "02",
		TRACKTOTAL:    "02",
		"ARTIST":      "林俊杰; 另一位",
		"COMPOSER":    "词曲作者",
		"ISRC":        "TW1234567890",
		"DATE":        "2010",
	} {
		if got := get(tags, k); got != want {
			t.Errorf("track %s = %q, want %q", k, got, want)
		}
	}
	if !has(tags, "ALBUM") || !has(tags, "ALBUMARTIST") {
		t.Error("album tags must be inherited onto tracks")
	}
}

// TestSourceTagBase checks the DESIGN §2 merge: the source audio file's tags
// are the base, the cue sheet overrides per key, and keys only the file has
// survive into the split tracks.
func TestSourceTagBase(t *testing.T) {
	sheet, err := parseTagCue()
	if err != nil {
		t.Fatal(err)
	}
	base := map[string][]string{
		"ALBUM":       {"源里的旧专辑名"},
		"GENRE":       {"摇滚"},
		"DATE":        {"2001"},
		"ARTIST":      {"源里的艺术家"},
		"TITLE":       {"源文件的标题"}, // the album file's title: must never leak
		"TRACKNUMBER": {"1"},      // the album file's numbering: never leaks
		"TRACKTOTAL":  {"12"},     // neither does its total
		"LABEL":       {"某厂牌"},    // only the file knows this one
	}

	album := AlbumTags(sheet, base, nil)
	for k, want := range map[string]string{
		"ALBUM":       "她说 CD2", // cue overrides the file
		"GENRE":       "POP",    // cue REM overrides the file
		"DATE":        "2010",   // ditto
		"ARTIST":      "林俊杰",    // cue PERFORMER overrides the file
		"ALBUMARTIST": "林俊杰",
		"LABEL":       "某厂牌", // kept: the cue is silent about it
	} {
		if got := get(album, k); got != want {
			t.Errorf("album %s = %q, want %q", k, got, want)
		}
	}

	// Track 01 has a cue TITLE, track 02 does not: neither may fall back to
	// the album file's TITLE, and neither may inherit its track numbering.
	if v := get(TrackTags(sheet, sheet.Tracks[0], base, nil), "TITLE"); v != "I Am" {
		t.Errorf("track 01 TITLE = %q, want \"I Am\"", v)
	}
	if v := get(TrackTags(sheet, sheet.Tracks[1], base, nil), "TITLE"); v != "" {
		t.Errorf("track 02 TITLE = %q, want empty (source TITLE must not leak)", v)
	}
	for i, tr := range sheet.Tracks {
		tags := TrackTags(sheet, tr, base, nil)
		if v := get(tags, "TRACKNUMBER"); v != fmt.Sprintf("%02d", i+1) {
			t.Errorf("track %d TRACKNUMBER = %q, want the cue's number", i+1, v)
		}
		if v := get(tags, TRACKTOTAL); v != "02" {
			t.Errorf("track %d TRACKTOTAL = %q, want 02 (cue tracks), not 12", i+1, v)
		}
	}

	if has(album, "CUESHEET") {
		t.Error("the embedded CUESHEET must never leak into track tags")
	}

	// The ignore filter applies to inherited base keys too.
	re, err := IgnoreTagsRe([]string{"LABEL"})
	if err != nil {
		t.Fatal(err)
	}
	if has(AlbumTags(sheet, base, re), "LABEL") {
		t.Error("ignore list must suppress inherited base keys")
	}
}

func TestIgnoreTags(t *testing.T) {
	sheet, err := parseTagCue()
	if err != nil {
		t.Fatal(err)
	}
	re, err := IgnoreTagsRe([]string{"CUE_TRACK*", "COMMENT"})
	if err != nil {
		t.Fatal(err)
	}
	tags := TrackTags(sheet, sheet.Tracks[0], nil, re)
	if has(tags, "COMMENT") {
		t.Error("ignore list must suppress COMMENT")
	}
	if !has(tags, "TITLE") {
		t.Error("ignore list must not suppress TITLE")
	}
	// The default tag order keeps output byte-stable across runs.
	first := TrackTags(sheet, sheet.Tracks[0], nil, nil)
	second := TrackTags(sheet, sheet.Tracks[0], nil, nil)
	if len(first) != len(second) {
		t.Fatalf("tag list is not stable")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("tag %d is not stable: %+v vs %+v", i, first[i], second[i])
		}
	}
	if !strings.HasPrefix(first[0].Key, "TRACKNUMBER") {
		t.Errorf("expected TRACKNUMBER to come first, got %+v", first[0])
	}
}

func TestIgnoreTagGlobs(t *testing.T) {
	sheet, err := parseTagCue()
	if err != nil {
		t.Fatal(err)
	}
	// A bare "*" glob suppresses every tag key.
	re, err := IgnoreTagsRe([]string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if tags := TrackTags(sheet, sheet.Tracks[0], nil, re); len(tags) != 0 {
		t.Errorf("* must suppress everything, got %+v", tags)
	}
	// An escaped glob is still valid input.
	if _, err := IgnoreTagsRe([]string{"(balanced)"}); err != nil {
		t.Errorf("harmless glob rejected: %v", err)
	}
}

func TestBuildFollowsSourceExtension(t *testing.T) {
	sheet, err := parseTagCue()
	if err != nil {
		t.Fatal(err)
	}
	f := &audio.Format{Codec: "wav", SampleRate: 44100, Channels: 2, BitsPerSample: 16}
	cases := []struct {
		src, apeExt, wantExt string
	}{
		{"x.flac", "", ".flac"},
		{"x.wav", "", ".wav"},
		{"x.WAV", "", ".wav"},
		// Naming follows the source for any listed format; whether the
		// engine can actually cut it is a separate gate (writer support).
		{"x.m4a", "", ".m4a"},
		{"x.ape", ".wav", ".wav"},
		{"x.ape", "", ".flac"},
	}
	for _, c := range cases {
		opts := Options{SourceRoot: "lib", OutputRoot: "out", NameTemplate: "{num:02d}{ext}"}
		if c.apeExt != "" {
			opts.ApeExt = c.apeExt
		}
		album, err := Build(&library.Entry{
			CuePath:    filepath.Join("lib", "album.cue"),
			SourcePath: filepath.Join("lib", c.src),
			Kind:       library.KindSplit,
			Sheet:      sheet,
		}, f, opts)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		for _, tr := range album.Tracks {
			if got := strings.ToLower(filepath.Ext(tr.Name)); got != c.wantExt {
				t.Errorf("%s: track name %q does not end in %q", c.src, tr.Name, c.wantExt)
			}
		}
	}
}
