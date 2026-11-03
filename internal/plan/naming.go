package plan

import (
	"fmt"
	"regexp"
	"strings"
)

// TrackFileName renders the virtual file name from the configured template.
// Recognised placeholders (DESIGN §4.8 / D12):
//
//	{num}       number, zero padded to two digits
//	{num:NN}    number, zero padded to NN digits ("print width"...)
//	{num:02d}   ...or the Go width form, the trailing "d" is optional
//	{title}     sanitised track title
//	{ext}       output extension, including the dot
var placeholderRe = regexp.MustCompile(`\{num(?::(\d+)d?)?\}|\{title\}|\{ext\}`)

// TrackFileName builds one track file name.
func TrackFileName(template string, num int, title string, maxTitleLen int, ext string, taken map[string]int) string {
	title = SanitizeTitle(title, maxTitleLen)
	name := placeholderRe.ReplaceAllStringFunc(template, func(m string) string {
		switch {
		case m == "{title}":
			return title
		case m == "{ext}":
			return ext
		case strings.HasPrefix(m, "{num:"):
			w := strings.TrimSuffix(strings.TrimPrefix(m, "{num:"), "}")
			return pad(num, w)
		default: // {num}
			return fmt.Sprintf("%02d", num)
		}
	})
	name = cleanName(name)

	// De-duplicate: two tracks may share a title (or the template may not
	// include the number at all).
	if n := taken[name]; n > 0 {
		base := name
		ext := ""
		if dot := strings.LastIndex(name, "."); dot > 0 {
			base, ext = name[:dot], name[dot:]
		}
		for i := 1; ; i++ {
			name = fmt.Sprintf("%s (%d)%s", base, n+i, ext)
			if taken[name] == 0 {
				break
			}
		}
	}
	taken[name]++
	return name
}

func pad(n int, width string) string {
	w := 0
	for _, r := range width {
		if r < '0' || r > '9' {
			w = 2
			break
		}
		w = w*10 + int(r-'0')
	}
	if w <= 0 {
		w = 2
	}
	return fmt.Sprintf(fmt.Sprintf("%%0%dd", w), n)
}

// invalidNameChars are Windows-hostile characters plus control characters; a
// library that has to survive a subscription backup over an SMB share keeps
// them out.
var invalidNameChars = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

// SanitizeTitle squeezes a track title down to something that works as a file
// name component: whitespace collapsed, separators replaced, bounded length.
func SanitizeTitle(title string, maxLen int) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == ' ':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, title)
	s = invalidNameChars.ReplaceAllString(s, "_")
	s = strings.Join(strings.Fields(s), " ")
	if maxLen > 0 {
		r := []rune(s)
		if len(r) > maxLen {
			s = string(r[:maxLen])
		}
	}
	s = strings.Trim(s, " .")
	return s
}

func cleanName(n string) string {
	n = strings.Join(strings.Fields(n), " ")
	if n == "" {
		n = "unknown"
	}
	// A name that is only dots would be invisible to some tooling.
	if strings.Trim(n, ".") == "" {
		return "unknown"
	}
	return strings.Trim(n, ". ")
}
