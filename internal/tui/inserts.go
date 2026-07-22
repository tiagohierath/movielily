package tui

import (
	"fmt"
	"movielily/internal/manim"
	"movielily/internal/model"
	"movielily/internal/typst"
	"os"
	"os/exec"
	"strings"

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
	e.status = "music/narration file from footage/ (e.g. musica.mp3)"
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
	e.status = fmt.Sprintf("bed %s at %sdB · tags place it: #at_ #from_ #for_ #duck (e edits) · w to save", e.pendingFile, trimf(db))
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
	e.status = "overlay image from footage/ (png keeps transparency)"
}

func (e *editor) commitOvlFile() {
	name := strings.TrimSpace(string(e.inputBytes))
	// Image from footage/, or a typst template (its text = the note: e edits).
	if strings.HasSuffix(name, ".typ") {
		if _, err := typst.Resolve(e.p, name); err != nil {
			e.status = err.Error()
			e.inputBytes = nil
			return
		}
		e.pendingFile = typst.StoreName(name)
	} else {
		if _, err := e.p.ResolveFootage(name); err != nil {
			e.status = err.Error()
			e.inputBytes = nil // stay in the prompt
			return
		}
		e.pendingFile = e.p.StoreName(name)
	}
	e.editWhat = editOvlSpec
	e.inputBytes = []byte("0 0 " + model.DefaultPlace)
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

// youtubeOp posts the last render to YouTube by re-invoking movielily's own
// `youtube` subcommand (the TUI can't import the cli package). It runs with
// the terminal handed over, so the uploader's OAuth prompt and progress show.
func (e *editor) youtubeOp(st *xterm.State) {
	exe, err := os.Executable()
	if err != nil {
		exe = "movielily"
	}
	e.suspend(st)
	fmt.Println("posting the last render to YouTube…")
	cmd := exec.Command(exe, "youtube")
	cmd.Dir = e.p.Root
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	e.resume(st)
	if runErr != nil {
		e.status = "youtube: " + runErr.Error()
	} else {
		e.status = "posted the last render to YouTube (private)"
	}
	e.redraw(true)
	e.onSceneChange()
	e.out.Flush()
}
