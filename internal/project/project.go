// Package project locates a movielily project (the directory containing
// movielily.conf), exposes its standard paths, and reads/writes its config.
package project

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const ConfigName = "movielily.conf"

const gitignoreStart = "# >>> movielily"
const gitignoreEnd = "# <<< movielily"

var projectDirs = []string{
	"footage",     // legacy catch-all; old flat projects keep working
	"storyboards", // board drawings and animatic stills
	"images",      // cleaned/key stills used by the film
	"refs",        // visual references
	"audio",       // voice, music, ambience
	"fxs",         // overlays, textures, effect plates
	"scripts",     // writing, shot notes, dialogue drafts
	"sequences",
	"exports",
	"titles",
	"anims",
	"grades",
}

var projectSubdirs = []string{
	"storyboards/inbox",
	"storyboards/scenes",
	"images/stills",
	"images/backgrounds",
	"refs/visual",
	"refs/visual/bildkasten",
	"refs/research",
	"audio/dialogue",
	"audio/music",
	"audio/sfx",
	"audio/ambience",
	"fxs/overlays",
	"fxs/mattes",
	"fxs/textures",
	"footage/raw",
	"exports/video",
	"exports/storyboard-books",
	"exports/frames",
}

var mediaDirNames = []string{"footage", "storyboards", "images", "refs", "audio", "fxs"}
var imageDirNames = []string{"storyboards", "images", "refs", "fxs", "footage"}

type folderDoc struct {
	Path string
	Text string
}

var projectFolderDocs = []folderDoc{
	{"README.txt", "movielily project\n\nThe edit is plain text:\n- sequences/*.txt are the cuts\n- movielily.conf is render settings\n- markers.txt, notes.txt and selects.txt are searchable notes\n\nSource media lives in storyboards/, images/, refs/, audio/, fxs/ and footage/.\nExports belong in exports/. Git snapshots track the instructions, not the heavy media.\n"},
	{"scripts/README.txt", "Writing lives here: script drafts, scene notes, dialogue and shot ideas.\n"},
	{"storyboards/README.txt", "Storyboard drawings and animatic stills live here.\n\nUse inbox/ for newly imported boards and scenes/ when you want manual scene folders. The browser board scans this folder recursively.\n"},
	{"storyboards/inbox/README.txt", "Images added from the browser board land here by default.\n"},
	{"storyboards/scenes/README.txt", "Optional manual scene folders for storyboard boards.\n"},
	{"images/README.txt", "Clean stills, backgrounds, plates and finished image assets used by the cut.\n"},
	{"images/stills/README.txt", "Finished stills and cleaned board images used directly in sequences.\n"},
	{"images/backgrounds/README.txt", "Backgrounds, plates and establishing image assets.\n"},
	{"refs/README.txt", "Reference material for the film: visual refs, research images, mood boards and notes.\n"},
	{"refs/visual/README.txt", "Visual references, composition refs, colour refs and mood material.\n"},
	{"refs/visual/bildkasten/README.txt", "Bildkasten-selected visual references live here as symlinks, grouped by tag. Originals remain in Bildkasten's library.\n"},
	{"refs/research/README.txt", "Research images and source material that are not part of the final cut.\n"},
	{"audio/README.txt", "Audio source files. Suggested folders: dialogue/, music/, sfx/ and ambience/.\n"},
	{"audio/dialogue/README.txt", "Voice, narration and dialogue takes.\n"},
	{"audio/music/README.txt", "Music beds and score ideas.\n"},
	{"audio/sfx/README.txt", "Sound effects.\n"},
	{"audio/ambience/README.txt", "Room tone, atmosphere and background beds.\n"},
	{"fxs/README.txt", "Effect source assets: overlays, mattes, textures and transparent PNG elements.\n"},
	{"fxs/overlays/README.txt", "Transparent PNGs and other overlay elements.\n"},
	{"fxs/mattes/README.txt", "Masks and mattes.\n"},
	{"fxs/textures/README.txt", "Grain, paper, dust, light leaks and texture plates.\n"},
	{"footage/README.txt", "Raw video and legacy catch-all media. Old movielily projects can keep flat files here.\n"},
	{"footage/raw/README.txt", "Raw camera clips and imported footage.\n"},
	{"sequences/README.txt", "Plain-text cuts live here. Example records: section|Scene 1, image|storyboards/inbox/001.png|2|note, audio|audio/music/song.wav|-12|#duck.\n"},
	{"exports/README.txt", "Generated work goes here: video/ for renders, storyboard-books/ for Typst/PDF books and frames/ for still outputs.\n"},
	{"exports/video/README.txt", "Rendered movies land here.\n"},
	{"exports/storyboard-books/README.txt", "Printable Typst/PDF storyboard books land here.\n"},
	{"exports/frames/README.txt", "Frame grabs and thumbnail stills land here.\n"},
	{"titles/README.txt", "Typst title-card templates (.typ) live here.\n"},
	{"anims/README.txt", "Manim animation templates (.py) live here.\n"},
	{"grades/README.txt", "Optional text grade presets live here.\n"},
}

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

func (p *Project) Markers() string       { return filepath.Join(p.Root, "markers.txt") }
func (p *Project) Notes() string         { return filepath.Join(p.Root, "notes.txt") }
func (p *Project) Selects() string       { return filepath.Join(p.Root, "selects.txt") }
func (p *Project) Footage() string       { return filepath.Join(p.Root, "footage") }
func (p *Project) FootageRawDir() string { return filepath.Join(p.Footage(), "raw") }
func (p *Project) StoryboardsDir() string {
	return filepath.Join(p.Root, "storyboards")
}
func (p *Project) StoryboardInboxDir() string { return filepath.Join(p.StoryboardsDir(), "inbox") }
func (p *Project) ImagesDir() string          { return filepath.Join(p.Root, "images") }
func (p *Project) RefsDir() string            { return filepath.Join(p.Root, "refs") }
func (p *Project) AudioDir() string           { return filepath.Join(p.Root, "audio") }
func (p *Project) FXDir() string              { return filepath.Join(p.Root, "fxs") }
func (p *Project) ScriptsDir() string         { return filepath.Join(p.Root, "scripts") }
func (p *Project) ExportsDir() string         { return filepath.Join(p.Root, "exports") }
func (p *Project) SequencesDir() string       { return filepath.Join(p.Root, "sequences") }
func (p *Project) GradesDir() string          { return filepath.Join(p.Root, "grades") }

func (p *Project) MediaDirs() []string {
	return p.dirs(mediaDirNames)
}

func (p *Project) ImageDirs() []string {
	return p.dirs(imageDirNames)
}

func (p *Project) ProjectDirs() []string {
	return p.dirs(append(append([]string{}, projectDirs...), projectSubdirs...))
}

func (p *Project) ScaffoldFiles() []string {
	files := []string{
		filepath.Join(p.Root, "README.md"),
		filepath.Join(p.Root, ".gitignore"),
		p.Markers(),
		p.Notes(),
		p.Selects(),
	}
	for _, doc := range projectFolderDocs {
		files = append(files, filepath.Join(p.Root, filepath.FromSlash(doc.Path)))
	}
	return files
}

// SnapshotPaths is the complete portable, text-only project surface. Git
// snapshots stage only these paths, never arbitrary root files or media.
func (p *Project) SnapshotPaths() []string {
	paths := []string{
		ConfigName, ".gitignore", "README.md", "markers.txt", "notes.txt", "selects.txt",
		"scripts", "sequences", "titles", "anims", "grades",
	}
	for _, doc := range projectFolderDocs {
		paths = append(paths, filepath.FromSlash(doc.Path))
	}
	return paths
}

func (p *Project) dirs(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, filepath.Join(p.Root, name))
	}
	return out
}

func (p *Project) Sequence(name string) string {
	name = strings.TrimSuffix(filepath.Base(name), ".txt")
	return filepath.Join(p.SequencesDir(), name+".txt")
}

func IsSequenceFileName(name string) bool {
	return !strings.HasPrefix(name, ".") &&
		strings.HasSuffix(name, ".txt") &&
		!strings.EqualFold(name, "README.txt")
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
	for _, d := range append([]string{root}, (&Project{Root: root}).ProjectDirs()...) {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	c := DefaultConfig()
	c.Name = name
	if err := writeConfig(cfgPath, c); err != nil {
		return nil, err
	}
	p := &Project{Root: root, Config: c}
	if err := p.EnsureStructure(); err != nil {
		return nil, err
	}
	return p, nil
}

// EnsureStructure creates any missing standard folders and small helper files
// without overwriting user content. It lets old projects adopt the newer
// short-film workspace layout gradually.
func (p *Project) EnsureStructure() error {
	for _, d := range p.ProjectDirs() {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	for _, f := range []string{p.Markers(), p.Notes(), p.Selects()} {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			if err := os.WriteFile(f, nil, 0o644); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if err := writeProjectReadmes(p.Root); err != nil {
		return err
	}
	if err := writeProjectMarkdown(p.Root, p.Config.Name); err != nil {
		return err
	}
	if err := writeDefaultSequence(p.Root); err != nil {
		return err
	}
	return EnsureGitignore(p.Root)
}

// ResolveFootage turns a media reference into an absolute path. Old projects
// can keep flat names in footage/; new projects can use readable paths such as
// storyboards/shot-001.png, images/bg.png, audio/dialogue.wav or fxs/smoke.png.
func (p *Project) ResolveFootage(name string) (string, error) {
	var candidates []string
	if filepath.IsAbs(name) {
		candidates = append(candidates, name)
	} else if hasPathSep(name) {
		candidates = append(candidates, filepath.Join(p.Root, name))
	}
	for _, dir := range p.MediaDirs() {
		candidates = append(candidates, filepath.Join(dir, name))
	}
	candidates = append(candidates, filepath.Join(p.Root, name), name)
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return filepath.Abs(c)
		}
	}
	if !filepath.IsAbs(name) && !hasPathSep(name) {
		if match, ok, err := p.findUniqueBasename(name); err != nil {
			return "", err
		} else if ok {
			return match, nil
		}
	}
	return "", fmt.Errorf("media not found: %q (looked in project media folders)", name)
}

// StoreName is how a media reference is recorded. Files in the new source
// folders keep a project-relative path for readability; old flat footage keeps
// the basename so existing EDLs and habits remain compatible.
func (p *Project) StoreName(name string) string {
	if abs, err := p.ResolveFootage(name); err == nil {
		if rel, ok := p.rel(abs); ok {
			if strings.HasPrefix(rel, "footage/") {
				rest := strings.TrimPrefix(rel, "footage/")
				if !strings.Contains(rest, "/") {
					return filepath.Base(rel)
				}
			}
			return rel
		}
	}
	clean := filepath.Clean(name)
	if !filepath.IsAbs(clean) && clean != "." && !strings.HasPrefix(clean, "..") && hasPathSep(clean) {
		return filepath.ToSlash(clean)
	}
	return filepath.Base(name)
}

func (p *Project) rel(path string) (string, bool) {
	root, err := filepath.Abs(p.Root)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// RelPath returns a project-relative slash path when path is inside the
// project. It is used by packages that need user-facing folder names without
// knowing the project's internal path rules.
func (p *Project) RelPath(path string) (string, bool) {
	return p.rel(path)
}

func (p *Project) findUniqueBasename(name string) (string, bool, error) {
	var matches []string
	for _, dir := range p.MediaDirs() {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || d.Name() != name {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			matches = append(matches, abs)
			return nil
		})
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", false, err
		}
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	var rels []string
	for _, match := range matches {
		if rel, ok := p.rel(match); ok {
			rels = append(rels, rel)
		}
	}
	return "", false, fmt.Errorf("ambiguous media name %q; use one of: %s", name, strings.Join(rels, ", "))
}

func writeProjectReadmes(root string) error {
	for _, doc := range projectFolderDocs {
		path := filepath.Join(root, filepath.FromSlash(doc.Path))
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(doc.Text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeProjectMarkdown(root, name string) error {
	path := filepath.Join(root, "README.md")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(root)
	}
	body := "# " + name + "\n\n" +
		"A movielily short-film project.\n\n" +
		"## Edit\n\n" +
		"- `sequences/main.txt` is the main cut. It is plain text and safe to edit in movielily, vim, or GitHub.\n" +
		"- `movielily board main --open` opens the browser light table for storyboard images.\n" +
		"- `movielily edit main` opens the terminal editor for timing, audio, overlays and final checks.\n" +
		"- `movielily storyboard main exports/storyboard-books/main.pdf` makes a printable storyboard book.\n" +
		"- `movielily export main exports/video/main.mp4` renders the final video.\n\n" +
		"## Folders\n\n" +
		"```text\n" +
		"scripts/                  script drafts, dialogue and shot notes\n" +
		"storyboards/inbox/        boards imported from the browser light table\n" +
		"storyboards/scenes/       optional manual scene folders\n" +
		"images/stills/            cleaned stills and image assets used in the cut\n" +
		"images/backgrounds/       backgrounds and plates\n" +
		"refs/                     visual reference and research\n" +
		"audio/dialogue/           voice, narration and dialogue takes\n" +
		"audio/music/              music beds\n" +
		"audio/sfx/                sound effects\n" +
		"audio/ambience/           room tone and atmosphere\n" +
		"fxs/                      overlays, mattes and texture plates\n" +
		"footage/raw/              raw clips and legacy catch-all media\n" +
		"sequences/                plain-text cuts\n" +
		"exports/                  rendered movies, PDFs and frames\n" +
		"```\n\n" +
		"## Git\n\n" +
		"The `.gitignore` keeps source media, generated exports and caches out of Git. Commit the text instructions and folder notes; sync heavy media out of band.\n\n" +
		"```bash\n" +
		"movielily snapshot \"first cut\"\n" +
		"git remote add origin git@github.com:USER/" + filepath.Base(root) + ".git\n" +
		"git push -u origin main\n" +
		"```\n"
	return os.WriteFile(path, []byte(body), 0o644)
}

func writeDefaultSequence(root string) error {
	dir := filepath.Join(root, "sequences")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && IsSequenceFileName(e.Name()) {
			return nil
		}
	}
	path := filepath.Join(dir, "main.txt")
	content := "# main - movielily sequence\n" +
		"# Add storyboard images with: movielily board main --open\n" +
		"section|Scene 01 - Opening\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

// EnsureGitignore installs or appends the movielily ignore block. The block is
// intentionally idempotent so snapshot/doctor can repair older projects.
func EnsureGitignore(root string) error {
	path := filepath.Join(root, ".gitignore")
	block := GitignoreBlock()
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(current), gitignoreStart) {
		if strings.Contains(string(current), "!/refs/visual/bildkasten/README.txt") {
			return nil
		}
		return os.WriteFile(path, []byte(string(current)+"\n# Keep the Bildkasten folder label, never its linked media.\n!/refs/visual/bildkasten/\n!/refs/visual/bildkasten/README.txt\n"), 0o644)
	}
	next := string(current)
	if strings.TrimSpace(next) != "" && !strings.HasSuffix(next, "\n") {
		next += "\n"
	}
	if strings.TrimSpace(next) != "" {
		next += "\n"
	}
	next += block
	return os.WriteFile(path, []byte(next), 0o644)
}

func GitignoreBlock() string {
	return gitignoreStart + "\n" +
		"# Keep the readable project instructions in git, not heavy source media.\n" +
		"/.cache/\n" +
		"*.review.edl\n" +
		"\n" +
		"# Generated output.\n" +
		"/exports/**\n" +
		"!/exports/\n" +
		"!/exports/README.txt\n" +
		"!/exports/video/\n" +
		"!/exports/video/README.txt\n" +
		"!/exports/storyboard-books/\n" +
		"!/exports/storyboard-books/README.txt\n" +
		"!/exports/frames/\n" +
		"!/exports/frames/README.txt\n" +
		"\n" +
		"# Source media folders. README.txt files remain trackable as folder labels.\n" +
		gitignoreMediaFolder("footage", "raw") +
		gitignoreMediaFolder("storyboards", "inbox", "scenes") +
		gitignoreMediaFolder("images", "stills", "backgrounds") +
		gitignoreMediaFolder("refs", "visual", "research") +
		"!/refs/visual/bildkasten/\n" +
		"!/refs/visual/bildkasten/README.txt\n" +
		gitignoreMediaFolder("audio", "dialogue", "music", "sfx", "ambience") +
		gitignoreMediaFolder("fxs", "overlays", "mattes", "textures") +
		"\n" +
		"# Loose media accidentally dropped at the root.\n" +
		"*.mp4\n*.mov\n*.mkv\n*.m4v\n*.webm\n" +
		"*.wav\n*.mp3\n*.m4a\n*.flac\n*.aac\n*.ogg\n*.opus\n" +
		"*.png\n*.jpg\n*.jpeg\n*.webp\n*.tif\n*.tiff\n*.psd\n*.kra\n*.xcf\n" +
		gitignoreEnd + "\n"
}

func gitignoreMediaFolder(name string, subdirs ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "/%s/**\n", name)
	fmt.Fprintf(&b, "!/%s/\n", name)
	fmt.Fprintf(&b, "!/%s/README.txt\n", name)
	for _, subdir := range subdirs {
		fmt.Fprintf(&b, "!/%s/%s/\n", name, subdir)
		fmt.Fprintf(&b, "!/%s/%s/README.txt\n", name, subdir)
	}
	return b.String()
}

func hasPathSep(s string) bool {
	return strings.Contains(s, "/") || strings.Contains(s, string(filepath.Separator))
}

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
