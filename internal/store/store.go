// Package store reads and writes movielily's plain-text files. Reads ignore
// blank lines and #-comment lines so the files stay friendly to cat/grep/sed
// and hand-editing. Replacing a file is atomic (temp + rename).
package store

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// RawLines returns the meaningful lines of a file (comments and blanks
// stripped). A missing file yields no lines and no error.
func RawLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// Append adds one record line to a file, creating it (and parent dirs) if
// needed.
func Append(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

// WriteLines replaces a file's contents atomically.
func WriteLines(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	w := bufio.NewWriter(tmp)
	for _, l := range lines {
		if _, err := w.WriteString(l + "\n"); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
