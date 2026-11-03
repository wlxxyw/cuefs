// Package cue parses CDRWin-style cue sheets and detects their character
// encoding (DESIGN §4.1, §4.2, decision D11).
package cue

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/saintfish/chardet"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Encoding registry. Canonical names are what appear in configuration and in
// probe output; aliases let users write whatever their tooling uses.
var registry = map[string]encoding.Encoding{
	"utf-8":        unicode.UTF8,
	"utf-16le":     unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM),
	"utf-16be":     unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM),
	"gb18030":      simplifiedchinese.GB18030,
	"gbk":          simplifiedchinese.GBK,
	"big5":         traditionalchinese.Big5,
	"shift_jis":    japanese.ShiftJIS,
	"euc-jp":       japanese.EUCJP,
	"euc-kr":       korean.EUCKR,
	"windows-1252": charmap.Windows1252,
	"windows-1251": charmap.Windows1251,
	"iso-8859-1":   charmap.ISO8859_1,
}

var aliases = map[string]string{
	"ascii":     "utf-8",
	"utf8":      "utf-8",
	"utf16":     "utf-16le",
	"utf16le":   "utf-16le",
	"utf16be":   "utf-16be",
	"gb2312":    "gbk",
	"gb-18030":  "gb18030",
	"cp936":     "gbk",
	"gbk2312":   "gbk",
	"big5hkscs": "big5",
	"cp950":     "big5",
	"sjis":      "shift_jis",
	"cp932":     "shift_jis",
	"shift-jis": "shift_jis",
	"eucjp":     "euc-jp",
	"euckr":     "euc-kr",
	"cp949":     "euc-kr",
	"latin1":    "iso-8859-1",
	"latin-1":   "iso-8859-1",
	"iso88591":  "iso-8859-1",
	"cp1252":    "windows-1252",
	"cp1251":    "windows-1251",
	"win-1252":  "windows-1252",
	"win-1251":  "windows-1251",
	"ansi":      "windows-1252",
}

// builtinOrder is tried when cue.encoding_list is empty or every configured
// candidate fails (DESIGN §4.2 step 4). Chinese-language libraries dominate
// in practice, so GB18030 leads; it is a strict superset of GBK/GB2312.
var builtinOrder = []string{
	"gb18030", "big5", "shift_jis", "euc-jp", "euc-kr", "windows-1252", "windows-1251",
}

// BuiltinOrder returns a copy of the built-in candidate order. Exposed for
// `cuefs probe`.
func BuiltinOrder() []string { return append([]string(nil), builtinOrder...) }

// KnownEncodings returns the sorted list of canonical encoding names.
func KnownEncodings() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CanonicalName resolves an encoding name or alias to its canonical form.
func CanonicalName(name string) (string, bool) {
	n := normalizeName(name)
	if _, ok := registry[n]; ok {
		return n, true
	}
	if c, ok := aliases[n]; ok {
		return c, true
	}
	return "", false
}

func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("_", "-", " ", "").Replace(s)
	return strings.ReplaceAll(s, "_", "-")
}

// Encode renders text in the named encoding. It is the inverse of Decode and
// exists so tests and fixture generators can produce cue files in every
// supported character set without committing binary blobs.
func Encode(name, text string) ([]byte, error) {
	n, ok := CanonicalName(name)
	if !ok {
		return nil, fmt.Errorf("unknown encoding %q", name)
	}
	return io.ReadAll(transform.NewReader(strings.NewReader(text), registry[n].NewEncoder()))
}

// DetectOptions controls encoding detection (DESIGN §4.2).
type DetectOptions struct {
	// Force, when non-empty, skips detection entirely.
	Force string
	// List is the ordered candidate list from cue.encoding_list.
	List []string
}

// Candidate records the outcome for one encoding that was evaluated.
type Candidate struct {
	Name   string  `json:"name"`
	Origin string  `json:"origin"` // configured | builtin | extra
	Valid  bool    `json:"valid"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason,omitempty"`
}

// DetectResult describes the outcome of detection, including every candidate
// that was evaluated so that `cuefs probe` can show its work.
type DetectResult struct {
	Encoding   string      `json:"encoding"`
	Origin     string      `json:"origin"` // bom | utf8 | utf16 | forced | configured | builtin | fallback
	Confidence float64     `json:"confidence"`
	Candidates []Candidate `json:"candidates,omitempty"`
	Warnings   []string    `json:"warnings,omitempty"`
}

// Detect picks an encoding for the raw cue bytes without decoding them.
func Detect(b []byte, opts DetectOptions) (DetectResult, error) {
	if opts.Force != "" && !strings.EqualFold(opts.Force, "auto") {
		name, ok := CanonicalName(opts.Force)
		if !ok {
			return DetectResult{}, fmt.Errorf("unknown encoding %q (known: %s)",
				opts.Force, strings.Join(KnownEncodings(), ", "))
		}
		res := DetectResult{Encoding: name, Origin: "forced"}
		s, err := decodeWith(registry[name], b)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("forced encoding %q failed to decode cleanly: %v", name, err))
		}
		sc := evaluate(name, b, s)
		if !sc.valid {
			res.Warnings = append(res.Warnings, fmt.Sprintf("forced encoding %q: %s", name, sc.reason))
		}
		res.Confidence = clampConfidence(sc.score)
		return res, nil
	}

	// Step 1: byte-order marks.
	if name, rest, ok := stripBOM(b); ok {
		res := DetectResult{Encoding: name, Origin: "bom"}
		s, err := decodeWith(registry[name], rest)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("BOM says %q but decoding failed: %v", name, err))
		}
		sc := evaluate(name, b, s)
		if !sc.valid {
			res.Warnings = append(res.Warnings, "content does not validate under BOM encoding: "+sc.reason)
		}
		res.Confidence = clampConfidence(sc.score)
		return res, nil
	}

	// Step 2: strict UTF-8.
	if utf8.Valid(b) {
		res := DetectResult{Encoding: "utf-8", Origin: "utf8"}
		sc := evaluate("utf-8", b, string(b))
		if !sc.valid {
			res.Warnings = append(res.Warnings, "valid UTF-8 but content check failed: "+sc.reason)
		}
		res.Confidence = clampConfidence(sc.score)
		return res, nil
	}

	// Step 2.5: BOM-less UTF-16 (heuristic on NUL placement).
	if name, ok := sniffUTF16(b); ok {
		res := DetectResult{Encoding: name, Origin: "utf16"}
		s, err := decodeWith(registry[name], b)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("UTF-16 sniff says %q but decoding failed: %v", name, err))
		}
		sc := evaluate(name, b, s)
		if sc.valid {
			res.Confidence = clampConfidence(sc.score)
			return res, nil
		}
		res.Warnings = append(res.Warnings, "UTF-16 sniff rejected: "+sc.reason)
		// fall through to ordered candidates
	}

	// Steps 3+4: configured order first, then the built-in order. Within the
	// configured list the first candidate that validates wins outright - the
	// user asserted the order, so we do not second-guess it. The built-in
	// order gets one refinement: GBK and Big5 both "decode successfully"
	// from each other's bytes, so when chardet is confident we let it break
	// the tie (DESIGN §4.2).
	var evaluated []Candidate
	seen := map[string]bool{}
	evalOne := func(raw, origin string) (Candidate, bool) {
		name, ok := CanonicalName(raw)
		if !ok || seen[name] {
			return Candidate{}, false
		}
		seen[name] = true
		s, err := decodeWith(registry[name], b)
		sc := evaluate(name, b, s)
		if err != nil {
			sc = score{valid: false, reason: "decode error: " + err.Error()}
		}
		c := Candidate{
			Name: name, Origin: origin, Valid: sc.valid,
			Score: sc.score, Reason: sc.reason,
		}
		evaluated = append(evaluated, c)
		return c, true
	}
	runOrder := func(names []string, origin string) *Candidate {
		var first *Candidate
		for _, raw := range names {
			c, ok := evalOne(raw, origin)
			if !ok || !c.Valid {
				continue
			}
			if first == nil {
				cp := c
				first = &cp
			}
		}
		return first
	}

	var warnings []string
	configuredFailed := false
	if len(opts.List) > 0 {
		if first := runOrder(opts.List, "configured"); first != nil {
			return DetectResult{
				Encoding: first.Name, Origin: "configured",
				Confidence: clampConfidence(first.Score),
				Candidates: evaluated,
			}, nil
		}
		configuredFailed = true
	}

	if first := runOrder(builtinOrder, "builtin"); first != nil {
		if configuredFailed {
			warnings = append(warnings,
				"every encoding in cue.encoding_list failed validation; fell back to the built-in order")
		}
		// Tie-break with chardet. The built-in order alone is correct for
		// GBK/Big5 (each fails to validate under the other) but picks GB18030
		// for Shift-JIS and EUC-JP text, which also validate as garbage.
		// chardet gets to override the order only when it is very confident
		// (>=60) about one candidate while the order's own pick scores low
		// (<=40) - that keeps GBK on the right answer even though chardet's
		// EUC-KR recognizer over-fires on Chinese text (measured 88 vs 58).
		if ch := chardetAll(b); len(ch) > 0 {
			// Make sure anything chardet mentions is part of the report.
			for raw := range ch {
				if _, ok := CanonicalName(raw); ok {
					evalOne(raw, "extra")
				}
			}
			bestConf := -1
			var pick *Candidate
			for i := range evaluated {
				c := &evaluated[i]
				if !c.Valid {
					continue
				}
				if conf, ok := ch[c.Name]; ok && conf > bestConf {
					bestConf, pick = conf, c
				}
			}
			orderConf := ch[first.Name] // 0 when chardet has no opinion
			if pick != nil && pick.Name != first.Name && bestConf >= 60 && orderConf <= 40 {
				warnings = append(warnings,
					fmt.Sprintf("chardet is confident (%d) about %q while the built-in order chose %q; used chardet's pick",
						bestConf, pick.Name, first.Name))
				first = pick
			} else if pick != nil && pick.Name != first.Name && bestConf >= 60 {
				warnings = append(warnings,
					fmt.Sprintf("chardet prefers %q (conf %d) but the built-in order chose %q (conf %d); kept the order pick",
						pick.Name, bestConf, first.Name, orderConf))
			}
		}
		return DetectResult{
			Encoding: first.Name, Origin: "builtin",
			Confidence: clampConfidence(first.Score),
			Candidates: evaluated,
			Warnings:   warnings,
		}, nil
	}

	// Step 5: score-based fallback over everything we have not tried yet.
	for _, name := range KnownEncodings() {
		if seen[name] {
			continue
		}
		s, err := decodeWith(registry[name], b)
		sc := evaluate(name, b, s)
		if err != nil {
			sc = score{valid: false, reason: "decode error: " + err.Error()}
		}
		evaluated = append(evaluated, Candidate{
			Name: name, Origin: "extra", Valid: sc.valid,
			Score: sc.score, Reason: sc.reason,
		})
	}
	res := DetectResult{Candidates: evaluated, Origin: "fallback", Warnings: warnings}
	sort.SliceStable(res.Candidates, func(i, j int) bool {
		return res.Candidates[i].Score > res.Candidates[j].Score
	})
	best := res.Candidates[0]
	res.Encoding = best.Name
	res.Confidence = clampConfidence(best.Score)
	if best.Valid {
		res.Warnings = append(res.Warnings, fmt.Sprintf("no candidate passed validation in order; picked best score %q", best.Name))
	} else {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"no candidate validated; using best-effort %q (%s) - the cue file may be corrupt",
			best.Name, best.Reason))
	}
	return res, nil
}

// Decode detects the encoding (or honours Force/List) and returns the decoded
// cue text together with the detection report.
func Decode(b []byte, opts DetectOptions) (string, DetectResult, error) {
	res, err := Detect(b, opts)
	if err != nil {
		return "", res, err
	}
	name := res.Encoding
	if res.Origin == "bom" || res.Origin == "utf8" {
		// stripBOM already removed the leading marker for BOM; for plain
		// UTF-8 there is nothing to strip.
		if _, rest, ok := stripBOM(b); ok {
			b = rest
		}
	}
	s, derr := decodeWith(registry[name], b)
	if derr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("decoding as %q: %v", name, derr))
	}
	return s, res, nil
}

// --- internals -------------------------------------------------------------

func decodeWith(e encoding.Encoding, b []byte) (string, error) {
	r := transform.NewReader(bytes.NewReader(b), e.NewDecoder())
	data, err := io.ReadAll(r)
	return string(data), err
}

func stripBOM(b []byte) (string, []byte, bool) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return "utf-8", b[3:], true
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return "utf-16le", b[2:], true
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return "utf-16be", b[2:], true
	}
	return "", b, false
}

// sniffUTF16 detects BOM-less UTF-16 by the regularity of NUL bytes. A cue
// sheet written in UTF-16 has ASCII keywords interleaved with NULs, so one
// byte position of each pair is overwhelmingly zero.
func sniffUTF16(b []byte) (string, bool) {
	if len(b) < 8 {
		return "", false
	}
	n := len(b) / 2
	oddZero, evenZero := 0, 0
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 {
			evenZero++
		}
		if b[i+1] == 0 {
			oddZero++
		}
	}
	switch {
	case oddZero*4 >= n*3 && evenZero*8 <= n:
		return "utf-16le", true
	case evenZero*4 >= n*3 && oddZero*8 <= n:
		return "utf-16be", true
	}
	return "", false
}

type score struct {
	valid  bool
	score  float64
	reason string
}

// cueKeywords are the structural markers used both for validation and as a
// scoring signal. They are ASCII, so they survive any of our decodings.
var cueKeywords = []string{"TRACK", "INDEX", "FILE", "REM", "CATALOG", "PERFORMER", "TITLE", "SONGWRITER", "PREGAP", "POSTGAP", "FLAGS", "ISRC"}

// structuralKeywords must appear for the text to count as a cue sheet at all.
var structuralKeywords = []string{"TRACK", "INDEX", "FILE"}

// evaluate decides whether decoded text s is a plausible cue sheet and how
// strongly it supports that conclusion (DESIGN §4.2).
func evaluate(name string, src []byte, s string) score {
	if s == "" {
		return score{valid: false, reason: "empty output"}
	}
	if strings.ContainsRune(s, '�') {
		return score{valid: false, reason: "contains replacement characters (U+FFFD)"}
	}
	if strings.ContainsRune(s, 0) {
		return score{valid: false, reason: "contains NUL bytes"}
	}

	// Control characters other than tab/newline/CR indicate a wrong decoding.
	ctrl, total := 0, 0
	for _, r := range s {
		total++
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			ctrl++
		}
	}
	if total == 0 {
		return score{valid: false, reason: "no characters decoded"}
	}
	if float64(ctrl)/float64(total) > 0.02 {
		return score{valid: false, reason: fmt.Sprintf("control character ratio %.1f%% too high", 100*float64(ctrl)/float64(total))}
	}

	srcHigh := false
	for _, c := range src {
		if c >= 0x80 {
			srcHigh = true
			break
		}
	}
	hasNonASCII := false
	for _, r := range s {
		if r >= 0x80 {
			hasNonASCII = true
			break
		}
	}
	if srcHigh && !hasNonASCII {
		return score{valid: false, reason: "source has high bytes but decoding produced pure ASCII"}
	}

	upper := strings.ToUpper(s)
	structural := 0
	for _, k := range structuralKeywords {
		if strings.Contains(upper, k+" ") || strings.Contains(upper, k+"\t") {
			structural++
		}
	}
	if structural == 0 {
		return score{valid: false, reason: "no cue keywords (TRACK/INDEX/FILE) found"}
	}

	sc := score{valid: true}
	for _, k := range cueKeywords {
		if strings.Contains(upper, k) {
			sc.score += 10
		}
	}
	sc.score += float64(structural) * 5
	if srcHigh {
		// Reward decoders that produced a healthy amount of non-ASCII text
		// rather than a single stray character.
		nonASCII := 0
		for _, r := range s {
			if r >= 0x80 {
				nonASCII++
			}
		}
		sc.score += float64(min(nonASCII, 400)) / 40.0
	} else {
		sc.score += 20 // pure-ASCII cue: every candidate ties, keep it simple
	}

	// Penalise single-byte Western code pages when the source looks like
	// damaged UTF-8 (lead bytes C2/C3 followed by continuation bytes): a
	// mojibake decode must not win over a nearly-correct one.
	switch name {
	case "windows-1252", "iso-8859-1", "windows-1251":
		if looksLikeBrokenUTF8(src) {
			sc.score -= 60
		}
	}
	return sc
}

func looksLikeBrokenUTF8(b []byte) bool {
	// Count C2/C3 lead bytes followed by a UTF-8 continuation byte. A file
	// that is mostly valid UTF-8 with a couple of bad bytes produces a high
	// ratio and must not be reinterpreted as Latin-1.
	lead, pair := 0, 0
	for i := 0; i+1 < len(b); i++ {
		if b[i] == 0xC2 || b[i] == 0xC3 {
			lead++
			if b[i+1]&0xC0 == 0x80 {
				pair++
			}
		}
	}
	return lead > 0 && pair*4 >= lead*3
}

// sameEncoding reports whether two encoding names refer to the same family.
// chardet reports "GB2312" where our registry calls the same thing "gbk",
// and gb18030 is a strict superset of both, so they are treated as equal.
func sameEncoding(a, b string) bool {
	if a == b {
		return true
	}
	for _, group := range [][]string{{"gb18030", "gbk"}} {
		inA, inB := false, false
		for _, n := range group {
			inA = inA || n == a
			inB = inB || n == b
		}
		if inA && inB {
			return true
		}
	}
	return false
}

// chardetAll runs chardet and maps its suggestions onto our canonical names,
// keyed by name with the highest confidence seen for each. Returns nil when
// there is not enough data to say anything useful.
func chardetAll(b []byte) map[string]int {
	if len(b) < 16 {
		return nil
	}
	rs, err := chardet.NewTextDetector().DetectAll(b)
	if err != nil || len(rs) == 0 {
		return nil
	}
	out := map[string]int{}
	for _, r := range rs {
		n, ok := CanonicalName(r.Charset)
		if !ok {
			continue
		}
		if r.Confidence > out[n] {
			out[n] = r.Confidence
		}
	}
	return out
}

// chardetSuggest returns chardet's single best guess, for advisory output.
func chardetSuggest(b []byte) (string, bool) {
	if len(b) < 16 {
		return "", false
	}
	det, err := chardet.NewTextDetector().DetectBest(b)
	if err != nil || det == nil || det.Charset == "" {
		return "", false
	}
	n, ok := CanonicalName(det.Charset)
	return n, ok
}

// maxScore is the theoretical ceiling of evaluate()'s score; used to normalise
// it into a 0..1 confidence for display.
const maxScore = 140.0

func clampConfidence(v float64) float64 {
	c := v / maxScore
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}
