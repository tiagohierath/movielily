package tui

import (
	"fmt"
	"io"
	"movielily/internal/manim"
	"movielily/internal/model"
	"movielily/internal/typst"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	xterm "golang.org/x/term"
)

// ---- title cards (T) -------------------------------------------------------

// startTitleCard begins the two-step card wizard: template, then text. The
// last-used template is prefilled, so reusing a style is Enter + type text.
func (e *editor) startTitleCard() {
	if created, err := typst.EnsureDefault(e.p); err != nil {
		e.status = "titles: " + err.Error()
		return
	} else if created != "" {
		e.status = "created titles/" + created + " (edit it for your style)"
	}
	tpl := e.lastTemplate
	if tpl == "" {
		if ts, _ := typst.Templates(e.p); len(ts) > 0 {
			tpl = ts[0]
		}
	}
	e.mode = modeEdit
	e.editWhat = editTitleTemplate
	e.inputBytes = []byte(tpl)
}

func (e *editor) commitTitleTemplate() {
	tpl := strings.TrimSpace(string(e.inputBytes))
	if _, err := typst.Resolve(e.p, tpl); err != nil {
		ts, _ := typst.Templates(e.p)
		e.status = fmt.Sprintf("no template %q (have: %s)", tpl, strings.Join(ts, " "))
		e.inputBytes = nil // stay in the prompt for another try
		return
	}
	e.lastTemplate = typst.StoreName(tpl)
	e.editWhat = editTitleText
	e.inputBytes = nil
}

func (e *editor) commitTitleText() {
	text := strings.TrimSpace(string(e.inputBytes))
	e.mode = modeNormal
	e.editWhat = editNote
	e.inputBytes = nil
	if text == "" {
		e.status = "title card cancelled (no text)"
		return
	}
	e.pushUndo()
	at := e.cursor + 1
	if len(e.items) == 0 {
		at = 0
	}
	if at > len(e.items) {
		at = len(e.items)
	}
	it := model.SequenceItem{Kind: model.KindTitle, File: e.lastTemplate, Dur: 4, Note: text}
	e.items = append(e.items, model.SequenceItem{})
	copy(e.items[at+1:], e.items[at:])
	e.items[at] = it
	e.cursor = at
	e.marked = map[int]bool{}
	e.dirty = true
	e.forceScene = true
	e.status = fmt.Sprintf("card %q added (4s · t to change · T reuses %s) · w to save", text, e.lastTemplate)
}

// ---- animated cards (A) ----------------------------------------------------

// startAnimCard mirrors the title-card wizard for manim templates. The render
// itself happens suspended (it takes real time and prints progress).
func (e *editor) startAnimCard() {
	if created, err := manim.EnsureDefault(e.p); err != nil {
		e.status = "anims: " + err.Error()
		return
	} else if created != "" {
		e.status = "created anims/" + created + " (edit it for your style)"
	}
	tpl := e.lastAnimTemplate
	if tpl == "" {
		if ts, _ := manim.Templates(e.p); len(ts) > 0 {
			tpl = ts[0]
		}
	}
	e.mode = modeEdit
	e.editWhat = editAnimTemplate
	e.inputBytes = []byte(tpl)
}

func (e *editor) commitAnimTemplate() {
	tpl := strings.TrimSpace(string(e.inputBytes))
	if _, err := manim.Resolve(e.p, tpl); err != nil {
		ts, _ := manim.Templates(e.p)
		e.status = fmt.Sprintf("no anim template %q (have: %s)", tpl, strings.Join(ts, " "))
		e.inputBytes = nil // stay in the prompt for another try
		return
	}
	e.lastAnimTemplate = manim.StoreName(tpl)
	e.editWhat = editAnimText
	e.inputBytes = nil
}

func (e *editor) commitAnimText() {
	text := strings.TrimSpace(string(e.inputBytes))
	e.mode = modeNormal
	e.editWhat = editNote
	e.inputBytes = nil
	if text == "" {
		e.status = "animated card cancelled (no text)"
		return
	}
	e.pendingAnimTpl, e.pendingAnimText = e.lastAnimTemplate, text
	e.wantAnim = true // the Edit loop renders it suspended, then inserts
}

// animRenderOp runs the manim render with the terminal handed over (progress
// is visible, like vim or mpv), then inserts the finished card at the cursor.
func (e *editor) animRenderOp(st *xterm.State) {
	tpl, text := e.pendingAnimTpl, e.pendingAnimText
	e.pendingAnimTpl, e.pendingAnimText = "", ""

	e.suspend(st)
	fmt.Printf("rendering animated card %q with %s (cached afterwards)…\n", text, tpl)
	clip, err := manim.Render(e.p, tpl, text)
	var dur float64
	if err == nil {
		dur, err = manim.Probe(clip)
	}
	e.resume(st)

	if err != nil {
		e.status = "anim: " + err.Error()
	} else {
		e.pushUndo()
		at := e.cursor + 1
		if len(e.items) == 0 {
			at = 0
		}
		if at > len(e.items) {
			at = len(e.items)
		}
		it := model.SequenceItem{Kind: model.KindAnim, File: tpl, Dur: roundCenti(dur), Note: text}
		e.items = append(e.items, model.SequenceItem{})
		copy(e.items[at+1:], e.items[at:])
		e.items[at] = it
		e.cursor = at
		e.marked = map[int]bool{}
		e.dirty = true
		e.forceScene = true
		e.status = fmt.Sprintf("animated card %q added (%ss) · w to save", text, trimf(it.Dur))
	}
	e.redraw(true)
	e.onSceneChange()
	e.out.Flush()
}

func roundCenti(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// pendingFile carries the first wizard answer between the two prompts of the
// bed and overlay wizards.
func (e *editor) startBed() {
	e.mode = modeEdit
	e.editWhat = editBedFile
	e.inputBytes = nil
	e.status = "music/narration file from audio/ or another source media folder"
}

func (e *editor) commitBedFile() {
	name := strings.TrimSpace(string(e.inputBytes))
	if _, err := e.p.ResolveFootage(name); err != nil {
		e.status = err.Error()
		e.inputBytes = nil // stay in the prompt
		return
	}
	e.pendingFile = e.p.StoreName(name)
	e.editWhat = editBedGain
	e.inputBytes = []byte("-12")
}

func (e *editor) commitBedGain() {
	db, err := model.ParseSeconds(string(e.inputBytes))
	e.mode = modeNormal
	e.editWhat = editNote
	e.inputBytes = nil
	if err != nil {
		e.status = "bed cancelled (gain must be dB, e.g. -12)"
		return
	}
	e.pushUndo()
	e.items = append(e.items, model.SequenceItem{Kind: model.KindAudio, File: e.pendingFile, Gain: db})
	e.cursor = len(e.items) - 1
	e.dirty = true
	e.forceScene = true
	e.status = fmt.Sprintf("bed %s at %sdB · place: #at_image_ #at_scene_ #at_ #from_ #for_ #duck (e edits) · w to save", e.pendingFile, trimf(db))
}

func (e *editor) startOverlay() {
	// An overlay rides the scene above it, so it needs one to ride.
	ok := false
	for i := 0; i <= e.cursor && i < len(e.items); i++ {
		it := e.items[i]
		if !it.IsSection() && !it.IsAudio() && !it.IsOverlay() {
			ok = true
		}
	}
	if !ok {
		e.status = "overlays ride a scene: move the cursor onto/below one first"
		return
	}
	e.mode = modeEdit
	e.editWhat = editOvlFile
	e.inputBytes = nil
	e.status = "overlay image — type/drop a local path or drop an https image URL (png keeps transparency)"
}

// pasteClipboardOverlay imports an image copied in a browser with one key.
// On Wayland wl-paste exposes either the copied image/png bytes or plain text
// (a file path/direct image URL), both of which use the same portable import
// path as the ordinary overlay prompt.
func (e *editor) pasteClipboardOverlay() {
	e.startOverlay()
	if e.mode != modeEdit || e.editWhat != editOvlFile {
		return
	}
	types, err := exec.Command("wl-paste", "--list-types").Output()
	if err != nil {
		e.mode = modeNormal
		e.status = "clipboard image needs wl-paste (or use :overlay)"
		return
	}
	available := strings.Fields(string(types))
	for _, typ := range available {
		if strings.HasPrefix(typ, "text/plain") || typ == "text/uri-list" {
			out, err := exec.Command("wl-paste", "--no-newline", "--type", typ).Output()
			if err == nil && strings.TrimSpace(string(out)) != "" {
				e.inputBytes = out
				e.commitOvlFile()
				if e.editWhat != editOvlSpec {
					return
				}
				e.finishClipboardOverlay()
				return
			}
		}
	}
	for _, typ := range available {
		if typ != "image/png" {
			continue
		}
		out, err := exec.Command("wl-paste", "--type", typ).Output()
		if err != nil || len(out) == 0 {
			break
		}
		stored, err := e.storeClipboardPNG(out)
		if err != nil {
			e.mode = modeNormal
			e.status = "clipboard image: " + err.Error()
			return
		}
		e.pendingFile = stored
		e.finishClipboardOverlay()
		return
	}
	e.mode = modeNormal
	e.status = "clipboard has no PNG image or usable image path/URL"
}

// finishClipboardOverlay is the deliberately boring default: the copied
// image fills the selected narration scene until it ends. Custom timing stays
// available through :overlay, but the common "show this image" action is one
// key and no follow-up question.
func (e *editor) finishClipboardOverlay() {
	e.editWhat = editOvlSpec
	e.inputBytes = []byte("0 0 " + model.DefaultPlace)
	e.commitOvlSpec()
	e.status = "clipboard image added full-screen · w saves · :overlay is for custom timing"
}

func (e *editor) storeClipboardPNG(data []byte) (string, error) {
	if len(data) > 40*1024*1024 {
		return "", fmt.Errorf("image is larger than 40 MB")
	}
	dir := filepath.Join(e.p.ImagesDir(), "stills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := availableImageName(dir, "clipboard.png")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", err
	}
	return e.p.StoreName(dst), nil
}

func (e *editor) commitOvlFile() {
	name := normaliseDroppedImage(strings.TrimSpace(string(e.inputBytes)))
	// Image from source media, or a typst template (its text = the note: e edits).
	if strings.HasSuffix(name, ".typ") {
		if _, err := typst.Resolve(e.p, name); err != nil {
			e.status = err.Error()
			e.inputBytes = nil
			return
		}
		e.pendingFile = typst.StoreName(name)
	} else {
		if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
			stored, err := e.downloadOverlayImage(name)
			if err != nil {
				e.status = err.Error()
				e.inputBytes = nil
				return
			}
			e.pendingFile = stored
		} else {
			abs, err := e.p.ResolveFootage(name)
			if err != nil {
				e.status = err.Error()
				e.inputBytes = nil // stay in the prompt
				return
			}
			stored, err := e.copyDroppedOverlay(abs)
			if err != nil {
				e.status = err.Error()
				e.inputBytes = nil
				return
			}
			e.pendingFile = stored
		}
	}
	e.editWhat = editOvlSpec
	e.inputBytes = []byte("0 0 " + model.DefaultPlace)
}

// normaliseDroppedImage accepts the forms terminals commonly paste when a
// file is dropped: a plain path, shell-quoted path, or file:// URL.
func normaliseDroppedImage(name string) string {
	name = strings.Trim(strings.TrimSpace(name), "\"'")
	if u, err := url.Parse(name); err == nil && u.Scheme == "file" {
		return u.Path
	}
	return strings.ReplaceAll(name, "\\ ", " ")
}

// copyDroppedOverlay brings an image dropped from outside the project into
// images/stills. Keeping it inside the project makes the resulting sequence
// portable and avoids a later render depending on Downloads or the desktop.
func (e *editor) copyDroppedOverlay(abs string) (string, error) {
	if !isOverlayImage(abs) {
		return "", fmt.Errorf("%s is not a supported image (want png, jpg, jpeg, or webp)", filepath.Base(abs))
	}
	if rel, err := filepath.Rel(e.p.Root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return e.p.StoreName(abs), nil
	}
	dir := filepath.Join(e.p.ImagesDir(), "stills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := availableImageName(dir, filepath.Base(abs))
	in, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return "", closeErr
	}
	return e.p.StoreName(dst), nil
}

func (e *editor) downloadOverlayImage(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("overlay URL must be a complete http(s) image URL")
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download image: website returned %s", resp.Status)
	}
	name := filepath.Base(u.Path)
	if !isOverlayImage(name) {
		return "", fmt.Errorf("download image: URL must end in .png, .jpg, .jpeg, or .webp")
	}
	dir := filepath.Join(e.p.ImagesDir(), "stills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := availableImageName(dir, name)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	// A cap prevents an accidental page/video URL from filling the project.
	n, copyErr := io.Copy(out, io.LimitReader(resp.Body, 40*1024*1024+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return "", copyErr
	}
	if n > 40*1024*1024 {
		_ = os.Remove(dst)
		return "", fmt.Errorf("download image: file is larger than 40 MB")
	}
	if closeErr != nil {
		return "", closeErr
	}
	return e.p.StoreName(dst), nil
}

func isOverlayImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	default:
		return false
	}
}

func availableImageName(dir, name string) string {
	name = filepath.Base(name)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 1; ; n++ {
		candidate := filepath.Join(dir, name)
		if n > 1 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, n, ext))
		}
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// commitOvlSpec parses "at dur place" (dur 0 = until the scene ends) and
// inserts the overlay below the cursor so it rides the scene above.
func (e *editor) commitOvlSpec() {
	fields := strings.Fields(string(e.inputBytes))
	e.mode = modeNormal
	e.editWhat = editNote
	e.inputBytes = nil
	if len(fields) < 2 {
		e.status = "overlay cancelled (want: at dur [place], e.g. 2 5 tr:30)"
		return
	}
	at, err1 := model.ParseSeconds(fields[0])
	dur, err2 := model.ParseSeconds(fields[1])
	place := model.DefaultPlace
	if len(fields) > 2 {
		place = fields[2]
	}
	if _, _, err := model.ParsePlace(place); err1 != nil || err2 != nil || err != nil {
		e.status = "overlay cancelled (want: at dur [place], e.g. 2 5 tr:30)"
		return
	}
	e.pushUndo()
	at2 := e.cursor + 1
	if at2 > len(e.items) {
		at2 = len(e.items)
	}
	it := model.SequenceItem{Kind: model.KindOverlay, File: e.pendingFile, In: at, Dur: dur, Place: place}
	e.items = append(e.items, model.SequenceItem{})
	copy(e.items[at2+1:], e.items[at2:])
	e.items[at2] = it
	e.cursor = at2
	e.marked = map[int]bool{}
	e.dirty = true
	e.forceScene = true
	e.status = fmt.Sprintf("overlay %s riding the scene above (+%ss for %ss @ %s) · w to save", it.File, trimf(at), trimf(dur), place)
}

// youtubeOp queues the last render for YouTube by re-invoking movielily's own
// `youtube` subcommand (the TUI can't import the cli package). It runs with
// the terminal handed over, so the uploader's OAuth prompt and progress show.
func (e *editor) youtubeOp(st *xterm.State) {
	exe, err := os.Executable()
	if err != nil {
		exe = "movielily"
	}
	e.suspend(st)
	fmt.Println("queueing the last render for YouTube…")
	cmd := exec.Command(exe, "youtube")
	cmd.Dir = e.p.Root
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	e.resume(st)
	if runErr != nil {
		e.status = "youtube: " + runErr.Error()
	} else {
		e.status = "queued the last render for YouTube"
	}
	e.redraw(true)
	e.onSceneChange()
	e.out.Flush()
}

// silencesOp delegates to the CLI so its conservative defaults and idempotent
// select handling stay identical whether the command is used here or in a
// shell. It deliberately never changes the source audio or current sequence.
func (e *editor) silencesOp(st *xterm.State) {
	if e.cursor < 0 || e.cursor >= len(e.items) || !model.IsAudioFile(e.items[e.cursor].File) {
		e.status = "cut silences: select a narration audio scene first"
		return
	}
	file := e.items[e.cursor].File
	if _, err := e.p.ResolveFootage(file); err != nil {
		e.status = "cut silences: " + err.Error()
		return
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "movielily"
	}
	e.suspend(st)
	fmt.Println("finding only clearly long, quiet pauses — source audio will not be changed…")
	cmd := exec.Command(exe, "silences", file, "--keep", "--noise", "-45", "--gap", "1.2", "--pad", "0.4")
	cmd.Dir = e.p.Root
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	e.resume(st)
	if runErr != nil {
		e.status = "cut silences: " + runErr.Error()
	} else {
		e.status = "safe silence selects ready — source unchanged; use seq from-selects to build a cut"
	}
	e.redraw(true)
	e.onSceneChange()
	e.out.Flush()
}
