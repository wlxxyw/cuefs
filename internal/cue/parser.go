package cue

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ParseText parses already-decoded cue text.
func ParseText(text string) (*Sheet, error) {
	s := &Sheet{Rem: map[string]string{}}
	var curFile *File
	var curTrack *Track

	lines := strings.Split(text, "\n")
	for n, raw := range lines {
		line := strings.TrimRight(strings.TrimLeft(raw, " \t\r"), "\r")
		if line == "" {
			continue
		}
		tokens := tokenize(line)
		if len(tokens) == 0 {
			continue
		}
		cmd := strings.ToUpper(tokens[0])
		rest := tokens[1:]

		switch cmd {
		case "REM":
			if len(rest) == 0 {
				continue
			}
			key := strings.ToUpper(rest[0])
			val := strings.Join(rest[1:], " ")
			// REM ENCODING gb18030 style hints are honoured by callers; keep
			// every REM as a flat "KEY=VALUE" pair.
			if old, ok := s.Rem[key]; ok && old != val {
				s.Rem[key] = old + " " + val
			} else {
				s.Rem[key] = val
			}

		case "CATALOG":
			s.Catalog = firstNonEmpty(rest)

		case "PERFORMER":
			v := firstNonEmpty(rest)
			if curTrack != nil {
				curTrack.Performers = append(curTrack.Performers, splitMulti(v)...)
			} else {
				s.Performers = append(s.Performers, splitMulti(v)...)
			}

		case "TITLE":
			v := firstNonEmpty(rest)
			if curTrack != nil {
				curTrack.Title = v
			} else {
				s.Title = v
			}

		case "SONGWRITER":
			v := firstNonEmpty(rest)
			if curTrack != nil {
				curTrack.Songwriters = append(curTrack.Songwriters, splitMulti(v)...)
			} else {
				s.Songwriters = append(s.Songwriters, splitMulti(v)...)
			}

		case "FILE":
			name, ftype := parseFileArgs(rest)
			if name == "" {
				s.warnf("line %d: FILE without a name", n+1)
				continue
			}
			curFile = &File{Name: name, Type: ftype}
			s.Files = append(s.Files, curFile)
			curTrack = nil

		case "TRACK":
			if len(rest) < 2 {
				s.warnf("line %d: malformed TRACK %q", n+1, strings.Join(rest, " "))
				continue
			}
			num, err := strconv.Atoi(strings.TrimSpace(rest[0]))
			if err != nil || num <= 0 {
				s.warnf("line %d: invalid track number %q", n+1, rest[0])
				continue
			}
			ttype := TrackType(strings.ToUpper(strings.TrimSpace(rest[1])))
			curTrack = &Track{
				Num:   num,
				Type:  ttype,
				Index: map[int]Time{},
			}
			if curFile == nil {
				s.warnf("line %d: TRACK %02d appears before any FILE statement", n+1, num)
				curFile = &File{Name: "", Type: FileWave}
				s.Files = append(s.Files, curFile)
			}
			curFile.Tracks = append(curFile.Tracks, curTrack)
			s.Tracks = append(s.Tracks, curTrack)

		case "INDEX":
			if curTrack == nil {
				s.warnf("line %d: INDEX outside of a TRACK", n+1)
				continue
			}
			if len(rest) < 2 {
				s.warnf("line %d: malformed INDEX %q", n+1, strings.Join(rest, " "))
				continue
			}
			idx, err := strconv.Atoi(strings.TrimSpace(rest[0]))
			if err != nil {
				s.warnf("line %d: invalid index number %q", n+1, rest[0])
				continue
			}
			ts, err := ParseTime(rest[1])
			if err != nil {
				s.warnf("line %d: %v", n+1, err)
				continue
			}
			if _, dup := curTrack.Index[idx]; dup {
				s.warnf("line %d: duplicate INDEX %d on track %02d", n+1, idx, curTrack.Num)
			}
			curTrack.Index[idx] = ts

		case "PREGAP":
			if curTrack == nil || len(rest) == 0 {
				s.warnf("line %d: malformed PREGAP", n+1)
				continue
			}
			if ts, err := ParseTime(rest[0]); err == nil {
				curTrack.Pregap = &ts
			} else {
				s.warnf("line %d: %v", n+1, err)
			}

		case "POSTGAP":
			if curTrack == nil || len(rest) == 0 {
				s.warnf("line %d: malformed POSTGAP", n+1)
				continue
			}
			if ts, err := ParseTime(rest[0]); err == nil {
				curTrack.Postgap = &ts
			} else {
				s.warnf("line %d: %v", n+1, err)
			}

		case "ISRC":
			if curTrack != nil && len(rest) > 0 {
				curTrack.Isrc = rest[0]
			}

		case "FLAGS":
			if curTrack != nil {
				curTrack.Flags = append(curTrack.Flags, rest...)
			}

		case "CDTEXTFILE":
			// Informational only; no effect on splitting.

		default:
			s.warnf("line %d: unsupported statement %q", n+1, tokens[0])
		}
	}

	if len(s.Tracks) == 0 {
		return nil, fmt.Errorf("cue sheet contains no TRACK statements")
	}
	s.Resolve(nil)
	return s, nil
}

// ParseBytes detects the encoding, decodes and parses the raw cue bytes.
func ParseBytes(b []byte, opts DetectOptions) (*Sheet, error) {
	text, det, err := Decode(b, opts)
	if err != nil {
		return nil, err
	}
	s, err := ParseText(text)
	if err != nil {
		return nil, fmt.Errorf("%w (detected encoding %s)", err, det.Encoding)
	}
	s.Encoding = &det
	s.baseWarnings = append([]string(nil), det.Warnings...)
	s.Warnings = append(s.Warnings, det.Warnings...)
	return s, nil
}

// Load reads, detects, parses and resolves a cue file from disk.
// fileEnd may be nil when the audio duration is not known yet.
func Load(path string, opts DetectOptions, fileEnd func(*File) (Time, bool)) (*Sheet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := ParseBytes(b, opts)
	if err != nil {
		return nil, err
	}
	s.SourcePath = path
	if fileEnd != nil {
		s.Resolve(fileEnd)
	}
	return s, nil
}

// --- helpers ---------------------------------------------------------------

func firstNonEmpty(v []string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// tokenize splits a cue line into tokens, honouring double quotes so that
// titles containing spaces survive as a single token.
func tokenize(line string) []string {
	var out []string
	i := 0
	for i < len(line) {
		c := line[i]
		if c == ' ' || c == '\t' || c == '\r' {
			i++
			continue
		}
		if c == '"' {
			j := i + 1
			for j < len(line) && line[j] != '"' {
				j++
			}
			out = append(out, line[i+1:min(j, len(line))])
			i = j + 1
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' && line[j] != '\t' && line[j] != '\r' {
			j++
		}
		out = append(out, line[i:j])
		i = j
	}
	return out
}

var fileTypes = map[string]bool{
	FileWave: true, FileMP3: true, FileAIFF: true, FileBinary: true, FileMotorola: true,
}

// parseFileArgs extracts the file name and type from FILE arguments. Quoted
// names come back as one token; unquoted names with spaces are rejoined.
func parseFileArgs(args []string) (name, ftype string) {
	if len(args) == 0 {
		return "", ""
	}
	// Walk from the end looking for the type keyword.
	for i := len(args) - 1; i >= 1; i-- {
		if fileTypes[strings.ToUpper(args[i])] {
			return strings.Join(args[:i], " "), strings.ToUpper(args[i])
		}
	}
	return strings.Join(args, " "), FileWave
}
