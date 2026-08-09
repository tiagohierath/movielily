package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"milklily/internal/model"
	"milklily/internal/project"
	"milklily/internal/store"
)

func newBoardCmd() *cobra.Command {
	var addr string
	var open bool
	var seed bool
	var frames int
	var imagesDir string

	cmd := &cobra.Command{
		Use:   "board <sequence>",
		Short: "Open a browser storyboard board for a sequence",
		Long: "board serves a local browser view over sequences/<name>.txt: add\n" +
			"storyboard images from the project image folders, reorder shots, set\n" +
			"durations and pan motion, then preview the image animatic in-browser.\n" +
			"Use --images-dir to copy an external storyboard folder into this\n" +
			"project's storyboards/inbox/ before opening it.\n" +
			"The sequence file remains the canonical movie and the TUI can open it\n" +
			"immediately.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			name := strings.TrimSuffix(filepath.Base(args[0]), ".txt")
			s := &boardServer{p: p, name: name, defaultFrames: frames}
			if imagesDir != "" {
				n, err := s.importImageDir(imagesDir)
				if err != nil {
					return err
				}
				fmt.Printf("copied %d image(s) into storyboards/inbox/\n", n)
			}
			if seed {
				n, err := s.importUnusedImages(frames)
				if err != nil {
					return err
				}
				if n > 0 {
					fmt.Printf("imported %d storyboard image(s) into sequences/%s.txt\n", n, name)
				}
			}
			u := "http://" + addr
			fmt.Printf("board %q: %s\n", name, u)
			fmt.Printf("editing: sequences/%s.txt\n", name)
			if open {
				_ = exec.Command("xdg-open", u).Start()
			}
			return http.ListenAndServe(addr, s.routes())
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:4451", "local address to serve")
	cmd.Flags().BoolVar(&open, "open", false, "open the board in a browser")
	cmd.Flags().BoolVar(&seed, "seed", false, "append unused footage images to the sequence before serving")
	cmd.Flags().IntVar(&frames, "frames", 48, "default still duration in frames")
	cmd.Flags().StringVar(&imagesDir, "images-dir", "", "copy images from this external folder into storyboards/inbox before serving")
	return cmd
}

type boardServer struct {
	p             *project.Project
	name          string
	defaultFrames int
}

type boardPayload struct {
	Project       string         `json:"project"`
	Sequence      string         `json:"sequence"`
	Path          string         `json:"path"`
	FPS           int            `json:"fps"`
	DefaultFrames int            `json:"default_frames"`
	TotalSeconds  float64        `json:"total_seconds"`
	TotalFrames   int            `json:"total_frames"`
	Blocks        []boardBlock   `json:"blocks"`
	FootageImages []footageImage `json:"footage_images"`
	AudioFiles    []footageImage `json:"audio_files"`
}

type boardBlock struct {
	ID         string             `json:"id"`
	Kind       string             `json:"kind"`
	Index      int                `json:"index,omitempty"`
	Start      float64            `json:"start,omitempty"`
	StartFrame int                `json:"start_frame,omitempty"`
	Duration   float64            `json:"duration,omitempty"`
	Frames     int                `json:"frames,omitempty"`
	MediaURL   string             `json:"media_url,omitempty"`
	Item       model.SequenceItem `json:"item"`
}

type footageImage struct {
	File     string `json:"file"`
	MediaURL string `json:"media_url"`
	Used     bool   `json:"used"`
}

type boardSaveRequest struct {
	Blocks []boardBlock `json:"blocks"`
}

func (s *boardServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/api/sequence", s.apiSequence)
	mux.HandleFunc("/api/import-images", s.apiImportImages)
	mux.HandleFunc("/api/upload-images", s.apiUploadImages)
	mux.HandleFunc("/media", s.media)
	return mux
}

func (s *boardServer) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(boardHTML2))
}

func (s *boardServer) apiSequence(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		payload, err := s.payload()
		if err != nil {
			httpError(w, err)
			return
		}
		writeBoardJSON(w, payload)
	case http.MethodPut:
		var req boardSaveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, err)
			return
		}
		if err := s.save(req.Blocks); err != nil {
			httpError(w, err)
			return
		}
		payload, err := s.payload()
		if err != nil {
			httpError(w, err)
			return
		}
		writeBoardJSON(w, payload)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *boardServer) apiImportImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	n, err := s.importUnusedImages(s.defaultFrames)
	if err != nil {
		httpError(w, err)
		return
	}
	payload, err := s.payload()
	if err != nil {
		httpError(w, err)
		return
	}
	writeBoardJSON(w, struct {
		boardPayload
		Imported int `json:"imported"`
	}{boardPayload: payload, Imported: n})
}

func (s *boardServer) apiUploadImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		httpError(w, err)
		return
	}
	var imported int
	if r.MultipartForm != nil {
		dstDir := s.p.StoryboardInboxDir()
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			httpError(w, err)
			return
		}
		for _, headers := range r.MultipartForm.File {
			for _, hdr := range headers {
				name := filepath.Base(hdr.Filename)
				if !isStoryboardImage(name) {
					continue
				}
				src, err := hdr.Open()
				if err != nil {
					httpError(w, err)
					return
				}
				dstName := collisionFreeName(dstDir, name)
				dstPath := filepath.Join(dstDir, dstName)
				dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
				if err != nil {
					src.Close()
					httpError(w, err)
					return
				}
				_, copyErr := io.Copy(dst, src)
				closeErr := dst.Close()
				src.Close()
				if copyErr != nil {
					httpError(w, copyErr)
					return
				}
				if closeErr != nil {
					httpError(w, closeErr)
					return
				}
				imported++
				fmt.Fprintf(os.Stderr, "board uploaded image: %s\n", dstName)
			}
		}
	}
	payload, err := s.payload()
	if err != nil {
		httpError(w, err)
		return
	}
	writeBoardJSON(w, struct {
		boardPayload
		Imported int `json:"imported"`
	}{boardPayload: payload, Imported: imported})
}

func (s *boardServer) media(w http.ResponseWriter, r *http.Request) {
	file := strings.TrimSpace(r.URL.Query().Get("file"))
	if file == "" {
		http.NotFound(w, r)
		return
	}
	abs, err := s.p.ResolveFootage(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, abs)
}

func (s *boardServer) payload() (boardPayload, error) {
	items, err := s.loadItems()
	if err != nil {
		return boardPayload{}, err
	}
	if err := validateBoardItems(s.name, items); err != nil {
		return boardPayload{}, err
	}
	blocks := s.blocks(items)
	images, err := s.footageImages(items)
	if err != nil {
		return boardPayload{}, err
	}
	audio, err := listBoardAudio(s.p)
	if err != nil {
		return boardPayload{}, err
	}
	total := 0.0
	for _, b := range blocks {
		if b.Kind == "image" {
			total += b.Duration
		}
	}
	return boardPayload{
		Project:       s.p.Config.Name,
		Sequence:      s.name,
		Path:          filepath.ToSlash(filepath.Join("sequences", s.name+".txt")),
		FPS:           s.p.Config.FPS,
		DefaultFrames: s.defaultFrames,
		TotalSeconds:  total,
		TotalFrames:   int(total*float64(s.p.Config.FPS) + 0.5),
		Blocks:        blocks,
		FootageImages: images,
		AudioFiles:    audio,
	}, nil
}

func (s *boardServer) loadItems() ([]model.SequenceItem, error) {
	return store.LoadSequence(s.p.Sequence(s.name))
}

func (s *boardServer) save(blocks []boardBlock) error {
	lines := []string{"# " + s.name + " - edited with milklily board"}
	for _, b := range blocks {
		it := b.Item
		switch {
		case b.Kind == "section" || it.Kind == model.KindSection:
			it.Kind = model.KindSection
			lines = append(lines, it.String())
		case b.Kind == "image" || it.Kind == model.KindImage:
			it.Kind = model.KindImage
			it.File = s.p.StoreName(it.File)
			if it.Dur <= 0 {
				it.Dur = float64(s.defaultFrames) / float64(s.p.Config.FPS)
			}
			lines = append(lines, it.String())
		default:
			return fmt.Errorf("milklily board is image-only; refusing to save %s record", it.Kind)
		}
	}
	return store.WriteLines(s.p.Sequence(s.name), lines)
}

func (s *boardServer) importUnusedImages(frames int) (int, error) {
	items, err := s.loadItems()
	if err != nil {
		return 0, err
	}
	used := map[string]bool{}
	for _, it := range items {
		if it.Kind == model.KindImage {
			used[it.File] = true
		}
	}
	images, err := listStoryboardImages(s.p)
	if err != nil {
		return 0, err
	}
	dur := float64(frames) / float64(s.p.Config.FPS)
	var n int
	for _, img := range images {
		if used[img] {
			continue
		}
		items = append(items, model.SequenceItem{Kind: model.KindImage, File: img, Dur: dur, Note: strings.TrimSuffix(img, filepath.Ext(img))})
		n++
	}
	if n == 0 {
		return 0, nil
	}
	lines := []string{"# " + s.name + " - edited with milklily board"}
	for _, it := range items {
		lines = append(lines, it.String())
	}
	return n, store.WriteLines(s.p.Sequence(s.name), lines)
}

// importImageDir copies external storyboard images into the project inbox. It
// never moves or modifies the originals, so later exports remain self-contained.
func (s *boardServer) importImageDir(src string) (int, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if !st.IsDir() {
		return 0, fmt.Errorf("images directory %q is not a folder", src)
	}
	dstDir := s.p.StoryboardInboxDir()
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return 0, err
	}
	n := 0
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !isStoryboardImage(d.Name()) {
			return nil
		}
		dst := filepath.Join(dstDir, collisionFreeName(dstDir, d.Name()))
		if err := copyFile(path, dst); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

func (s *boardServer) blocks(items []model.SequenceItem) []boardBlock {
	blocks := make([]boardBlock, 0, len(items))
	t := 0.0
	n := 0
	for i, it := range items {
		b := boardBlock{
			ID:   fmt.Sprintf("b%d", i+1),
			Kind: string(it.Kind),
			Item: it,
		}
		if it.Kind == model.KindImage {
			n++
			d := it.Duration()
			b.Index = n
			b.Start = t
			b.StartFrame = int(t*float64(s.p.Config.FPS) + 0.5)
			b.Duration = d
			b.Frames = int(d*float64(s.p.Config.FPS) + 0.5)
			b.MediaURL = "/media?file=" + urlQueryEscape(it.File)
			t += d
		}
		blocks = append(blocks, b)
	}
	return blocks
}

func (s *boardServer) footageImages(items []model.SequenceItem) ([]footageImage, error) {
	images, err := listStoryboardImages(s.p)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, it := range items {
		if it.Kind == model.KindImage {
			used[it.File] = true
		}
	}
	out := make([]footageImage, 0, len(images))
	for _, img := range images {
		out = append(out, footageImage{
			File:     img,
			MediaURL: "/media?file=" + urlQueryEscape(img),
			Used:     used[img],
		})
	}
	return out, nil
}

func validateBoardItems(seq string, items []model.SequenceItem) error {
	for _, it := range items {
		if it.Kind == model.KindImage || it.Kind == model.KindSection {
			continue
		}
		return fmt.Errorf("milklily board is image-only; %s contains a %s record for %q (use the TUI for video/audio/voice/title work)",
			seq, it.Kind, it.File)
	}
	return nil
}

func listStoryboardImages(p *project.Project) ([]string, error) {
	seen := map[string]bool{}
	var images []string
	for _, dir := range p.ImageDirs() {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !isStoryboardImage(d.Name()) {
				return nil
			}
			name := p.StoreName(path)
			if !seen[name] {
				seen[name] = true
				images = append(images, name)
			}
			return nil
		})
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
	}
	sort.Strings(images)
	return images, nil
}

func listBoardAudio(p *project.Project) ([]footageImage, error) {
	var out []footageImage
	err := filepath.WalkDir(p.AudioDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !model.IsAudioFile(d.Name()) {
			return nil
		}
		file := p.StoreName(path)
		out = append(out, footageImage{File: file, MediaURL: "/media?file=" + urlQueryEscape(file)})
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, nil
}

func isStoryboardImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	default:
		return false
	}
}

func collisionFreeName(dir, name string) string {
	name = filepath.Base(name)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := name
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
}

func writeBoardJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func urlQueryEscape(s string) string {
	r := strings.NewReplacer("%", "%25", " ", "%20", "#", "%23", "&", "%26", "+", "%2B", "?", "%3F", "=", "%3D")
	return r.Replace(s)
}

const boardHTML2 = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>milklily board</title>
<style>
:root { color-scheme: light; --bg:#c9c5b6; --paper:#f3efe3; --fg:#272720; --muted:#68685e; --line:#a9a394; --soft:#ded9ca; --drop:#3b3a33; --danger:#7e2730; --hi:#fffdf4; --shade:#b9b3a4; --well:#c8c5b9; --accent:#4d5c63; --preview-w:360px; }
* { box-sizing: border-box; }
html, body { height: 100%; }
body { margin: 0; background: linear-gradient(180deg, #d5d1c3 0, var(--bg) 100%); color: var(--fg); font: 15px/1.35 serif; letter-spacing: 0; overflow: hidden; }
body.resizing { cursor: col-resize; user-select: none; }
button, input, select { font: inherit; border: 1px solid var(--line); background: linear-gradient(180deg, #ebe6d8 0, var(--soft) 100%); color: var(--fg); border-radius: 0; padding: .34rem .48rem; box-shadow: inset 1px 1px 0 var(--hi), inset -1px -1px 0 var(--shade); }
button { cursor: pointer; display: inline-flex; align-items: center; justify-content: center; gap: .32rem; white-space: nowrap; }
button:hover, button.active { background: linear-gradient(180deg, #e3dfd1 0, #d0caba 100%); border-color: #858075; color: var(--fg); }
button.danger:hover { background: var(--danger); border-color: var(--danger); color: #fff; }
button:disabled { cursor: default; color: var(--muted); background: #d7d1c1; border-color: var(--line); box-shadow: inset 1px 1px 0 var(--hi); }
.icon { width: 1em; height: 1em; stroke: currentColor; fill: none; stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; flex: 0 0 auto; }
.icon-only { width: 1.9rem; min-width: 1.9rem; height: 1.9rem; padding: .18rem; gap: 0; }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
input { min-width: 14rem; outline: none; }
select { outline: none; }
input:focus, select:focus { border-color: var(--fg); }
select:disabled { color: var(--muted); background: var(--soft); }
.topbar { min-height: 54px; display: grid; grid-template-columns: auto auto auto 1fr auto; gap: .65rem 1rem; align-items: center; padding: .65rem .8rem; border-bottom: 1px solid #8f897b; background: linear-gradient(180deg, #eee9dc 0, #d9d3c3 100%); box-shadow: inset 0 1px 0 var(--hi), inset 0 -1px 0 var(--shade); }
.brand { font-weight: 700; letter-spacing: .01em; white-space: nowrap; }
.counts { color: var(--muted); white-space: nowrap; }
.scene-filter { color: var(--muted); white-space: nowrap; }
.transport { display: flex; gap: .35rem; align-items: center; flex-wrap: wrap; }
.audio-strip { display: flex; gap: .28rem; align-items: center; color: var(--muted); font-variant-numeric: tabular-nums; }
.audio-strip select { max-width: 13rem; min-width: 8rem; }
.audio-time { min-width: 4.8rem; text-align: right; }
.status { color: var(--muted); min-width: 5rem; text-align: right; }
.layout { height: calc(100vh - 55px); display: grid; grid-template-columns: 170px minmax(360px, 1fr) 10px var(--preview-w); overflow: hidden; }
.unsorted { border-right: 1px solid var(--line); background: linear-gradient(90deg, #e9e4d7 0, #dfdacb 100%); padding: .8rem .7rem; overflow: auto; box-shadow: inset -1px 0 0 var(--hi); }
.unsorted.drop-target { background: linear-gradient(90deg, #f5f1e5 0, #e5dfce 100%); outline: 2px solid #6b695f; outline-offset: -4px; }
.sequence { padding: .8rem 1rem; overflow: auto; background: linear-gradient(90deg, #ded9ca 0, #d6d1c1 100%); box-shadow: inset -1px 0 0 var(--hi); }
.splitter { cursor: col-resize; background: linear-gradient(90deg, transparent 0 45%, rgba(84,82,74,.38) 45% 55%, transparent 55% 100%); }
.splitter:hover, body.resizing .splitter { background: linear-gradient(90deg, transparent 0 42%, rgba(84,82,74,.58) 42% 58%, transparent 58% 100%); }
.preview-pane { background: linear-gradient(90deg, #d5d0c0 0, #ebe6d9 12%, #f1ede1 100%); padding: .8rem; overflow: auto; }
.unsorted, .sequence, .preview-pane { scrollbar-width: none; -ms-overflow-style: none; }
.unsorted::-webkit-scrollbar, .sequence::-webkit-scrollbar, .preview-pane::-webkit-scrollbar { width: 0; height: 0; display: none; }
.pane-title { font-weight: 700; letter-spacing: .03em; margin: 0 0 .8rem; color: #3f4039; }
.preview { position: sticky; top: 0; }
.preview-frame { position: relative; border: 1px solid #8d887c; background: #f7f3e9; aspect-ratio: 4 / 3; display: grid; place-items: center; overflow: hidden; box-shadow: inset 1px 1px 0 #918b7d, inset -1px -1px 0 var(--hi), 0 1px 0 rgba(255,255,255,.35); }
.preview-frame:fullscreen { width: 100vw; height: 100vh; aspect-ratio: auto; border: 0; background: #070706; box-shadow: none; }
.preview-frame img { width: 100%; height: 100%; object-fit: contain; display: block; }
.preview-empty { color: var(--muted); }
.preview-meta { position: relative; min-height: 2.8rem; margin-top: .8rem; padding-left: 3.1rem; color: var(--muted); font-variant-numeric: tabular-nums; }
.preview-meta::before { content: ""; position: absolute; left: 0; top: .15rem; width: 2.25rem; aspect-ratio: 1; border-radius: 50%; border: 1px solid #8f897d; background: radial-gradient(circle at 35% 30%, #f7f3e7 0 18%, #c9c4b5 19% 58%, #9f9a8e 59% 61%, #d8d2c3 62% 100%); box-shadow: inset 1px 1px 1px var(--hi), inset -1px -1px 1px #999386; }
.preview-meta::after { content: ""; position: absolute; left: 1.03rem; top: .32rem; width: 2px; height: .85rem; background: var(--accent); transform: rotate(34deg); transform-origin: bottom center; opacity: .72; }
.preview-meta strong { display: block; color: var(--fg); font-size: 1.1rem; margin-bottom: .35rem; }
.preview-tools { display: flex; gap: .35rem; flex-wrap: wrap; margin-top: .75rem; }
.preview-tools button { min-width: 3.4rem; padding: .26rem .4rem; font-variant-numeric: tabular-nums; }
.preview-tools button.icon-only { width: 1.9rem; min-width: 1.9rem; padding: .18rem; }
.preview-tools button.active { background: linear-gradient(180deg, #cfd5d0 0, #b9c2bd 100%); border-color: #66706e; color: var(--fg); }
.unsorted-list { display: grid; gap: .65rem; }
.unsorted-card { border: 1px solid var(--line); background: linear-gradient(180deg, #f4f0e5 0, #e6e1d3 100%); padding: .35rem; cursor: grab; box-shadow: inset 1px 1px 0 var(--hi), inset -1px -1px 0 var(--shade); }
.unsorted-card:hover { border-color: var(--fg); }
.unsorted-card img { width: 100%; aspect-ratio: 4 / 3; object-fit: contain; display: block; background: #fbfbf6; }
.unsorted-card .name { margin-top: .25rem; color: var(--muted); font-size: .72rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.scene { margin-bottom: 1.4rem; }
.scene-head { display: flex; align-items: baseline; gap: .7rem; margin-bottom: .85rem; }
.scene-title { font-weight: 700; letter-spacing: .02em; }
.scene-count { color: var(--muted); font-size: .9rem; }
.shot-grid { display: grid; grid-template-columns: 1fr; gap: 0; align-items: start; min-height: 184px; }
.shot { position: relative; min-width: 0; cursor: grab; display: grid; grid-template-columns: 2.7rem 220px minmax(110px, 1fr) 8.7rem; gap: .65rem; align-items: start; padding: .55rem 0; border-bottom: 1px solid var(--line); }
.shot:hover { background: rgba(255,253,244,.28); }
.shot.selected { background: linear-gradient(180deg, #f4f0e5 0, #e8e2d2 100%); }
.shot.selected .thumb { border-color: #66665d; outline: 1px solid #66665d; outline-offset: 1px; }
.shot.dragging { opacity: .35; }
.shot.drop-before::before, .shot.drop-after::after { content: ""; position: absolute; left: 0; right: 0; height: 6px; background: var(--drop); z-index: 2; }
.shot.drop-before::before { top: -4px; }
.shot.drop-after::after { bottom: -4px; }
.shot-num { grid-column: 1; grid-row: 1 / span 3; padding-top: .18rem; font-weight: 700; font-variant-numeric: tabular-nums; }
.thumb { border: 1px solid #b3ad9f; background: #fbf7ec; aspect-ratio: 4 / 3; display: grid; place-items: center; overflow: hidden; }
.shot .thumb { grid-column: 2; grid-row: 1 / span 3; width: 220px; }
.thumb img { width: 100%; height: 100%; object-fit: contain; display: block; }
.shot-name { grid-column: 3; margin-top: 0; color: var(--fg); font-size: .84rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.shot-head .shot-name { flex: 1; min-width: 0; }
.shot-time { display: grid; grid-template-columns: 1.8rem 4.2rem auto 1.8rem; gap: .24rem; align-items: stretch; margin-top: .35rem; }
.shot .shot-time { grid-column: 3; margin-top: 0; }
.dur-step { min-width: 0; padding: .12rem .2rem; font-weight: 700; line-height: 1; }
.dur-step.icon-only { width: 1.8rem; min-width: 0; }
.dur-text { min-width: 0; width: 4.2rem; padding: .18rem .3rem; text-align: right; font-variant-numeric: tabular-nums; }
.frame-read { align-self: center; color: var(--muted); font-size: .78rem; font-variant-numeric: tabular-nums; white-space: nowrap; }
.time-marks { grid-column: 2 / span 2; display: grid; grid-template-columns: repeat(6, 1fr); gap: 2px; height: 3px; opacity: .75; }
.time-marks i { display: block; background: #aaa394; }
.time-marks i.on { background: var(--accent); }
.shot-head { grid-column: 3; display: flex; gap: .45rem; align-items: center; min-width: 0; }
.dup { margin-left: auto; color: var(--muted); }
.motion-pad { display: grid; grid-template-columns: repeat(3, 1fr); grid-template-rows: repeat(3, 1.45rem); gap: .22rem; width: min(8.7rem, 100%); }
.motion-pad button { min-width: 0; padding: .05rem .18rem; font-size: .74rem; line-height: 1; font-variant-numeric: tabular-nums; }
.motion-pad button.icon-only { width: 100%; min-width: 0; height: 1.45rem; }
.motion-pad button .icon { width: .95rem; height: .95rem; }
.motion-pad .pan-mode { gap: .18rem; }
.motion-pad .pan-mode .icon { width: .78rem; height: .78rem; }
.motion-pad .pan-tb { grid-column: 2; grid-row: 1; }
.motion-pad .pan-lr { grid-column: 1; grid-row: 2; }
.motion-pad .pan-mode { grid-column: 2; grid-row: 2; }
.motion-pad .pan-rl { grid-column: 3; grid-row: 2; }
.motion-pad .pan-bt { grid-column: 2; grid-row: 3; }
.motion-pad button.active { background: linear-gradient(180deg, #cfd5d0 0, #b9c2bd 100%); border-color: #66706e; color: var(--fg); }
.motion-pad button:disabled { color: var(--muted); background: #d6d0c0; }
.row-motion { grid-column: 4; grid-row: 1 / span 3; }
.duration-presets { display: grid; grid-template-columns: repeat(4, 1fr); gap: .28rem; margin-top: .35rem; }
.preset { min-width: 0; padding: .16rem .2rem; font-size: .76rem; font-variant-numeric: tabular-nums; }
.preset.active { background: #cfcfc4; border-color: #77776d; color: var(--fg); }
.end-drop { border: 2px dashed var(--line); min-height: 74px; display: grid; place-items: center; color: var(--muted); background: rgba(255,255,255,.55); margin-top: .6rem; }
.end-drop.over { border-color: var(--fg); color: var(--fg); }
.empty { border: 2px dashed var(--line); min-height: 170px; display: grid; place-items: center; color: var(--muted); background: rgba(255,255,255,.55); }
.lightbox { position: fixed; inset: 0; display: none; grid-template-rows: 1fr auto; background: rgba(245,245,242,.97); z-index: 20; padding: 2rem; }
.lightbox.open { display: grid; }
.lightbox-frame { min-height: 0; display: grid; place-items: center; }
.lightbox img { max-width: 100%; max-height: 100%; object-fit: contain; background: #fff; border: 1px solid var(--line); }
.lightbox-bar { display: flex; justify-content: space-between; gap: 1rem; padding-top: 1rem; color: var(--muted); }
@media (max-width: 840px) {
  body { overflow: auto; }
  .topbar { grid-template-columns: 1fr; align-items: start; }
  .layout { height: auto; overflow: visible; grid-template-columns: 1fr; }
  .unsorted { border-right: 0; border-bottom: 1px solid var(--line); max-height: 220px; }
  .sequence { border-right: 0; border-bottom: 1px solid var(--line); }
  .splitter { display: none; }
  .preview-pane { min-height: 70vh; }
  .unsorted-list { grid-template-columns: repeat(auto-fill, minmax(100px, 1fr)); }
  .preview { position: static; }
  .shot { grid-template-columns: 2.6rem 130px minmax(0, 1fr); }
  .shot .thumb { width: 130px; }
  .row-motion { grid-column: 3; grid-row: auto; }
}

/* Neutral palette borrowed from the Pictogrep storyboard. Layout stays intact. */
:root { --bg:#f5f5f2; --paper:#fff; --fg:#171717; --muted:#666; --line:#c9c9c3; --soft:#eeeeea; --hi:#fff; --shade:#eeeeea; --well:#f5f5f2; --drop:#171717; --accent:#171717; --unsorted-w:280px; }
html, body, body { background:var(--bg); }
button, input, select { background:var(--paper); border-color:var(--line); box-shadow:none; }
button:hover, button.active { background:var(--soft); border-color:var(--line); color:var(--fg); }
.topbar { background:var(--paper); border-bottom-color:var(--line); box-shadow:none; }
.topbar { min-height:0; display:flex; gap:.55rem; padding:.55rem .75rem; align-items:center; }
.counts { margin-right:auto; }
#search { width:min(16rem, 28vw); min-width:8rem; }
.transport { flex:0 0 auto; flex-wrap:nowrap; }
.scene-filter, .audio-strip, #scene, #toUnsorted { display:none; }
.layout { grid-template-columns:var(--unsorted-w) 10px minmax(360px, 1fr) 10px var(--preview-w); }
.unsorted, .sequence, .preview-pane { background:var(--bg); box-shadow:none; }
.unsorted-list { grid-template-columns:1fr; }
.unsorted-card { min-width:0; overflow:visible; }
.unsorted-card img { height:auto; max-height:220px; aspect-ratio:auto; object-fit:contain; }
.unsorted.drop-target { background:var(--soft); outline-color:var(--fg); }
.preview-frame, .thumb, .unsorted-card, .unsorted-card img { background:var(--paper); border-color:var(--line); box-shadow:none; }
.unsorted-card.selected { border-color:var(--fg); outline:1px solid var(--fg); outline-offset:1px; }
.shot:hover, .shot.selected { background:var(--soft); }
.shot.selected .thumb { border-color:var(--fg); outline-color:var(--fg); }
.end-drop, .empty { background:var(--paper); border-color:var(--line); }
.preview-scrub { width:100%; margin:.7rem 0 .1rem; accent-color:var(--fg); }
.key-help { position:fixed; inset:0; z-index:30; display:grid; place-items:center; padding:1rem; background:rgba(245,245,242,.88); }
.key-help[hidden] { display:none; }
.key-help-card { position:relative; width:min(31rem, 100%); padding:1rem 1.15rem; border:1px solid var(--line); background:var(--paper); box-shadow:0 4px 14px rgba(0,0,0,.14); }
.key-help-card strong { display:block; margin-bottom:.7rem; }
.key-help-card dl { display:grid; grid-template-columns:9rem 1fr; gap:.35rem .8rem; margin:0; }
.key-help-card dt { font-family:monospace; font-weight:700; }
.key-help-card dd { margin:0; color:var(--muted); }
.key-help-close { position:absolute; top:.45rem; right:.45rem; min-width:1.75rem; padding:.1rem .35rem; font-size:1.2rem; }
@media (max-width:840px) {
  .topbar { flex-wrap:wrap; }
  #search { order:2; flex:1 1 100%; width:auto; }
  .counts { margin-right:0; }
}
</style>
</head>
<body>
<header class="topbar">
  <div class="brand" id="project">Project</div>
  <div class="counts" id="counts">0 images</div>
  <div class="scene-filter">Scene: ALL</div>
  <input id="search" type="search" placeholder="Search">
  <div class="transport">
    <button id="addImages">Add Images</button>
    <span class="audio-strip" title="Narration clock for syncing images">
      <select id="audioSelect"><option value="">Audio…</option></select>
      <button id="audioStart" class="icon-only" title="Audio start"></button>
      <button id="audioBack" class="icon-only" title="Back 5 seconds"></button>
      <button id="audioPlay" class="icon-only" title="Play audio"></button>
      <button id="audioForward" class="icon-only" title="Forward 5 seconds"></button>
      <button id="audioEnd" class="icon-only" title="Audio end"></button>
      <span id="audioTime" class="audio-time">00:00 / 00:00</span>
    </span>
    <button id="undo" title="Undo last edit (Ctrl+Z)" disabled>Undo</button>
    <button id="start">Start</button>
    <button id="prev">Prev</button>
    <button id="play">Play</button>
    <button id="next">Next</button>
    <button id="end">End</button>
    <button id="scene">+ Scene</button>
    <button id="export">Save EDL</button>
    <button id="toUnsorted" title="Remove selected shot from the EDL; the source image stays in the unsorted inbox">To Unsorted</button>
    <span class="status" id="status">Loading</span>
  </div>
  <input id="filePick" type="file" accept="image/png,image/jpeg,image/webp" multiple hidden>
  <audio id="audioPlayer" preload="metadata"></audio>
</header>
<main class="layout">
  <aside class="unsorted" id="unsortedPane">
    <div class="pane-title">UNSORTED</div>
    <div id="unsorted" class="unsorted-list"></div>
  </aside>
  <div class="splitter" id="unsortedSplitter" title="Drag to resize images"></div>
  <section class="sequence">
    <div class="pane-title">EDL</div>
    <div id="sequence"></div>
  </section>
  <div class="splitter" id="splitter" title="Drag to resize preview"></div>
  <aside class="preview-pane">
    <div class="pane-title">PREVIEW</div>
    <div class="preview">
      <div class="preview-frame" id="previewFrame"><div class="preview-empty">No shot selected</div></div>
      <div>
        <div class="preview-meta" id="previewMeta"></div>
        <div class="preview-tools" id="previewTools"></div>
        <input id="previewScrub" class="preview-scrub" type="range" min="0" max="0" step="0.01" value="0" aria-label="Preview timeline">
      </div>
    </div>
  </aside>
</main>
<div id="lightbox" class="lightbox">
  <div class="lightbox-frame"><img id="lightboxImg" alt=""></div>
  <div class="lightbox-bar"><span id="lightboxMeta"></span><span>Esc / Enter returns - Left / Right cycles</span></div>
</div>
<div id="keyHelp" class="key-help" hidden>
  <div class="key-help-card" role="dialog" aria-modal="true" aria-label="Keyboard shortcuts">
    <button id="closeKeyHelp" class="key-help-close" aria-label="Close keyboard shortcuts">×</button>
    <strong>Keyboard shortcuts</strong>
    <dl>
      <dt>Tab</dt><dd>switch between Images and EDL</dd>
      <dt>h / l</dt><dd>left / right in the current pane</dd>
      <dt>j / k</dt><dd>down / up (across image-grid rows)</dd>
      <dt>J / K</dt><dd>move selected EDL shot down / up</dd>
      <dt>U / I</dt><dd>shorten / lengthen selected EDL shot by 0.1s</dd>
      <dt>gg / G</dt><dd>first / last item in the current pane</dd>
      <dt>Space</dt><dd>play / pause preview</dd>
      <dt>Enter</dt><dd>add selected image, or open selected EDL shot</dd>
      <dt>x</dt><dd>return selected EDL shot to Images</dd>
      <dt>Ctrl+Z</dt><dd>undo</dd>
      <dt>/</dt><dd>focus search</dd>
      <dt>?</dt><dd>toggle this help</dd>
    </dl>
  </div>
</div>
<script>
var state = null;
var blocks = [];
var selectedId = null;
var draggingId = "";
var draggingFile = "";
var playTime = 0;
var playing = false;
var lastTick = 0;
var saveTimer = 0;
var lightboxOpen = false;
var previewFile = "";
var previewToolsKey = "";
var undoStack = [];
var maxUndo = 80;
var pendingG = false;
var pendingGTimer = 0;
var boardFocus = "sequence";
var selectedFile = "";

function qs(s) { return document.querySelector(s); }
var iconPaths = {
  "archive": '<polyline points="21 8 21 21 3 21 3 8"></polyline><rect x="1" y="3" width="22" height="5"></rect><line x1="10" y1="12" x2="14" y2="12"></line>',
  "arrow-down": '<path d="M12 5v14"></path><path d="m19 12-7 7-7-7"></path>',
  "arrow-left": '<path d="M19 12H5"></path><path d="m12 19-7-7 7-7"></path>',
  "arrow-right": '<path d="M5 12h14"></path><path d="m12 5 7 7-7 7"></path>',
  "arrow-up": '<path d="M12 19V5"></path><path d="m5 12 7-7 7 7"></path>',
  "copy": '<rect x="9" y="9" width="13" height="13"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>',
  "image-plus": '<rect x="3" y="5" width="18" height="14"></rect><circle cx="8.5" cy="10.5" r="1.5"></circle><path d="m21 15-5-5L5 21"></path><path d="M16 3v6"></path><path d="M13 6h6"></path>',
  "inbox": '<path d="M22 12h-6l-2 3h-4l-2-3H2"></path><path d="M5.5 5h13L22 12v6a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-6l3.5-7Z"></path>',
  "maximize": '<path d="M8 3H5a2 2 0 0 0-2 2v3"></path><path d="M21 8V5a2 2 0 0 0-2-2h-3"></path><path d="M3 16v3a2 2 0 0 0 2 2h3"></path><path d="M16 21h3a2 2 0 0 0 2-2v-3"></path>',
  "minus": '<path d="M5 12h14"></path>',
  "pause": '<path d="M8 5v14"></path><path d="M16 5v14"></path>',
  "play": '<polygon points="6 4 20 12 6 20 6 4"></polygon>',
  "plus": '<path d="M12 5v14"></path><path d="M5 12h14"></path>',
  "rewind": '<polygon points="11 19 2 12 11 5 11 19"></polygon><polygon points="22 19 13 12 22 5 22 19"></polygon>',
  "rotate-ccw": '<path d="M3 7v6h6"></path><path d="M21 17a9 9 0 0 0-15-6.7L3 13"></path>',
  "save": '<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z"></path><path d="M17 21v-8H7v8"></path><path d="M7 3v5h8"></path>',
  "skip-back": '<polygon points="19 20 9 12 19 4 19 20"></polygon><path d="M5 19V5"></path>',
  "skip-forward": '<polygon points="5 4 15 12 5 20 5 4"></polygon><path d="M19 5v14"></path>',
  "step-back": '<polygon points="19 20 9 12 19 4 19 20"></polygon>',
  "step-forward": '<polygon points="5 4 15 12 5 20 5 4"></polygon>',
  "wave": '<path d="M3 12c2-6 4-6 6 0s4 6 6 0 4-6 6 0"></path>',
  "wave-in": '<path d="M4 18c5-1 9-4 16-12"></path>',
  "wave-out": '<path d="M4 18c7-8 11-11 16-12"></path>',
  "line": '<path d="M4 18 20 6"></path>'
};
function icon(name) {
  return '<svg class="icon" aria-hidden="true" viewBox="0 0 24 24">' + (iconPaths[name] || "") + '</svg>';
}
function buttonHTML(iconName, label) {
  return icon(iconName) + '<span class="btn-text">' + escapeHTML(label) + '</span>';
}
function iconOnlyHTML(iconName, label) {
  return icon(iconName) + '<span class="sr-only">' + escapeHTML(label) + '</span>';
}
function setButtonContent(id, iconName, label) {
  var btn = qs("#" + id);
  if (!btn) return;
  btn.innerHTML = buttonHTML(iconName, label);
  btn.setAttribute("aria-label", label);
}
function initButtonIcons() {
  setButtonContent("addImages", "image-plus", "Add Images");
	setButtonContent("audioStart", "skip-back", "Audio start");
	setButtonContent("audioBack", "step-back", "Back 5 seconds");
	setButtonContent("audioPlay", "play", "Play audio");
	setButtonContent("audioForward", "step-forward", "Forward 5 seconds");
	setButtonContent("audioEnd", "skip-forward", "Audio end");
  setButtonContent("undo", "rotate-ccw", "Undo");
  setButtonContent("start", "skip-back", "Start");
  setButtonContent("prev", "step-back", "Prev");
  setPlayButton(false);
  setButtonContent("next", "step-forward", "Next");
  setButtonContent("end", "skip-forward", "End");
  setButtonContent("scene", "plus", "Scene");
  setButtonContent("export", "save", "Save EDL");
  setButtonContent("toUnsorted", "inbox", "To Unsorted");
}
function setPlayButton(isPlaying) {
  setButtonContent("play", isPlaying ? "pause" : "play", isPlaying ? "Pause" : "Play");
}
function setAudioPlayButton(isPlaying) {
  setButtonContent("audioPlay", isPlaying ? "pause" : "play", isPlaying ? "Pause audio" : "Play audio");
}
function clamp(n, lo, hi) { return Math.max(lo, Math.min(hi, n)); }
function mediaURL(file) { return "/media?file=" + encodeURIComponent(file); }
function stem(file) { return file.replace(/\.[^.]+$/, ""); }
function isShot(b) { return b && b.kind === "image"; }
function shots() { return blocks.filter(isShot); }
function selectedBlock() { return blocks.find(function(b) { return b.id === selectedId; }) || null; }
function secToTime(s) {
  var m = Math.floor(Math.max(0, s) / 60);
  var rest = (Math.max(0, s) - m * 60).toFixed(2).padStart(5, "0");
  return String(m).padStart(2, "0") + ":" + rest;
}
function audioTimeText(s) {
  if (!isFinite(s) || s < 0) s = 0;
  var m = Math.floor(s / 60);
  var rest = Math.floor(s % 60);
  return String(m).padStart(2, "0") + ":" + String(rest).padStart(2, "0");
}
function renderAudioStrip() {
  var select = qs("#audioSelect");
  var current = select.value;
  select.innerHTML = '<option value="">Audio…</option>' + (state.audio_files || []).map(function(a) {
    return '<option value="' + escapeHTML(a.file) + '">' + escapeHTML(a.file) + '</option>';
  }).join("");
  if ((state.audio_files || []).some(function(a) { return a.file === current; })) select.value = current;
  else if ((state.audio_files || []).length === 1) { select.value = state.audio_files[0].file; loadAudio(); }
  updateAudioClock();
}
function updateAudioClock() {
  var a = qs("#audioPlayer");
  qs("#audioTime").textContent = audioTimeText(a.currentTime) + " / " + audioTimeText(a.duration);
  setAudioPlayButton(!a.paused);
}
function loadAudio() {
  var file = qs("#audioSelect").value;
  var a = qs("#audioPlayer");
  a.pause();
  a.src = file ? mediaURL(file) : "";
  a.load();
  updateAudioClock();
}
function seekAudio(delta) {
  var a = qs("#audioPlayer");
  if (!a.src) return;
  a.currentTime = Math.max(0, Math.min(isFinite(a.duration) ? a.duration : Infinity, a.currentTime + delta));
}
function toggleAudio() {
  var a = qs("#audioPlayer");
  if (!a.src) return;
  if (a.paused) a.play(); else a.pause();
}
function durationOf(b) { return Number((b && b.item && b.item.Dur) || 0); }
function framesOf(b) { return Math.max(1, Math.round(durationOf(b) * state.fps)); }
function durationTicks(b) {
  var filled = clamp(Math.ceil(durationOf(b)), 1, 6);
  var html = "";
  for (var i = 1; i <= 6; i++) html += '<i class="' + (i <= filled ? "on" : "") + '"></i>';
  return html;
}
function cloneBlocks(src) {
  return JSON.parse(JSON.stringify(src || []));
}
function pushUndo() {
  if (!state) return;
  undoStack.push({blocks: cloneBlocks(blocks), selectedId: selectedId, playTime: playTime});
  if (undoStack.length > maxUndo) undoStack.shift();
  updateUndoButton();
}
function undoLastEdit() {
  if (!undoStack.length) {
    setStatus("Nothing to undo");
    return;
  }
  pause();
  var snap = undoStack.pop();
  blocks = cloneBlocks(snap.blocks);
  selectedId = snap.selectedId || null;
  playTime = Number(snap.playTime) || 0;
  recalc();
  render();
  saveSoon();
  setStatus("Undone");
  updateUndoButton();
}
function updateUndoButton() {
  var btn = qs("#undo");
  if (btn) btn.disabled = !undoStack.length;
}
function normalizedDuration(sec) {
  sec = Math.max(0.1, Number(sec) || 0.1);
  return Math.round(sec * 100) / 100;
}
function setDuration(b, sec) {
  if (!isShot(b)) return;
  b.item.Dur = normalizedDuration(sec);
  b.duration = b.item.Dur;
  b.frames = Math.max(1, Math.round(b.duration * state.fps));
}
function changeDuration(b, sec) {
  if (!isShot(b)) return false;
  var next = normalizedDuration(sec);
  if (Math.abs(durationOf(b) - next) < 0.001) return false;
  pushUndo();
  setDuration(b, next);
  recalc();
  render();
  saveSoon();
  return true;
}
function adjustDuration(b, delta) {
  if (!isShot(b)) return;
  changeDuration(b, durationOf(b) + delta);
}
function applyDurationToSelected(sec) {
  var b = selectedBlock() || blockAt(playTime);
  if (!isShot(b)) return;
  var changedSelection = selectedId !== b.id;
  selectedId = b.id;
  if (!changeDuration(b, sec) && changedSelection) render();
}
function nudgeSelectedDuration(delta) {
  var b = selectedBlock() || blockAt(playTime);
  if (!isShot(b)) return;
  selectedId = b.id;
  adjustDuration(b, delta);
}
function duplicateShot(b) {
  if (!isShot(b)) return;
  var idx = blocks.findIndex(function(x) { return x.id === b.id; });
  if (idx < 0) return;
  pushUndo();
  var copy = {
    id: "n" + Date.now() + Math.floor(Math.random() * 1000),
    kind: "image",
    media_url: b.media_url,
    item: Object.assign({}, b.item)
  };
  blocks.splice(idx + 1, 0, copy);
  selectedId = copy.id;
  recalc();
  render();
  saveSoon();
}
function duplicateSelected() {
  var b = selectedBlock() || blockAt(playTime);
  duplicateShot(b);
}
function previewWidthBounds() {
  var layout = qs(".layout");
  var w = layout ? layout.clientWidth : window.innerWidth;
  return {min: 260, max: Math.max(260, w - unsortedWidth() - 20 - 360)};
}
function unsortedWidth() {
  var value = getComputedStyle(document.documentElement).getPropertyValue("--unsorted-w");
  return parseFloat(value) || 280;
}
function unsortedWidthBounds() {
  var layout = qs(".layout");
  var w = layout ? layout.clientWidth : window.innerWidth;
  var preview = parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--preview-w")) || 360;
  return {min: 140, max: Math.max(140, w - preview - 20 - 360)};
}
function setUnsortedWidth(px, persist) {
  var bounds = unsortedWidthBounds();
  px = clamp(Math.round(Number(px) || 280), bounds.min, bounds.max);
  document.documentElement.style.setProperty("--unsorted-w", px + "px");
  if (persist) {
    try { localStorage.setItem("milklily.unsortedWidth", String(px)); } catch (e) {}
  }
}
function setPreviewWidth(px, persist) {
  var bounds = previewWidthBounds();
  px = clamp(Math.round(Number(px) || 360), bounds.min, bounds.max);
  document.documentElement.style.setProperty("--preview-w", px + "px");
  if (persist) {
    try { localStorage.setItem("milklily.previewWidth", String(px)); } catch (e) {}
  }
  renderPreview();
}
function initPreviewWidth() {
  var saved = "";
  try { saved = localStorage.getItem("milklily.previewWidth") || ""; } catch (e) {}
  if (saved) setPreviewWidth(Number(saved), false);
  var unsorted = "";
  try { unsorted = localStorage.getItem("milklily.unsortedWidth") || ""; } catch (e) {}
  if (unsorted) setUnsortedWidth(Number(unsorted), false);
}
function bindPreviewSplitter() {
  var splitter = qs("#splitter");
  var layout = qs(".layout");
  if (!splitter || !layout) return;
  splitter.addEventListener("pointerdown", function(e) {
    e.preventDefault();
    document.body.classList.add("resizing");
    splitter.setPointerCapture(e.pointerId);
  });
  splitter.addEventListener("pointermove", function(e) {
    if (!document.body.classList.contains("resizing")) return;
    var rect = layout.getBoundingClientRect();
    setPreviewWidth(rect.right - e.clientX, true);
  });
  function end(e) {
    if (!document.body.classList.contains("resizing")) return;
    document.body.classList.remove("resizing");
    try { splitter.releasePointerCapture(e.pointerId); } catch (err) {}
  }
  splitter.addEventListener("pointerup", end);
  splitter.addEventListener("pointercancel", end);
  splitter.addEventListener("dblclick", function() { setPreviewWidth(360, true); });
  window.addEventListener("resize", function() {
    var current = getComputedStyle(document.documentElement).getPropertyValue("--preview-w");
    setPreviewWidth(parseFloat(current) || 360, false);
  });
}
function bindUnsortedSplitter() {
  var splitter = qs("#unsortedSplitter");
  var layout = qs(".layout");
  if (!splitter || !layout) return;
  splitter.addEventListener("pointerdown", function(e) {
    e.preventDefault();
    document.body.classList.add("resizing");
    splitter.setPointerCapture(e.pointerId);
  });
  splitter.addEventListener("pointermove", function(e) {
    if (!document.body.classList.contains("resizing")) return;
    var rect = layout.getBoundingClientRect();
    setUnsortedWidth(e.clientX - rect.left, true);
  });
  function end(e) {
    if (!document.body.classList.contains("resizing")) return;
    document.body.classList.remove("resizing");
    try { splitter.releasePointerCapture(e.pointerId); } catch (err) {}
  }
  splitter.addEventListener("pointerup", end);
  splitter.addEventListener("pointercancel", end);
  splitter.addEventListener("dblclick", function() { setUnsortedWidth(280, true); });
  window.addEventListener("resize", function() { setUnsortedWidth(unsortedWidth(), false); });
}
function fullscreenPreview() {
  var frame = qs("#previewFrame");
  if (!frame) return;
  if (document.fullscreenElement) {
    document.exitFullscreen && document.exitFullscreen();
    return;
  }
  if (frame.requestFullscreen) {
    frame.requestFullscreen().then(function() { setTimeout(renderPreview, 60); }).catch(openLightbox);
  } else {
    openLightbox();
  }
}
function noteOf(b) { return (b && b.item && b.item.Note) || ""; }
function panOf(note) {
  var m = String(note || "").toLowerCase().match(/#pan_(lr|rl|tb|bt)\b/);
  return m ? m[1] : "";
}
function easeOf(note) {
  note = String(note || "");
  if (/#ease_inout\b/i.test(note)) return "inout";
  if (/#ease_out\b/i.test(note)) return "out";
  if (/#ease_in\b/i.test(note)) return "in";
  return "linear";
}
function stripMotion(note) {
  return String(note || "")
    .replace(/#pan_(lr|rl|tb|bt)\b/gi, "")
    .replace(/#ease_(linear|in|out|inout)\b/gi, "")
    .replace(/\s+/g, " ")
    .trim();
}
function normalizeEase(ease) {
  return ease === "in" || ease === "out" || ease === "inout" ? ease : "linear";
}
function setMotion(b, pan, ease) {
  if (!isShot(b)) return;
  var base = stripMotion(b.item.Note);
  if (pan) {
    base = (base + " #pan_" + pan + " #ease_" + normalizeEase(ease)).trim();
  }
  b.item.Note = base;
}
function easeLabel(ease) {
  if (ease === "in") return "ease in";
  if (ease === "out") return "ease out";
  if (ease === "inout") return "ease in/out";
  return "linear";
}
function easeShort(ease) {
  if (ease === "in") return "In";
  if (ease === "out") return "Out";
  if (ease === "inout") return "Both";
  return "Lin";
}
function easeIcon(ease) {
  if (ease === "in") return "wave-in";
  if (ease === "out") return "wave-out";
  if (ease === "inout") return "wave";
  return "line";
}
function nextEase(ease) {
  var modes = ["linear", "in", "out", "inout"];
  var idx = modes.indexOf(normalizeEase(ease));
  return modes[(idx + 1) % modes.length];
}
function motionLabel(b) {
  var pan = panOf(noteOf(b));
  if (!pan) return "";
  var dir = {lr:"pan L->R", rl:"pan R->L", tb:"pan T->B", bt:"pan B->T"}[pan] || "pan";
  return dir + " " + easeLabel(easeOf(noteOf(b)));
}
function motionTitle(value) {
  return {
    lr: "Pan from left side to right side",
    rl: "Pan from right side to left side",
    tb: "Pan from top to bottom",
    bt: "Pan from bottom to top"
  }[value] || "Pan motion";
}
function motionPadButton(cls, value, label, selected) {
  var ico = {lr:"arrow-right", rl:"arrow-left", tb:"arrow-down", bt:"arrow-up"}[value] || "arrow-right";
  return '<button class="icon-only ' + cls + (selected === value ? " active" : "") + '" data-pan="' + value + '" title="' + motionTitle(value) + '">' + iconOnlyHTML(ico, label) + '</button>';
}
function motionButtonsHTML(b) {
  var pan = panOf(noteOf(b));
  var ease = easeOf(noteOf(b));
  var mode = pan ? easeShort(ease) : "Still";
  var modeIcon = pan ? easeIcon(ease) : "line";
  return '<div class="motion-pad">' +
    motionPadButton("pan-tb", "tb", "Down", pan) +
    motionPadButton("pan-lr", "lr", "Right", pan) +
    '<button class="pan-mode' + (ease !== "linear" ? " active" : "") + '" data-ease="cycle"' + (pan ? "" : " disabled") + ' title="Cycle pan timing">' + icon(modeIcon) + '<span>' + mode + '</span></button>' +
    motionPadButton("pan-rl", "rl", "Left", pan) +
    motionPadButton("pan-bt", "bt", "Up", pan) +
    '</div>';
}
function bindMotionButtons(root, b) {
  root.querySelectorAll("[data-pan]").forEach(function(btn) {
    btn.addEventListener("click", function(e) {
      if (e) e.stopPropagation();
      var nextPan = btn.dataset.pan || "";
      if (panOf(noteOf(b)) === nextPan) nextPan = "";
      var nextEase = nextPan ? easeOf(noteOf(b)) : "linear";
      pushUndo();
      setMotion(b, nextPan, nextEase);
      recalc();
      render();
      saveSoon();
    });
    btn.addEventListener("mousedown", function(e) { e.stopPropagation(); });
  });
  root.querySelectorAll("[data-ease]").forEach(function(btn) {
    btn.addEventListener("click", function(e) {
      if (e) e.stopPropagation();
      var pan = panOf(noteOf(b));
      if (!pan) return;
      pushUndo();
      setMotion(b, pan, nextEase(easeOf(noteOf(b))));
      recalc();
      render();
      saveSoon();
    });
    btn.addEventListener("mousedown", function(e) { e.stopPropagation(); });
  });
}
var cardDurationPresets = [0.5, 1, 2, 3];
var previewDurationPresets = [0.5, 1, 1.5, 2, 3, 4, 6, 8];
function presetLabel(sec) {
  return sec < 1 ? "1/2s" : (sec % 1 === 0 ? String(sec) + "s" : sec.toFixed(1) + "s");
}
function presetHTML(b) {
  var cur = durationOf(b);
  return cardDurationPresets.map(function(sec) {
    var active = Math.abs(cur - sec) < 0.01 ? " active" : "";
    return '<button class="preset' + active + '" data-sec="' + sec + '">' + presetLabel(sec) + '</button>';
  }).join("");
}
function recalc() {
  var t = 0, n = 0;
  blocks.forEach(function(b) {
    if (!isShot(b)) return;
    n += 1;
    b.index = n;
    b.start = t;
    b.duration = durationOf(b);
    b.frames = Math.max(1, Math.round(b.duration * state.fps));
    t += b.duration;
  });
  state.total_seconds = t;
  state.total_frames = Math.round(t * state.fps);
  refreshUsed();
}
function refreshUsed() {
  var used = {};
  shots().forEach(function(b) { used[b.item.File] = true; });
  (state.footage_images || []).forEach(function(img) { img.used = !!used[img.file]; });
}
async function load() {
  var res = await fetch("/api/sequence");
  if (!res.ok) throw new Error(await res.text());
  state = await res.json();
  blocks = state.blocks || [];
  undoStack = [];
  recalc();
  selectedId = selectedId || (shots()[0] && shots()[0].id) || null;
  render();
  renderAudioStrip();
  updateUndoButton();
  setStatus("Saved");
}
async function uploadFiles(files) {
  files = Array.from(files || []).filter(function(f) { return f && f.type && f.type.startsWith("image/"); });
  if (!files.length) return;
  setStatus("Adding");
  var form = new FormData();
  files.forEach(function(f) { form.append("images", f, f.name); });
  var res = await fetch("/api/upload-images", {method: "POST", body: form});
  if (!res.ok) {
    setStatus("Error");
    console.error(await res.text());
    return;
  }
  var data = await res.json();
  state = data;
  blocks = state.blocks || [];
  recalc();
  render();
  renderAudioStrip();
  setStatus("Added " + data.imported);
}
function render() {
  updateCounts();
  renderUnsorted();
  renderSequence();
  renderPreview();
  renderLightbox();
}
function updateCounts() {
  var placed = shots().length;
  var total = (state.footage_images || []).length;
  var unsorted = (state.footage_images || []).filter(function(i) { return !i.used; }).length;
  qs("#project").textContent = state.project || "Project";
  qs("#counts").textContent = total + " images   Placed: " + placed + "   Unsorted: " + unsorted + "   " + secToTime(playTime);
}
function query() { return qs("#search").value.trim().toLowerCase(); }
function matchesText() {
  var q = query();
  return function(file, note) {
    if (!q) return true;
    return String(file || "").toLowerCase().includes(q) || String(note || "").toLowerCase().includes(q);
  };
}
function renderUnsorted() {
  var list = qs("#unsorted");
  list.innerHTML = "";
  var match = matchesText();
  (state.footage_images || []).filter(function(img) { return !img.used && match(img.file, ""); }).forEach(function(img) {
    var el = document.createElement("div");
    el.className = "unsorted-card" + (img.file === selectedFile ? " selected" : "");
    el.draggable = true;
    el.dataset.file = img.file;
    el.innerHTML = '<img src="' + img.media_url + '" loading="lazy" alt=""><div class="name">' + escapeHTML(img.file) + '</div>';
    el.addEventListener("dragstart", function(e) {
      draggingFile = img.file;
      draggingId = "";
      if (e.dataTransfer) {
        e.dataTransfer.effectAllowed = "copy";
        e.dataTransfer.setData("text/plain", img.file);
      }
    });
    el.addEventListener("click", function() {
      boardFocus = "unsorted";
      selectedFile = img.file;
      renderUnsorted();
    });
    el.addEventListener("dblclick", function() { addImage(img.file, null, false); });
    list.appendChild(el);
  });
  if (!list.children.length) {
    list.innerHTML = '<div class="empty">No unsorted images</div>';
  }
}
function sceneGroups() {
  var groups = [];
  var current = {title: "SCENE 01 - ALL", blocks: []};
  var sceneNum = 1;
  blocks.forEach(function(b) {
    if (b.kind === "section") {
      if (current.blocks.length) groups.push(current);
      sceneNum += groups.length ? 1 : 0;
      current = {title: "SCENE " + String(sceneNum).padStart(2, "0") + " - " + (b.item.Note || "UNTITLED"), blocks: []};
      return;
    }
    if (isShot(b)) current.blocks.push(b);
  });
  groups.push(current);
  return groups;
}
function renderSequence() {
  var root = qs("#sequence");
  root.innerHTML = "";
  var match = matchesText();
  sceneGroups().forEach(function(group) {
    var section = document.createElement("section");
    section.className = "scene";
    var visible = group.blocks.filter(function(b) { return match(b.item.File, b.item.Note); });
    section.innerHTML = '<div class="scene-head"><div class="scene-title">' + escapeHTML(group.title) + '</div><div class="scene-count">' + group.blocks.length + ' shots</div></div>';
    var grid = document.createElement("div");
    grid.className = "shot-grid";
    grid.addEventListener("dragover", function(e) { e.preventDefault(); });
    grid.addEventListener("drop", function(e) {
      e.preventDefault();
      if (draggingFile) addImage(draggingFile, null, false);
    });
    visible.forEach(function(b) { grid.appendChild(shotNode(b)); });
    grid.appendChild(endDropNode(group.blocks.length ? group.blocks[group.blocks.length - 1].id : null));
    section.appendChild(grid);
    root.appendChild(section);
  });
}
function shotNode(b) {
  var el = document.createElement("div");
  var seconds = durationOf(b);
  el.className = "shot" + (b.id === selectedId ? " selected" : "");
  el.draggable = true;
  el.dataset.id = b.id;
  el.innerHTML =
    '<div class="shot-num">' + String(b.index).padStart(3, "0") + '</div>' +
    '<div class="thumb"><img src="' + b.media_url + '" loading="lazy" alt=""></div>' +
    '<div class="shot-head"><div class="shot-name">' + escapeHTML(b.item.File) + '</div><button class="dup icon-only" title="Duplicate shot">' + iconOnlyHTML("copy", "Duplicate shot") + '</button></div>' +
    '<div class="shot-time">' +
      '<button class="dur-step icon-only" data-d="-0.25" title="Shorter">' + iconOnlyHTML("minus", "Shorter") + '</button>' +
      '<input class="dur-text" type="text" inputmode="decimal" value="' + seconds.toFixed(2) + '" title="Duration in seconds">' +
      '<span class="frame-read">' + framesOf(b) + 'f</span>' +
      '<button class="dur-step icon-only" data-d="0.25" title="Longer">' + iconOnlyHTML("plus", "Longer") + '</button>' +
      '<span class="time-marks">' + durationTicks(b) + '</span>' +
    '</div>' +
    '<div class="row-motion">' + motionButtonsHTML(b) + '</div>';
  var durInput = el.querySelector(".dur-text");
  var dup = el.querySelector(".dup");
  dup.addEventListener("click", function(e) {
    e.stopPropagation();
    duplicateShot(b);
  });
  dup.addEventListener("mousedown", function(e) { e.stopPropagation(); });
  function commitDurationInput() {
    if (!changeDuration(b, durInput.value)) {
      durInput.value = durationOf(b).toFixed(2);
    }
  }
  durInput.addEventListener("click", function(e) { e.stopPropagation(); });
  durInput.addEventListener("mousedown", function(e) { e.stopPropagation(); });
  durInput.addEventListener("change", commitDurationInput);
  durInput.addEventListener("blur", commitDurationInput);
  durInput.addEventListener("keydown", function(e) {
    if (e.key === "Enter") {
      e.preventDefault();
      durInput.blur();
    } else if (e.key === "Escape") {
      e.preventDefault();
      durInput.value = durationOf(b).toFixed(2);
      durInput.blur();
    }
  });
  el.querySelectorAll(".dur-step").forEach(function(btn) {
    btn.addEventListener("click", function(e) {
      e.stopPropagation();
      adjustDuration(b, Number(btn.dataset.d) || 0);
    });
    btn.addEventListener("mousedown", function(e) { e.stopPropagation(); });
  });
  bindMotionButtons(el.querySelector(".row-motion"), b);
  el.addEventListener("click", function(e) { select(b.id, !e.shiftKey); });
  el.addEventListener("dblclick", function() { openLightbox(); });
  el.addEventListener("dragstart", function(e) {
    draggingId = b.id;
    draggingFile = "";
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/plain", b.id);
    }
    el.classList.add("dragging");
  });
  el.addEventListener("dragend", function() {
    draggingId = "";
    draggingFile = "";
    clearDrops();
    qs("#unsortedPane").classList.remove("drop-target");
    el.classList.remove("dragging");
  });
  el.addEventListener("dragover", function(e) {
    e.preventDefault();
    clearDrops();
    el.classList.add(dropAfter(e, el) ? "drop-after" : "drop-before");
  });
  el.addEventListener("dragleave", clearDrops);
  el.addEventListener("drop", function(e) {
    e.preventDefault();
    var after = dropAfter(e, el);
    clearDrops();
    if (draggingFile) addImage(draggingFile, b.id, after);
    else moveBlock(draggingId, b.id, after);
  });
  return el;
}
function endDropNode(afterId) {
  var el = document.createElement("div");
  el.className = "end-drop";
  el.textContent = "DROP HERE";
  el.addEventListener("dragover", function(e) { e.preventDefault(); el.classList.add("over"); });
  el.addEventListener("dragleave", function() { el.classList.remove("over"); });
  el.addEventListener("drop", function(e) {
    e.preventDefault();
    el.classList.remove("over");
    if (draggingFile) addImage(draggingFile, afterId, true);
    else if (afterId) moveBlock(draggingId, afterId, true);
  });
  return el;
}
function renderPreview() {
  var frame = qs("#previewFrame");
  var meta = qs("#previewMeta");
  var scrub = qs("#previewScrub");
  scrub.max = String(Math.max(0, state.total_seconds || 0));
  scrub.value = String(Math.max(0, Math.min(state.total_seconds || 0, playTime)));
  var b = selectedBlock() || blockAt(playTime);
  if (!b) {
    previewFile = "";
    previewToolsKey = "";
    frame.innerHTML = '<div class="preview-empty">No shot selected</div>';
    meta.textContent = "";
    qs("#previewTools").innerHTML = "";
    return;
  }
  var local = Math.max(0, Math.min(durationOf(b), playTime - (b.start || 0)));
  if (previewFile !== b.item.File) {
    previewFile = b.item.File;
    frame.innerHTML = "";
    var img = document.createElement("img");
    img.src = b.media_url;
    img.alt = b.item.File;
    img.addEventListener("load", function() {
      if (previewFile === b.item.File) applyPreviewMotion(b, local);
    }, {once: true});
    frame.appendChild(img);
  }
  applyPreviewMotion(b, local);
  var motion = motionLabel(b);
  meta.innerHTML =
    '<strong>SHOT ' + String(b.index).padStart(3, "0") + '</strong>' +
    escapeHTML(b.item.File) + '<br>' +
    durationOf(b).toFixed(2) + ' sec / ' + Math.max(1, Math.round(durationOf(b) * state.fps)) + ' frames<br>' +
    'time ' + secToTime(playTime) + ' / ' + secToTime(state.total_seconds || 0) + '<br>' +
    'shot +' + local.toFixed(2) + ' sec' +
    (motion ? '<br>' + escapeHTML(motion) : '');
  renderPreviewTools(b);
}
function renderPreviewTools(b) {
  var root = qs("#previewTools");
  var key = b.id + "|" + durationOf(b);
  if (previewToolsKey === key) return;
  previewToolsKey = key;
  var cur = durationOf(b);
  root.innerHTML = '<button class="icon-only" data-action="full" title="Fullscreen preview">' + iconOnlyHTML("maximize", "Fullscreen preview") + '</button>' +
    '<button class="icon-only" data-action="dup" title="Duplicate shot">' + iconOnlyHTML("copy", "Duplicate shot") + '</button>' +
    '<button data-d="-0.25" title="Shorter by 1/4 second">' + icon("minus") + '<span>1/4</span></button>' +
    previewDurationPresets.map(function(sec) {
      var active = Math.abs(cur - sec) < 0.01 ? " active" : "";
      return '<button class="' + active + '" data-sec="' + sec + '">' + presetLabel(sec) + '</button>';
    }).join("") +
    '<button data-d="0.25" title="Longer by 1/4 second">' + icon("plus") + '<span>1/4</span></button>';
  root.querySelectorAll("[data-sec]").forEach(function(btn) {
    btn.addEventListener("click", function() { applyDurationToSelected(Number(btn.dataset.sec)); });
  });
  root.querySelectorAll("[data-d]").forEach(function(btn) {
    btn.addEventListener("click", function() { nudgeSelectedDuration(Number(btn.dataset.d) || 0); });
  });
  root.querySelectorAll("[data-action='dup']").forEach(function(btn) {
    btn.addEventListener("click", duplicateSelected);
  });
  root.querySelectorAll("[data-action='full']").forEach(function(btn) {
    btn.addEventListener("click", fullscreenPreview);
  });
}
function clearPreviewMotion(img) {
  img.style.position = "";
  img.style.left = "";
  img.style.top = "";
  img.style.width = "";
  img.style.height = "";
  img.style.maxWidth = "";
  img.style.maxHeight = "";
  img.style.objectFit = "";
  img.style.transform = "";
  img.style.willChange = "";
}
function previewProgress(b, local) {
  var dur = Math.max(0.001, durationOf(b));
  var p = Math.max(0, Math.min(1, local / dur));
  var ease = easeOf(noteOf(b));
  if (ease === "in") p = p * p;
  else if (ease === "out") p = 1 - (1 - p) * (1 - p);
  else if (ease === "inout") p = 0.5 - 0.5 * Math.cos(Math.PI * p);
  return p;
}
function applyPreviewMotion(b, local) {
  var frame = qs("#previewFrame");
  var img = frame && frame.querySelector("img");
  if (!img) return;
  clearPreviewMotion(img);
  var pan = panOf(noteOf(b));
  if (!pan) return;
  if (!img.naturalWidth || !img.naturalHeight || !frame.clientWidth || !frame.clientHeight) return;

  var scale = Math.max(frame.clientWidth / img.naturalWidth, frame.clientHeight / img.naturalHeight);
  var sw = Math.ceil(img.naturalWidth * scale);
  var sh = Math.ceil(img.naturalHeight * scale);
  var dx = Math.max(0, sw - frame.clientWidth);
  var dy = Math.max(0, sh - frame.clientHeight);
  var p = previewProgress(b, local);
  var x = -dx / 2;
  var y = -dy / 2;
  if (pan === "lr") x = -dx * p;
  else if (pan === "rl") x = -dx * (1 - p);
  else if (pan === "tb") y = -dy * p;
  else if (pan === "bt") y = -dy * (1 - p);

  img.style.position = "absolute";
  img.style.left = "0";
  img.style.top = "0";
  img.style.width = sw + "px";
  img.style.height = sh + "px";
  img.style.maxWidth = "none";
  img.style.maxHeight = "none";
  img.style.objectFit = "fill";
  img.style.willChange = "transform";
  img.style.transform = "translate(" + x.toFixed(2) + "px, " + y.toFixed(2) + "px)";
}
function dropAfter(e, el) { return e.clientY > el.getBoundingClientRect().top + el.offsetHeight / 2; }
function clearDrops() { document.querySelectorAll(".drop-before,.drop-after").forEach(function(n) { n.classList.remove("drop-before", "drop-after"); }); }
function addImage(file, targetId, after) {
  if (!file || shots().some(function(b) { return b.item.File === file; })) return;
  pushUndo();
  var b = {
    id: "n" + Date.now() + Math.floor(Math.random() * 1000),
    kind: "image",
    media_url: mediaURL(file),
    item: {Kind: "image", File: file, Dur: state.default_frames / state.fps, Note: stem(file)}
  };
  var idx = targetId ? blocks.findIndex(function(x) { return x.id === targetId; }) : -1;
  if (idx < 0) blocks.push(b);
  else blocks.splice(after ? idx + 1 : idx, 0, b);
  selectedId = b.id;
  selectedFile = "";
  boardFocus = "sequence";
  recalc();
  render();
  saveSoon();
}
function moveBlock(fromId, toId, after) {
  if (!fromId || !toId || fromId === toId) return;
  var from = blocks.findIndex(function(b) { return b.id === fromId; });
  var to = blocks.findIndex(function(b) { return b.id === toId; });
  if (from < 0 || to < 0) return;
  pushUndo();
  var moved = blocks.splice(from, 1)[0];
  if (to > from) to -= 1;
  blocks.splice(after ? to + 1 : to, 0, moved);
  recalc();
  render();
  saveSoon();
}
function moveSelected(dir) {
  var ss = shots();
  var idx = ss.findIndex(function(b) { return b.id === selectedId; });
  var next = idx + dir;
  if (idx < 0 || next < 0 || next >= ss.length) return;
  moveBlock(selectedId, ss[next].id, dir > 0);
  select(selectedId, false);
}
function unsortedFiles() {
  var match = matchesText();
  return (state.footage_images || []).filter(function(img) { return !img.used && match(img.file, ""); }).map(function(img) { return img.file; });
}
function unsortedColumns() {
  var list = qs("#unsorted");
  var cols = getComputedStyle(list).gridTemplateColumns.trim().split(/\s+/).filter(Boolean).length;
  return Math.max(1, cols || 1);
}
function moveUnsorted(delta) {
  var files = unsortedFiles();
  if (!files.length) return;
  var idx = files.indexOf(selectedFile);
  if (idx < 0) idx = 0;
  else idx = Math.max(0, Math.min(files.length - 1, idx + delta));
  selectedFile = files[idx];
  boardFocus = "unsorted";
  renderUnsorted();
  var node = qs("#unsorted [data-file='" + CSS.escape(selectedFile) + "']");
  if (node) node.scrollIntoView({block: "nearest", inline: "nearest"});
}
function toggleBoardFocus() {
  if (boardFocus === "sequence") {
    boardFocus = "unsorted";
    if (!selectedFile) selectedFile = unsortedFiles()[0] || "";
    renderUnsorted();
  } else {
    boardFocus = "sequence";
    renderUnsorted();
  }
}
function goUnsortedStart() {
  var files = unsortedFiles();
  if (!files.length) return;
  selectedFile = files[0];
  boardFocus = "unsorted";
  renderUnsorted();
}
function goUnsortedEnd() {
  var files = unsortedFiles();
  if (!files.length) return;
  selectedFile = files[files.length - 1];
  boardFocus = "unsorted";
  renderUnsorted();
}
function select(id, scroll) {
  selectedId = id;
  boardFocus = "sequence";
  var b = selectedBlock();
  if (b) playTime = b.start || 0;
  var audio = qs("#audioPlayer");
  if (b && audio.src) audio.currentTime = b.start || 0;
  render();
  if (scroll !== false) {
    var node = document.querySelector("[data-id='" + id + "']");
    if (node) node.scrollIntoView({block: "nearest", inline: "nearest"});
  }
}
function nudgeDuration(delta) {
  nudgeSelectedDuration(delta);
}
function selectedIndex() {
  var ss = shots();
  return ss.findIndex(function(b) { return b.id === selectedId; });
}
function selectIndex(idx) {
  var ss = shots();
  if (!ss.length) return;
  idx = Math.max(0, Math.min(ss.length - 1, idx));
  select(ss[idx].id);
}
function goStart() { pause(); selectIndex(0); }
function goEnd() { pause(); selectIndex(shots().length - 1); }
function stepSelection(dir) {
  var idx = selectedIndex();
  selectIndex((idx < 0 ? 0 : idx) + dir);
}
function blockAt(t) {
  var ss = shots();
  for (var i = 0; i < ss.length; i++) {
    if (t >= ss[i].start && t < ss[i].start + ss[i].duration) return ss[i];
  }
  return ss[ss.length - 1] || null;
}
function togglePlay() {
  if (playing) { pause(); return; }
  if (playTime >= state.total_seconds) playTime = 0;
  playing = true;
  lastTick = performance.now();
  setPlayButton(true);
  requestAnimationFrame(tick);
}
function pause() {
  playing = false;
  setPlayButton(false);
}
function tick(now) {
  if (!playing) return;
  playTime += (now - lastTick) / 1000;
  lastTick = now;
  if (playTime >= state.total_seconds) {
    playTime = state.total_seconds;
    pause();
  }
  var b = blockAt(playTime);
  if (b && b.id !== selectedId) {
    selectedId = b.id;
    renderSequence();
  }
  updateCounts();
  renderPreview();
  renderLightbox();
  if (playing) requestAnimationFrame(tick);
}
function sendBlockToUnsorted(id) {
  var idx = blocks.findIndex(function(b) { return b.id === id; });
  if (idx < 0) return;
  pushUndo();
  blocks.splice(idx, 1);
  selectedId = (shots()[Math.min(idx, shots().length - 1)] || shots()[0] || {}).id || null;
  recalc();
  render();
  saveSoon();
  setStatus("Moved to unsorted");
}
function sendSelectedToUnsorted() {
  sendBlockToUnsorted(selectedId);
}
function addScene() {
  var title = prompt("Scene title", "NEW SCENE");
  if (title === null) return;
  var idx = blocks.findIndex(function(b) { return b.id === selectedId; });
  if (idx < 0) idx = blocks.length;
  pushUndo();
  blocks.splice(idx, 0, {
    id: "s" + Date.now(),
    kind: "section",
    item: {Kind: "section", Note: title.trim() || "NEW SCENE"}
  });
  recalc();
  render();
  saveSoon();
}
async function saveNow() {
  recalc();
  var payload = {blocks: blocks.map(function(b) { return {id: b.id, kind: b.kind, item: b.item}; })};
  var res = await fetch("/api/sequence", {method: "PUT", headers: {"Content-Type": "application/json"}, body: JSON.stringify(payload)});
  if (!res.ok) {
    setStatus("Error");
    console.error(await res.text());
    return false;
  }
  setStatus("Saved");
  return true;
}
function saveSoon() {
  setStatus("Saving");
  clearTimeout(saveTimer);
  saveTimer = setTimeout(saveNow, 250);
}
function setStatus(text) { qs("#status").textContent = text; }
function openLightbox() {
  if (!selectedBlock()) return;
  lightboxOpen = true;
  qs("#lightbox").classList.add("open");
  renderLightbox();
}
function closeLightbox() {
  lightboxOpen = false;
  qs("#lightbox").classList.remove("open");
}
function toggleKeyHelp(force) {
  var help = qs("#keyHelp");
  var open = force === undefined ? help.hidden : Boolean(force);
  help.hidden = !open;
}
function renderLightbox() {
  if (!lightboxOpen) return;
  var b = selectedBlock();
  if (!b) return closeLightbox();
  qs("#lightboxImg").src = b.media_url;
  qs("#lightboxMeta").textContent = String(b.index).padStart(3, "0") + "  " + b.item.File;
}
function escapeHTML(s) {
  return String(s || "").replace(/[&<>"']/g, function(ch) {
    return {"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#039;"}[ch];
  });
}
initButtonIcons();
qs("#search").addEventListener("input", render);
qs("#search").addEventListener("keydown", function(e) {
  if (e.key === "Escape") {
    e.currentTarget.blur();
    e.preventDefault();
  }
});
qs("#addImages").addEventListener("click", function() { qs("#filePick").click(); });
qs("#audioSelect").addEventListener("change", loadAudio);
qs("#audioStart").addEventListener("click", function() { seekAudio(-Infinity); });
qs("#audioBack").addEventListener("click", function() { seekAudio(-5); });
qs("#audioPlay").addEventListener("click", toggleAudio);
qs("#audioForward").addEventListener("click", function() { seekAudio(5); });
qs("#audioEnd").addEventListener("click", function() {
  var audio = qs("#audioPlayer");
  if (isFinite(audio.duration)) audio.currentTime = audio.duration;
});
qs("#audioPlayer").addEventListener("timeupdate", updateAudioClock);
qs("#audioPlayer").addEventListener("loadedmetadata", updateAudioClock);
qs("#audioPlayer").addEventListener("play", updateAudioClock);
qs("#audioPlayer").addEventListener("pause", updateAudioClock);
qs("#filePick").addEventListener("change", function(e) {
  uploadFiles(e.target.files);
  e.target.value = "";
});
qs("#unsortedPane").addEventListener("dragover", function(e) {
  if (draggingId) {
    e.preventDefault();
    qs("#unsortedPane").classList.add("drop-target");
    if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
    return;
  }
  if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length) {
    e.preventDefault();
    qs("#unsortedPane").classList.add("drop-target");
  }
});
qs("#unsortedPane").addEventListener("dragleave", function(e) {
  if (!qs("#unsortedPane").contains(e.relatedTarget)) {
    qs("#unsortedPane").classList.remove("drop-target");
  }
});
qs("#unsortedPane").addEventListener("drop", function(e) {
  qs("#unsortedPane").classList.remove("drop-target");
  if (draggingId) {
    e.preventDefault();
    sendBlockToUnsorted(draggingId);
    draggingId = "";
    draggingFile = "";
    clearDrops();
    return;
  }
  if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length) {
    e.preventDefault();
    uploadFiles(e.dataTransfer.files);
  }
});
qs("#start").addEventListener("click", goStart);
qs("#prev").addEventListener("click", function() { stepSelection(-1); });
qs("#play").addEventListener("click", togglePlay);
qs("#next").addEventListener("click", function() { stepSelection(1); });
qs("#end").addEventListener("click", goEnd);
qs("#scene").addEventListener("click", addScene);
qs("#previewFrame").addEventListener("click", togglePlay);
qs("#previewScrub").addEventListener("input", function(e) {
  pause();
  playTime = Number(e.target.value) || 0;
  var b = blockAt(playTime);
  if (b) selectedId = b.id;
  render();
});
qs("#export").addEventListener("click", async function() {
  if (await saveNow()) setStatus("EDL saved");
});
qs("#undo").addEventListener("click", undoLastEdit);
qs("#toUnsorted").addEventListener("click", sendSelectedToUnsorted);
qs("#closeKeyHelp").addEventListener("click", function() { toggleKeyHelp(false); });
qs("#keyHelp").addEventListener("click", function(e) {
  if (e.target === qs("#keyHelp")) toggleKeyHelp(false);
});
initPreviewWidth();
bindPreviewSplitter();
bindUnsortedSplitter();
document.addEventListener("fullscreenchange", renderPreview);
window.addEventListener("keydown", function(e) {
  if ((e.ctrlKey || e.metaKey) && !e.shiftKey && String(e.key).toLowerCase() === "z") {
    e.preventDefault();
    undoLastEdit();
    return;
  }
  var tag = document.activeElement && document.activeElement.tagName;
  if (tag === "INPUT" || tag === "SELECT") return;
  if (!qs("#keyHelp").hidden) {
    if (e.key === "Escape" || e.key === "?" || e.key === "Enter") toggleKeyHelp(false);
    return;
  }
  if (lightboxOpen && (e.key === "Escape" || e.key === "Enter")) { closeLightbox(); return; }
  if (e.key === "/") {
    e.preventDefault();
    qs("#search").focus();
    qs("#search").select();
    return;
  }
  if (e.key === "Tab") {
    e.preventDefault();
    toggleBoardFocus();
    return;
  }
  if (boardFocus === "unsorted") {
    if (e.key === "h" || e.key === "ArrowLeft") moveUnsorted(-1);
    else if (e.key === "l" || e.key === "ArrowRight") moveUnsorted(1);
    else if (e.key === "j" || e.key === "ArrowDown") moveUnsorted(unsortedColumns());
    else if (e.key === "k" || e.key === "ArrowUp") moveUnsorted(-unsortedColumns());
    else if (e.key === "Enter") addImage(selectedFile, null, false);
    else if (e.key === "G" || e.key === "End") goUnsortedEnd();
    else if (e.key === "g") {
      if (pendingG) {
        clearTimeout(pendingGTimer);
        pendingG = false;
        goUnsortedStart();
      } else {
        pendingG = true;
        pendingGTimer = setTimeout(function() { pendingG = false; }, 650);
      }
    }
    return;
  }
  if (e.key === " ") { e.preventDefault(); togglePlay(); }
  else if (e.key === "?") toggleKeyHelp(true);
  else if (e.key === "Home") goStart();
  else if (e.key === "End" || e.key === "G") goEnd();
  else if (e.key === "ArrowDown" || e.key === "ArrowRight" || e.key === "j" || e.key === "l") stepSelection(1);
  else if (e.key === "ArrowUp" || e.key === "ArrowLeft" || e.key === "k" || e.key === "h") stepSelection(-1);
  else if (e.key === "J") moveSelected(1);
  else if (e.key === "K") moveSelected(-1);
  else if (e.key === "u" || e.key === "U") nudgeDuration(-0.1);
  else if (e.key === "i" || e.key === "I") nudgeDuration(0.1);
  else if (e.key === "f" || e.key === "F") fullscreenPreview();
  else if (e.key === "Enter") openLightbox();
  else if (e.key === "s" || e.key === "S") addScene();
  else if (e.key === "x" || e.key === "Delete" || e.key === "Backspace") sendSelectedToUnsorted();
  else if (e.key === "g") {
    if (pendingG) {
      clearTimeout(pendingGTimer);
      pendingG = false;
      goStart();
    } else {
      pendingG = true;
      pendingGTimer = setTimeout(function() { pendingG = false; }, 650);
    }
  }
});
load().catch(function(err) {
  setStatus("Error");
  console.error(err);
});
</script>
</body>
</html>
`
