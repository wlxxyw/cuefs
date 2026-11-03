package cue

import (
	"fmt"
	"strings"
)

// Time is a CD timestamp in minutes:seconds:frames, 75 frames per second
// (DESIGN §4.1). It is exact: no floating point ever enters the conversion.
type Time struct {
	MM, SS, FF int
}

// Frames returns the timestamp as a count of CD frames (75 per second).
func (t Time) Frames() int64 {
	return int64(t.MM)*4500 + int64(t.SS)*75 + int64(t.FF)
}

// FromFrames builds a Time from a count of CD frames.
func FromFrames(f int64) Time {
	if f < 0 {
		f = 0
	}
	ff := f % 75
	rest := f / 75
	return Time{MM: int(rest / 60), SS: int(rest % 60), FF: int(ff)}
}

// String renders the canonical "mm:ss:ff" form.
func (t Time) String() string { return fmt.Sprintf("%02d:%02d:%02d", t.MM, t.SS, t.FF) }

// Add returns t + o with proper carries.
func (t Time) Add(o Time) Time { return FromFrames(t.Frames() + o.Frames()) }

// Sub returns t - o, clamped at zero (negative durations are not meaningful
// for cue sheets).
func (t Time) Sub(o Time) Time {
	d := t.Frames() - o.Frames()
	if d < 0 {
		d = 0
	}
	return FromFrames(d)
}

// Less reports whether t precedes o.
func (t Time) Less(o Time) bool { return t.Frames() < o.Frames() }

// DurationSeconds returns the duration as a fractional number of seconds.
// Only for human-readable output; never use it for sample arithmetic.
func (t Time) DurationSeconds() float64 {
	return float64(t.MM)*60 + float64(t.SS) + float64(t.FF)/75.0
}

// ParseTime parses "mm:ss:ff". Whitespace around the components is tolerated
// because real-world cue files are full of it.
func ParseTime(s string) (Time, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return Time{}, fmt.Errorf("invalid time %q: want mm:ss:ff", s)
	}
	var v [3]int
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return Time{}, fmt.Errorf("invalid time %q: empty component", s)
		}
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				return Time{}, fmt.Errorf("invalid time %q: non-digit in component %q", s, p)
			}
			n = n*10 + int(r-'0')
		}
		v[i] = n
	}
	if v[1] > 59 {
		return Time{}, fmt.Errorf("invalid time %q: seconds out of range", s)
	}
	if v[2] > 74 {
		return Time{}, fmt.Errorf("invalid time %q: frames out of range (0-74)", s)
	}
	return Time{MM: v[0], SS: v[1], FF: v[2]}, nil
}

// Samples converts a CD timestamp to an absolute sample index for the given
// sample rate, rounding to the nearest sample (DESIGN §4.1).
//
//	samples = round(frames * sampleRate / 75)
//
// The intermediate product stays well inside int64 range: 24h of audio at
// 192 kHz is ~1.6e13 CD frames * 192000 ≈ 3e18 < 9.2e18.
func Samples(t Time, sampleRate int) (int64, error) {
	if sampleRate <= 0 {
		return 0, fmt.Errorf("invalid sample rate %d", sampleRate)
	}
	if t.FF > 74 || t.SS > 59 || t.MM < 0 {
		return 0, fmt.Errorf("invalid timestamp %s", t)
	}
	n := t.Frames() * int64(sampleRate)
	return (n + 37) / 75, nil
}
