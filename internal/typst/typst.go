// Package typst renders title cards: small typst templates that live in the
// project's titles/ folder and receive their text per use, so one template
// (say, a chapter card) is written once and reused any number of times with
// different words. Rendered PNGs land in a content-addressed cache; nothing
// is regenerated unless the template or the text changed.
package typst

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"movielily/internal/project"
)

// DefaultTemplate is written to titles/chapter.typ the first time title cards
// are used, so there is always something to reuse and edit.
const DefaultTemplate = `// movielily title card. The card's text arrives as sys.inputs.text;
// this file is the STYLE, reused across every card that names it.
// Duplicate it (e.g. cp chapter.typ lower-third.typ) to make new styles.
#let card-text = sys.inputs.at("text", default: "Title")
#set page(width: 720pt, height: 540pt, margin: 48pt, fill: black)
#set text(fill: white, size: 64pt)
#align(center + horizon)[#card-text]
`

// TitlesDir is where a project keeps its card templates.
func TitlesDir(p *project.Project) string { return filepath.Join(p.Root, "titles") }

func cacheDir(p *project.Project) string {
	return filepath.Join(p.Root, ".cache", "titles")
}

// EnsureDefault makes sure titles/ exists with at least one template, creating
// chapter.typ on first use. Returns the template's base name when it created
// one ("" if the folder already had templates).
func EnsureDefault(p *project.Project) (created string, err error) {
	dir := TitlesDir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if ts, _ := Templates(p); len(ts) > 0 {
		return "", nil
	}
	if err := os.WriteFile(filepath.Join(dir, "chapter.typ"), []byte(DefaultTemplate), 0o644); err != nil {
		return "", err
	}
	return "chapter.typ", nil
}

// Templates lists the .typ files in titles/ by base name.
func Templates(p *project.Project) ([]string, error) {
	entries, err := os.ReadDir(TitlesDir(p))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".typ") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Resolve turns a template reference into an absolute path, checking titles/
// first (with or without the .typ suffix), then the path as given.
func Resolve(p *project.Project, name string) (string, error) {
	candidates := []string{
		filepath.Join(TitlesDir(p), name),
		filepath.Join(TitlesDir(p), name+".typ"),
		name,
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return filepath.Abs(c)
		}
	}
	return "", fmt.Errorf("title template not found: %q (looked in %s)", name, TitlesDir(p))
}

// StoreName is how a template is recorded in a sequence: its base name.
func StoreName(name string) string { return filepath.Base(name) }

// Render compiles the template with the given text into a cached PNG sized
// for the project frame and returns the PNG's path. The cache key is the
// template's content plus the text plus the frame, so editing the template or
// the words re-renders, and reuse costs nothing.
func Render(p *project.Project, template, text string) (string, error) {
	tpl, err := Resolve(p, template)
	if err != nil {
		return "", err
	}
	src, err := os.ReadFile(tpl)
	if err != nil {
		return "", err
	}
	w, h := p.Config.Width, p.Config.Height

	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00%s\x00%dx%d", src, text, w, h)
	out := filepath.Join(cacheDir(p), fmt.Sprintf("%x.png", sum.Sum(nil)[:12]))
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if err := os.MkdirAll(cacheDir(p), 0o755); err != nil {
		return "", err
	}

	argv, err := typstCommand()
	if err != nil {
		return "", err
	}
	// Typst rasterises at --ppi pixels per inch over the page size in points
	// (72pt = 1in), so ppi = 72 * target_px / page_pt. The default template's
	// 720pt-wide page at the default 1440px frame lands on ppi 144.
	tmp := out + ".tmp.png"
	args := append(argv[1:],
		"compile", "--format", "png",
		"--input", "text="+text,
		"--ppi", fmt.Sprintf("%d", ppiFor(tpl, w)),
		tpl, tmp,
	)
	cmd := exec.Command(argv[0], args...)
	if argv[0] == "nix" {
		// A leaked LD_LIBRARY_PATH (host libs vs nix glibc) breaks nix-provided
		// binaries; strip it, same as the shell scripts in navylily-tools do.
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "LD_LIBRARY_PATH=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
	}
	if msg, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("typst: %s", strings.TrimSpace(string(msg)))
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	return out, nil
}

// ppiFor picks the raster density that makes the template's page width come
// out at framePx pixels. Templates whose page width can't be read (custom
// sizes buried in code) get the 144 default, which suits a 720pt page at
// 1440px; ffmpeg scales whatever comes out, so this only affects sharpness.
func ppiFor(tplPath string, framePx int) int {
	src, err := os.ReadFile(tplPath)
	if err != nil {
		return 144
	}
	var pt float64
	for _, line := range strings.Split(string(src), "\n") {
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "#set page(width: %fpt", &pt); err == nil && pt > 0 {
			return int(72*float64(framePx)/pt + 0.5)
		}
	}
	return 144
}

// typstCommand finds typst: on PATH, or via an ephemeral nix shell on this
// declarative machine. The returned argv is the command prefix to run.
func typstCommand() ([]string, error) {
	if _, err := exec.LookPath("typst"); err == nil {
		return []string{"typst"}, nil
	}
	if _, err := exec.LookPath("nix"); err == nil {
		return []string{"nix", "shell", "nixpkgs#typst", "--command", "typst"}, nil
	}
	return nil, fmt.Errorf("typst not found on PATH (title cards need typst; everything else works without it)")
}
