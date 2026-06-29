// Package project locates a movielily project (the directory containing
// movielily.conf), exposes its standard paths, and reads/writes its config.
package project

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const ConfigName = "movielily.conf"

type Config struct {
	Name   string
	Width  int
	Height int
	FPS    int
	CRF    int
}

func DefaultConfig() Config {
	return Config{Width: 1440, Height: 1080, FPS: 30, CRF: 18}
}

type Project struct {
	Root   string
	Config Config
}

func (p *Project) Markers() string      { return filepath.Join(p.Root, "markers.txt") }
func (p *Project) Notes() string        { return filepath.Join(p.Root, "notes.txt") }
func (p *Project) Selects() string      { return filepath.Join(p.Root, "selects.txt") }
func (p *Project) Footage() string      { return filepath.Join(p.Root, "footage") }
func (p *Project) SequencesDir() string { return filepath.Join(p.Root, "sequences") }

func (p *Project) Sequence(name string) string {
	name = strings.TrimSuffix(filepath.Base(name), ".txt")
	return filepath.Join(p.SequencesDir(), name+".txt")
}

// Find walks up from start looking for movielily.conf.
func Find(start string) (*Project, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for {
		cfg := filepath.Join(dir, ConfigName)
		if st, err := os.Stat(cfg); err == nil && !st.IsDir() {
			c, err := readConfig(cfg)
			if err != nil {
				return nil, err
			}
			return &Project{Root: dir, Config: c}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("not inside a movielily project (no %s here or in any parent; run 'movielily init')", ConfigName)
		}
		dir = parent
	}
}

// Open finds the project starting at the current directory.
func Open() (*Project, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return Find(wd)
}

// Init creates a new project skeleton at dir.
func Init(dir, name string) (*Project, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = filepath.Base(root)
	}
	cfgPath := filepath.Join(root, ConfigName)
	if _, err := os.Stat(cfgPath); err == nil {
		return nil, fmt.Errorf("%s already exists in %s", ConfigName, root)
	}
	for _, d := range []string{root, filepath.Join(root, "footage"), filepath.Join(root, "sequences")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	c := DefaultConfig()
	c.Name = name
	if err := writeConfig(cfgPath, c); err != nil {
		return nil, err
	}
	// Touch the log files so they're discoverable and greppable from day one.
	for _, f := range []string{filepath.Join(root, "markers.txt"), filepath.Join(root, "notes.txt"), filepath.Join(root, "selects.txt")} {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			if err := os.WriteFile(f, nil, 0o644); err != nil {
				return nil, err
			}
		}
	}
	return &Project{Root: root, Config: c}, nil
}

// ResolveFootage turns a clip/image reference into an absolute path, checking
// footage/ first, then the project root, then the path as given.
func (p *Project) ResolveFootage(name string) (string, error) {
	var candidates []string
	if filepath.IsAbs(name) {
		candidates = append(candidates, name)
	}
	candidates = append(candidates,
		filepath.Join(p.Footage(), name),
		filepath.Join(p.Root, name),
		name,
	)
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return filepath.Abs(c)
		}
	}
	return "", fmt.Errorf("media not found: %q (looked in %s)", name, p.Footage())
}

// StoreName is how a media reference is recorded: its base name (footage is
// flat in v1), keeping the text files portable.
func (p *Project) StoreName(name string) string { return filepath.Base(name) }

func readConfig(path string) (Config, error) {
	c := DefaultConfig()
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "name":
			c.Name = v
		case "width":
			c.Width = atoiOr(v, c.Width)
		case "height":
			c.Height = atoiOr(v, c.Height)
		case "fps":
			c.FPS = atoiOr(v, c.FPS)
		case "crf":
			c.CRF = atoiOr(v, c.CRF)
		}
	}
	return c, sc.Err()
}

func writeConfig(path string, c Config) error {
	var b strings.Builder
	b.WriteString("# movielily project config\n")
	b.WriteString("name = " + c.Name + "\n\n")
	b.WriteString("# export target (4:3, SDR)\n")
	b.WriteString("width = " + strconv.Itoa(c.Width) + "\n")
	b.WriteString("height = " + strconv.Itoa(c.Height) + "\n")
	b.WriteString("fps = " + strconv.Itoa(c.FPS) + "\n\n")
	b.WriteString("# libx264 quality, lower is better (18 is visually lossless)\n")
	b.WriteString("crf = " + strconv.Itoa(c.CRF) + "\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
