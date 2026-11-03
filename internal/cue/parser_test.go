package cue

import (
	"strings"
	"testing"
)

const fullCue = `REM DISCID A10A2E0D
REM DATE 2014
REM GENRE Soundtrack
REM COMMENT "CUERipper v2.1.4"
CATALOG 5054196339524
PERFORMER "Zaz"
TITLE "Paris"
SONGWRITER "Zaz"
CDTEXTFILE "booklet.txt"
FILE "Zaz - Paris.flac" WAVE
  TRACK 01 AUDIO
    PERFORMER "Zaz"
    TITLE "Paris sera toujours Paris"
    ISRC FR2PY1403200
    FLAGS DCP
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    PERFORMER "Zaz; Zazo"
    TITLE "Sous le ciel de Paris"
    ISRC FR2PY1403250
    PREGAP 00:00:02
    INDEX 00 02:58:00
    INDEX 01 02:58:20
  TRACK 03 AUDIO
    PERFORMER "Zaz"
    TITLE "Trait de union"
    INDEX 01 10:00:00
`

func TestParseFull(t *testing.T) {
	s, err := ParseText(fullCue)
	if err != nil {
		t.Fatalf("ParseText: %v", err)
	}

	if s.Title != "Paris" {
		t.Errorf("album title = %q", s.Title)
	}
	if len(s.Performers) != 1 || s.Performers[0] != "Zaz" {
		t.Errorf("performers = %v", s.Performers)
	}
	if s.Catalog != "5054196339524" {
		t.Errorf("catalog = %q", s.Catalog)
	}
	if s.Rem["DATE"] != "2014" || s.Rem["GENRE"] != "Soundtrack" {
		t.Errorf("rem = %v", s.Rem)
	}
	if len(s.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(s.Files))
	}
	f := s.Files[0]
	if f.Name != "Zaz - Paris.flac" || f.Type != FileWave {
		t.Errorf("file = %q/%q", f.Name, f.Type)
	}
	if len(f.Tracks) != 3 || len(s.Tracks) != 3 {
		t.Fatalf("tracks = %d/%d, want 3", len(f.Tracks), len(s.Tracks))
	}

	tr := s.Tracks[0]
	if tr.Num != 1 || tr.Title != "Paris sera toujours Paris" {
		t.Errorf("track 1 = %+v", tr)
	}
	if tr.Isrc != "FR2PY1403200" {
		t.Errorf("isrc = %q", tr.Isrc)
	}
	if len(tr.Flags) != 1 || tr.Flags[0] != "DCP" {
		t.Errorf("flags = %v", tr.Flags)
	}
	if !tr.StartKnown || tr.Start != (Time{}) {
		t.Errorf("track 1 start = %v (known=%v)", tr.Start, tr.StartKnown)
	}

	// Track 2 declares both INDEX 00 and INDEX 01: start must be INDEX 01,
	// and the pregap must be recorded separately.
	tr2 := s.Tracks[1]
	if tr2.Start != (Time{MM: 2, SS: 58, FF: 20}) {
		t.Errorf("track 2 start = %v, want 02:58:20 (INDEX 01)", tr2.Start)
	}
	if tr2.Index[0] != (Time{MM: 2, SS: 58}) {
		t.Errorf("track 2 INDEX 00 = %v", tr2.Index[0])
	}
	// PREGAP is mm:ss:ff too, so 00:00:02 is two CD frames, not two seconds.
	if tr2.Pregap == nil || *tr2.Pregap != (Time{FF: 2}) {
		t.Errorf("track 2 pregap = %v", tr2.Pregap)
	}
	// End of track 2 comes from track 3's INDEX 01.
	if !tr2.EndKnown || tr2.End != (Time{MM: 10}) {
		t.Errorf("track 2 end = %v (known=%v), want 00:10:00", tr2.End, tr2.EndKnown)
	}

	// The last track has no known audio duration yet.
	if s.Tracks[2].EndKnown {
		t.Error("last track end must be unknown without the audio duration")
	}
	if !hasWarning(s, "last track") {
		t.Errorf("missing 'last track' warning, got %v", s.Warnings)
	}
}

func TestResolveWithFileDuration(t *testing.T) {
	s, err := ParseText(fullCue)
	if err != nil {
		t.Fatal(err)
	}
	end := Time{MM: 42, SS: 10, FF: 33}
	s.Resolve(func(f *File) (Time, bool) { return end, true })

	last := s.Tracks[2]
	if !last.EndKnown || last.End != end {
		t.Fatalf("last end = %v (known=%v)", last.End, last.EndKnown)
	}
	if got := last.Duration(); got != end.Sub(last.Start) {
		t.Fatalf("last duration = %v", got)
	}
	// Re-resolving must not duplicate warnings.
	n := len(s.Warnings)
	s.Resolve(func(f *File) (Time, bool) { return end, true })
	if len(s.Warnings) != n {
		t.Fatalf("Resolve duplicated warnings: %d -> %d", n, len(s.Warnings))
	}
	if hasWarning(s, "last track") {
		t.Fatal("warning should be gone once the duration is known")
	}
}

func TestMultipleFilesAndDataTracks(t *testing.T) {
	const twoDisc = `TITLE "Box Set"
FILE "CD1.flac" WAVE
  TRACK 01 AUDIO
    TITLE "One"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Two"
    INDEX 01 05:00:00
FILE "CD2.flac" WAVE
  TRACK 01 AUDIO
    TITLE "Three"
    INDEX 01 00:00:00
  TRACK 02 MODE1/2352
    INDEX 01 08:00:00
`
	s, err := ParseText(twoDisc)
	if err != nil {
		t.Fatalf("ParseText: %v", err)
	}
	if len(s.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(s.Files))
	}
	if s.Files[0].Name != "CD1.flac" || s.Files[1].Name != "CD2.flac" {
		t.Errorf("file names = %q / %q", s.Files[0].Name, s.Files[1].Name)
	}
	if len(s.Tracks) != 4 {
		t.Fatalf("tracks = %d, want 4", len(s.Tracks))
	}
	audio := s.AudioTracks()
	if len(audio) != 3 {
		t.Fatalf("audio tracks = %d, want 3 (data track excluded)", len(audio))
	}
	if s.Tracks[3].Type.IsAudio() {
		t.Error("MODE1/2352 must be treated as a data track")
	}
	// The second disc restarts at 00:00:00; the first disc's last track ends
	// at the disc duration, not at the second disc's first INDEX.
	if s.Tracks[1].End != (Time{}) || s.Tracks[1].EndKnown {
		t.Errorf("disc 1 last track end = %v (known=%v)", s.Tracks[1].End, s.Tracks[1].EndKnown)
	}
	if !hasWarning(s, "last track") {
		t.Error("expected an unresolved-end warning for disc 1")
	}
}

func TestQuotedFilenames(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{`FILE "My Album.flac" WAVE`, "My Album.flac"},
		{`FILE My Album.flac WAVE`, "My Album.flac"},
		{`FILE album.mp3 MP3`, "album.mp3"},
		{`FILE "with space and-dash.wav" AIFF`, "with space and-dash.wav"},
	}
	for _, c := range cases {
		s, err := ParseText(c.line + "\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n")
		if err != nil {
			t.Fatalf("%s: %v", c.line, err)
		}
		if s.Files[0].Name != c.want {
			t.Errorf("%s -> %q, want %q", c.line, s.Files[0].Name, c.want)
		}
	}
}

func TestMalformedInputIsTolerated(t *testing.T) {
	const bad = `TITLE "x"
FILE "a.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
  TRACK bogus AUDIO
    INDEX 01 00:10:00
  TRACK 03 AUDIO
    INDEX 01 99:99:99
  TRACK 04 AUDIO
    INDEX 01 not-a-time
  TRACK 05 AUDIO
    INDEX 01 00:20:00
UNKNOWNSTUFF foo
`
	s, err := ParseText(bad)
	if err != nil {
		t.Fatalf("a malformed cue must not be fatal: %v", err)
	}
	// TRACK bogus and the two bad INDEX lines are dropped/skipped but the
	// sheet must still be usable.
	if len(s.Tracks) != 4 {
		t.Fatalf("tracks = %d, want 4", len(s.Tracks))
	}
	if len(s.Warnings) == 0 {
		t.Fatal("malformed input must produce warnings")
	}
	if !hasWarning(s, "unsupported statement") {
		t.Errorf("expected an unsupported-statement warning, got %v", s.Warnings)
	}
}

func TestParseTextWithoutTracksFails(t *testing.T) {
	if _, err := ParseText("TITLE \"nothing here\"\n"); err == nil {
		t.Fatal("a cue without TRACK must fail")
	}
}

func TestTagRules(t *testing.T) {
	s, err := ParseText(fullCue)
	if err != nil {
		t.Fatal(err)
	}
	album := s.AlbumTags()
	if got := album["ALBUM"]; len(got) != 1 || got[0] != "Paris" {
		t.Errorf("ALBUM = %v", got)
	}
	if got := album["ALBUMARTIST"]; len(got) != 1 || got[0] != "Zaz" {
		t.Errorf("ALBUMARTIST = %v", got)
	}
	if got := album["DATE"]; len(got) != 1 || got[0] != "2014" {
		t.Errorf("DATE = %v", got)
	}
	if got := album["CATALOGNUMBER"]; len(got) != 1 || got[0] != "5054196339524" {
		t.Errorf("CATALOGNUMBER = %v", got)
	}

	tr := s.Tracks[1]
	tags := s.TrackTags(tr)
	if got := tags["TITLE"]; len(got) != 1 || got[0] != "Sous le ciel de Paris" {
		t.Errorf("track TITLE = %v", got)
	}
	// "Zaz; Zazo" must split into two ARTIST values (trackfs rule).
	if got := tags["ARTIST"]; len(got) != 2 || got[0] != "Zaz" || got[1] != "Zazo" {
		t.Errorf("track ARTIST = %v", got)
	}
	if got := tags["TRACKNUMBER"]; len(got) != 1 || got[0] != "02" {
		t.Errorf("TRACKNUMBER = %v", got)
	}
	if got := tags["TRACKTOTAL"]; len(got) != 1 || got[0] != "03" {
		t.Errorf("TRACKTOTAL = %v", got)
	}
	if got := tags["ISRC"]; len(got) != 1 || got[0] != "FR2PY1403250" {
		t.Errorf("ISRC = %v", got)
	}
	// Album-level COMPOSER comes from SONGWRITER and survives on tracks.
	if got := tags["COMPOSER"]; len(got) != 1 || got[0] != "Zaz" {
		t.Errorf("COMPOSER = %v", got)
	}
}

func TestParseBytesSetsEncoding(t *testing.T) {
	gbk := encode(t, "gbk", simplifiedCue)
	s, err := ParseBytes(gbk, DetectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Encoding == nil || !sameEncoding(s.Encoding.Encoding, "gb18030") {
		t.Fatalf("encoding = %+v", s.Encoding)
	}
	if s.Tracks[0].Title != "第一首歌" {
		t.Fatalf("title not decoded: %q", s.Tracks[0].Title)
	}
	if s.Title != "测试专辑" {
		t.Fatalf("album not decoded: %q", s.Title)
	}
}

func hasWarning(s *Sheet, substr string) bool {
	for _, w := range s.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
