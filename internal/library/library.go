// Package library decides which files in a music library need splitting and
// which are passed through untouched (DESIGN D3, §4.6).
//
// The rule is deliberately narrow: a cue file only becomes a split candidate
// when a sibling audio file shares its basename AND the cue describes exactly
// one FILE. Everything else - per-track cues, log files, artwork - is
// invisible to cuefs and reaches Navidrome unchanged.
package library

import (
	"os"
	"path/filepath"
	"strings"

	"cuefs/internal/cue"
)

// Kind classifies a cue file.
type Kind string

const (
	// KindSplit means the sibling album file will be replaced by tracks.
	KindSplit Kind = "split"
	// KindPassthrough means the cue has nothing to split; leave it alone.
	KindPassthrough Kind = "passthrough"
	// KindError means the cue could not be parsed.
	KindError Kind = "error"
)

// Reasons attached to Kind.
const (
	ReasonNoSource   = "no-source-audio"
	ReasonSingleFile = "same-basename-source"
	ReasonMultiFile  = "multi-file-cue"
	ReasonParseError = "parse-error"
)

// Entry is the classification result for one cue file.
type Entry struct {
	CuePath    string     `json:"cue_path"`
	SourcePath string     `json:"source_path,omitempty"`
	Kind       Kind       `json:"kind"`
	Reason     string     `json:"reason"`
	CueFiles   int        `json:"cue_files"`
	Tracks     int        `json:"tracks"`
	Encoding   string     `json:"encoding,omitempty"`
	Warnings   []string   `json:"warnings,omitempty"`
	Err        string     `json:"error,omitempty"`
	Sheet      *cue.Sheet `json:"-"`
}

// FindSource looks for the album file that pairs with cuePath: same basename,
// extension taken from exts (which are compared case-insensitively). It also
// accepts the degenerate case where the cue is named "<album>.flac.cue".
func FindSource(cuePath string, exts []string) (string, bool) {
	dir := filepath.Dir(cuePath)
	base := filepath.Base(cuePath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	// "<album>.flac.cue" -> the album file is "<album>.flac".
	if hasExt(filepath.Ext(stem), exts) {
		if p := filepath.Join(dir, stem); isFile(p) {
			return p, true
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	// Index the directory once, keyed by lower-case stem.
	byStem := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		dot := strings.LastIndex(n, ".")
		if dot <= 0 {
			continue
		}
		if !hasExt(n[dot:], exts) {
			continue
		}
		byStem[strings.ToLower(n[:dot])] = filepath.Join(dir, n)
	}
	if p, ok := byStem[strings.ToLower(stem)]; ok {
		return p, true
	}
	return "", false
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func hasExt(ext string, exts []string) bool {
	ext = strings.ToLower(ext)
	for _, e := range exts {
		if strings.ToLower(e) == ext {
			return true
		}
	}
	return false
}

// IsCueFile reports whether name looks like a cue sheet.
func IsCueFile(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".cue")
}

// ApeAction is what a split candidate with an APE source turns into
// (DESIGN D14): the transcode split, a tree without the ape pair at all, or
// the untouched cue + ape pair exposed as-is.
type ApeAction string

const (
	ApeConvert     ApeAction = ""            // split normally (transcode via ffmpeg)
	ApeHide        ApeAction = "hide"        // remove the ape source AND its cue from the view
	ApePassthrough ApeAction = "passthrough" // expose the ape source and its cue untouched
)

// ApeOf resolves a configured ape_mode into the action; unknown or empty
// values mean convert (config validation rejects the outright errors).
func ApeOf(mode string) ApeAction {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "hide":
		return ApeHide
	case "passthrough":
		return ApePassthrough
	default:
		return ApeConvert
	}
}

// IsApe reports whether sourcePath is a Monkey's Audio image.
func IsApe(sourcePath string) bool {
	return strings.EqualFold(filepath.Ext(sourcePath), ".ape")
}

// Classify reads one cue file and decides what cuefs should do with it.
// It only ever opens files for reading (DESIGN D15).
func Classify(cuePath string, exts []string, opts cue.DetectOptions) Entry {
	e := Entry{CuePath: cuePath, Kind: KindError, Reason: ReasonParseError}

	if src, ok := FindSource(cuePath, exts); ok {
		e.SourcePath = src
	}

	s, err := cue.Load(cuePath, opts, nil)
	if err != nil {
		e.Err = err.Error()
		return e
	}
	e.Sheet = s
	e.CueFiles = len(s.Files)
	e.Tracks = len(s.AudioTracks())
	e.Warnings = s.Warnings
	if s.Encoding != nil {
		e.Encoding = s.Encoding.Encoding
	}

	switch {
	case e.SourcePath == "":
		e.Kind, e.Reason = KindPassthrough, ReasonNoSource
	case len(s.Files) != 1:
		// A source file exists but the cue lists several FILEs: refusing to
		// guess is safer than producing a broken album.
		e.Kind, e.Reason = KindPassthrough, ReasonMultiFile
	default:
		e.Kind, e.Reason = KindSplit, ReasonSingleFile
	}
	return e
}

// ResolveRoot makes a directory path safe to walk: filepath.WalkDir does not
// follow a symlinked root, so resolve it. Unresolvable paths come back
// unchanged and will surface as walk errors.
func ResolveRoot(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// Scan walks root (read-only), classifies every cue file it finds and hands
// each entry to fn. Walk errors are reported through fn as KindError entries
// only when they concern a cue file; directory errors abort the walk.
func Scan(root string, exts []string, opts cue.DetectOptions, fn func(Entry)) error {
	root = ResolveRoot(root)
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Unreadable directory: skip it rather than aborting the sweep.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !IsCueFile(d.Name()) {
			return nil
		}
		fn(Classify(path, exts, opts))
		return nil
	})
}

// Summary aggregates a scan.
type Summary struct {
	Cues        int `json:"cues"`
	Split       int `json:"split"`
	Passthrough int `json:"passthrough"`
	Errors      int `json:"errors"`
	Tracks      int `json:"tracks"`
}

// Add folds one entry into the summary. Tracks are only counted for split
// candidates: the tracks of a per-track cue already exist as real files and
// cuefs will not generate anything for them.
func (s *Summary) Add(e Entry) {
	s.Cues++
	if e.Kind == KindSplit {
		s.Split++
		s.Tracks += e.Tracks
		return
	}
	if e.Kind == KindPassthrough {
		s.Passthrough++
		return
	}
	s.Errors++
}
