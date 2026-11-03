package engine

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// runCapture runs a command, discarding its stdout and folding its stderr
// into the error; outHint tells the user which output file was aimed at.
func runCapture(bin string, args []string, outHint string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return nil
	}
	return commandError(bin, args, &stderr, err)
}

// runMD5 runs a command that prints an md5 hash line to stdout.
func runMD5(bin string, args []string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", commandError(bin, args, &stderr, err)
	}
	line := strings.TrimSpace(stdout.String())
	const prefix = "MD5="
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("%s: unexpected output %q", bin, line)
	}
	return strings.TrimPrefix(line, prefix), nil
}

func commandError(bin string, args []string, stderr *bytes.Buffer, err error) error {
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		return fmt.Errorf("%s %s: %w", bin, args[0], err)
	}
	return fmt.Errorf("%s %s: %s", bin, args[0], firstLines(msg, 6))
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " | ")
}
