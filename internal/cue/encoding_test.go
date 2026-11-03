package cue

import (
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

// encodeAll renders text in the given encoding, so tests can synthesise cue
// files in every supported character set without committing binary fixtures.
func encodeAll(e encoding.Encoding, s string) ([]byte, error) {
	return io.ReadAll(transform.NewReader(strings.NewReader(s), e.NewEncoder()))
}

func TestParseTime(t *testing.T) {
	got, err := ParseTime("03:15:20")
	if err != nil {
		t.Fatalf("ParseTime: %v", err)
	}
	want := Time{MM: 3, SS: 15, FF: 20}
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
	if got.Frames() != 3*4500+15*75+20 {
		t.Fatalf("Frames() = %d", got.Frames())
	}
	if got.String() != "03:15:20" {
		t.Fatalf("String() = %q", got.String())
	}

	for _, bad := range []string{"", "1:2", "00:60:00", "00:00:75", "aa:bb:cc", "::"} {
		if _, err := ParseTime(bad); err == nil {
			t.Errorf("ParseTime(%q) should fail", bad)
		}
	}
	// Whitespace tolerance: real cue files are full of it.
	if _, err := ParseTime(" 03 : 15 : 20 "); err != nil {
		t.Errorf("whitespace-tolerant ParseTime failed: %v", err)
	}
}

func TestTimeArithmetic(t *testing.T) {
	a := Time{MM: 1, SS: 59, FF: 74}
	b := a.Add(Time{FF: 1})
	if b != (Time{MM: 2, SS: 0, FF: 0}) {
		t.Fatalf("carry failed: %v", b)
	}
	if b.Sub(a) != (Time{FF: 1}) {
		t.Fatalf("Sub failed: %v", b.Sub(a))
	}
	// Clamped at zero, never negative.
	if z := (Time{FF: 1}).Sub(Time{FF: 5}); !z.IsZero() {
		t.Fatalf("Sub should clamp to zero, got %v", z)
	}
}

// TestSamplesExact pins the MSF -> sample conversion (DESIGN §4.1). The
// intermediate arithmetic must be integer-only: a float implementation loses
// samples and shifts track boundaries.
func TestSamplesExact(t *testing.T) {
	cases := []struct {
		t    Time
		rate int
		want int64
	}{
		{Time{}, 44100, 0},
		{Time{SS: 1}, 44100, 44100},
		{Time{FF: 1}, 44100, 588},    // 44100/75
		{Time{FF: 74}, 44100, 43512}, // 74*588
		{Time{MM: 3, SS: 15, FF: 20}, 44100, 8611260},
		{Time{FF: 1}, 48000, 640}, // 48000/75 is exact
		{Time{FF: 1}, 32000, 427}, // 32000/75 = 426.666.. -> rounded
		{Time{FF: 2}, 32000, 853}, // 853.333.. -> rounded
		{Time{MM: 80, SS: 0, FF: 0}, 192000, 921600000},
	}
	for _, c := range cases {
		got, err := Samples(c.t, c.rate)
		if err != nil {
			t.Fatalf("Samples(%v, %d): %v", c.t, c.rate, err)
		}
		if got != c.want {
			t.Errorf("Samples(%v, %d) = %d, want %d", c.t, c.rate, got, c.want)
		}
	}

	// Round-trip: converting a sample count back to a Time and forward again
	// must be stable for frame-aligned values.
	for _, f := range []int64{0, 1, 74, 75, 4500, 1234567} {
		ts := FromFrames(f)
		back := ts.Frames()
		if back != f {
			t.Fatalf("FromFrames(%d).Frames() = %d", f, back)
		}
	}

	if _, err := Samples(Time{}, 0); err == nil {
		t.Error("Samples with rate 0 must fail")
	}
}

const simplifiedCue = `REM DISCID A10A2E0D
PERFORMER "测试乐队"
TITLE "测试专辑"
CATALOG 5054196339524
REM DATE 2014
REM GENRE 摇滚乐
REM DISCNUMBER 1
REM TOTALDISCS 1
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    PERFORMER "测试乐队"
    TITLE "第一首歌"
    ISRC CN12345678901
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    PERFORMER "测试乐队; 客串歌手"
    TITLE "第二首歌"
    INDEX 01 03:15:20
`

const traditionalCue = `REM DISCID B20A2E0D
PERFORMER "測試樂隊"
TITLE "測試專輯"
CATALOG 5054196339524
REM DATE 2014
REM GENRE 搖滾樂
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    PERFORMER "測試樂隊"
    TITLE "第一首曲目"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    PERFORMER "測試樂隊; 客串歌手"
    TITLE "第二首曲目"
    INDEX 01 03:15:20
`

const japaneseCue = `REM DISCID C30A2E0D
PERFORMER "テストバンド"
TITLE "テストアルバム"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "一曲目"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "二曲目"
    INDEX 01 04:12:33
`

func encode(t *testing.T, name, text string) []byte {
	t.Helper()
	e, ok := registry[name]
	if !ok {
		t.Fatalf("unknown encoding %q", name)
	}
	out, err := encodeAll(e, text)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	return out
}

func TestDetectEncodings(t *testing.T) {
	cases := []struct {
		name string // expected canonical name
		text string
		raw  func(*testing.T) []byte
	}{
		{"utf-8", simplifiedCue, func(t *testing.T) []byte { return []byte(simplifiedCue) }},
		{"utf-8", simplifiedCue, func(t *testing.T) []byte {
			return append([]byte{0xEF, 0xBB, 0xBF}, []byte(simplifiedCue)...)
		}},
		{"gb18030", simplifiedCue, func(t *testing.T) []byte { return encode(t, "gb18030", simplifiedCue) }},
		{"gbk", simplifiedCue, func(t *testing.T) []byte { return encode(t, "gbk", simplifiedCue) }},
		{"big5", traditionalCue, func(t *testing.T) []byte { return encode(t, "big5", traditionalCue) }},
		{"shift_jis", japaneseCue, func(t *testing.T) []byte { return encode(t, "shift_jis", japaneseCue) }},
		{"utf-16le", simplifiedCue, func(t *testing.T) []byte {
			return append([]byte{0xFF, 0xFE}, encode(t, "utf-16le", simplifiedCue)...)
		}},
		{"utf-16le", simplifiedCue, func(t *testing.T) []byte {
			return encode(t, "utf-16le", simplifiedCue) // no BOM: sniffed
		}},
		{"utf-16be", simplifiedCue, func(t *testing.T) []byte {
			return encode(t, "utf-16be", simplifiedCue) // no BOM: sniffed
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := c.raw(t)
			res, err := Detect(b, DetectOptions{})
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if !sameEncoding(res.Encoding, c.name) {
				t.Fatalf("detected %q (origin=%s), want %q\ncandidates: %+v\nwarnings: %v",
					res.Encoding, res.Origin, c.name, res.Candidates, res.Warnings)
			}
			// Detection must also produce text that parses.
			s, err := ParseBytes(b, DetectOptions{})
			if err != nil {
				t.Fatalf("ParseBytes: %v", err)
			}
			if len(s.Tracks) != 2 {
				t.Fatalf("parsed %d tracks, want 2", len(s.Tracks))
			}
		})
	}
}

// TestDetectForcedAndList covers decision D11: a forced encoding and an
// explicitly ordered candidate list.
func TestDetectForcedAndList(t *testing.T) {
	gbk := encode(t, "gbk", simplifiedCue)

	// Forced: always wins, even when it is the "wrong" answer.
	res, err := Detect(gbk, DetectOptions{Force: "utf-8"})
	if err != nil {
		t.Fatalf("Detect forced: %v", err)
	}
	if res.Encoding != "utf-8" || res.Origin != "forced" {
		t.Fatalf("forced detection = %s/%s", res.Encoding, res.Origin)
	}

	// Unknown forced name is an error, not a silent fallback.
	if _, err := Detect(gbk, DetectOptions{Force: "klingon"}); err == nil {
		t.Fatal("unknown forced encoding must fail")
	}

	// Configured list is tried in order and the first valid entry wins.
	res, err = Detect(gbk, DetectOptions{List: []string{"iso-8859-1", "gbk"}})
	if err != nil {
		t.Fatalf("Detect list: %v", err)
	}
	if res.Encoding != "iso-8859-1" || res.Origin != "configured" {
		t.Fatalf("configured list = %s/%s, want iso-8859-1/configured", res.Encoding, res.Origin)
	}

	// A list whose entries all fail must fall back to the built-in order.
	res, err = Detect(encode(t, "shift_jis", japaneseCue), DetectOptions{List: []string{"utf-16be"}})
	if err != nil {
		t.Fatalf("Detect list fallback: %v", err)
	}
	if res.Origin != "builtin" {
		t.Fatalf("origin = %s, want builtin", res.Origin)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("falling back from a configured list must be reported")
	}
	if !sameEncoding(res.Encoding, "shift_jis") {
		t.Fatalf("fallback picked %q, want shift_jis (%+v)", res.Encoding, res.Candidates)
	}
}

func TestDetectReportsCandidates(t *testing.T) {
	res, err := Detect(encode(t, "gb18030", simplifiedCue), DetectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) < 2 {
		t.Fatalf("probe needs the full candidate table, got %d entries", len(res.Candidates))
	}
	seen := map[string]bool{}
	for _, c := range res.Candidates {
		seen[c.Name] = true
	}
	for _, want := range []string{"gb18030", "big5", "windows-1252"} {
		if !seen[want] {
			t.Errorf("candidate %q missing from report", want)
		}
	}
}

func TestDecodeWarnsOnChardetDisagreement(t *testing.T) {
	// Pure-ASCII cue: every candidate decodes fine, so detection is trivial,
	// but the report must still be well formed.
	ascii := "FILE \"a.flac\" WAVE\n  TRACK 01 AUDIO\n    TITLE \"x\"\n    INDEX 01 00:00:00\n"
	text, res, err := Decode([]byte(ascii), DetectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Encoding != "utf-8" {
		t.Fatalf("ascii cue detected as %q", res.Encoding)
	}
	if text != ascii {
		t.Fatalf("round trip changed the text:\n%q", text)
	}
}

func TestCanonicalName(t *testing.T) {
	cases := map[string]string{
		"UTF-8": "utf-8", "gb2312": "gbk", "CP936": "gbk",
		"Shift_JIS": "shift_jis", "sjis": "shift_jis",
		"latin1": "iso-8859-1", "Big5": "big5", "nope": "",
	}
	for in, want := range cases {
		got, ok := CanonicalName(in)
		if want == "" {
			if ok {
				t.Errorf("CanonicalName(%q) = %q, want failure", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("CanonicalName(%q) = %q/%v, want %q", in, got, ok, want)
		}
	}
}

func TestBuiltinOrderIsExposed(t *testing.T) {
	if len(BuiltinOrder()) == 0 {
		t.Fatal("BuiltinOrder must not be empty")
	}
	if !strings.Contains(strings.Join(KnownEncodings(), ","), "gb18030") {
		t.Fatal("gb18030 must be a known encoding")
	}
}
