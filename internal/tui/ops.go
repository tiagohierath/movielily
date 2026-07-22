package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"movielily/internal/grade"
	"movielily/internal/model"
	"movielily/internal/mpv"
)

// ---- operations -----------------------------------------------------------

func (e *editor) down() {
	if e.cursor < len(e.items)-1 {
		e.cursor++
	}
}

func (e *editor) up() {
	if e.cursor > 0 {
		e.cursor--
	}
}

func (e *editor) clampCursor() {
	if e.cursor < 0 {
		e.cursor = 0
	}
	if e.cursor >= len(e.items) {
		e.cursor = len(e.items) - 1
	}
	if e.cursor < 0 {
		e.cursor = 0
	}
}

func (e *editor) toggleMark() {
	if len(e.items) == 0 {
		return
	}
	e.marked[e.cursor] = !e.marked[e.cursor]
	if e.cursor < len(e.items)-1 {
		e.cursor++
	}
}

func (e *editor) moveDown() {
	if e.cursor >= len(e.items)-1 {
		return
	}
	e.pushUndo()
	e.items[e.cursor], e.items[e.cursor+1] = e.items[e.cursor+1], e.items[e.cursor]
	e.marked[e.cursor], e.marked[e.cursor+1] = e.marked[e.cursor+1], e.marked[e.cursor]
	e.cursor++
	e.dirty = true
	e.forceScene = true
	e.status = "moved down"
}

func (e *editor) moveUp() {
	if e.cursor <= 0 {
		return
	}
	e.pushUndo()
	e.items[e.cursor], e.items[e.cursor-1] = e.items[e.cursor-1], e.items[e.cursor]
	e.marked[e.cursor], e.marked[e.cursor-1] = e.marked[e.cursor-1], e.marked[e.cursor]
	e.cursor--
	e.dirty = true
	e.forceScene = true
	e.status = "moved up"
}

func (e *editor) deleteSel() {
	if len(e.items) == 0 {
		return
	}
	e.pushUndo()
	if e.anyMarked() {
		kept := e.items[:0:0]
		var cut []model.SequenceItem
		for i, it := range e.items {
			if e.marked[i] {
				cut = append(cut, it)
			} else {
				kept = append(kept, it)
			}
		}
		e.items = kept
		e.marked = map[int]bool{}
		e.clipboard = cut
		e.status = fmt.Sprintf("cut %d scene(s) · p pastes them back", len(cut))
	} else {
		e.clipboard = []model.SequenceItem{e.items[e.cursor]}
		e.items = append(e.items[:e.cursor], e.items[e.cursor+1:]...)
		e.status = "cut scene · p pastes it back"
	}
	e.clampCursor()
	e.dirty = true
	e.forceScene = true
}

func (e *editor) undoOp() {
	if len(e.undo) == 0 {
		e.status = "nothing to undo"
		return
	}
	e.redoStack = append(e.redoStack, e.snapshotItems())
	last := len(e.undo) - 1
	e.items = e.undo[last]
	e.undo = e.undo[:last]
	e.marked = map[int]bool{}
	e.clampCursor()
	e.dirty = true
	e.forceScene = true
	e.status = "undo (^R redoes)"
}

func (e *editor) redoOp() {
	if len(e.redoStack) == 0 {
		e.status = "nothing to redo"
		return
	}
	// Straight onto the undo stack, not through pushUndo: pushUndo would
	// clear the redo history we are in the middle of walking.
	e.undo = append(e.undo, e.snapshotItems())
	last := len(e.redoStack) - 1
	e.items = e.redoStack[last]
	e.redoStack = e.redoStack[:last]
	e.marked = map[int]bool{}
	e.clampCursor()
	e.dirty = true
	e.forceScene = true
	e.status = "redo"
}

func (e *editor) snapshotItems() []model.SequenceItem {
	cp := make([]model.SequenceItem, len(e.items))
	copy(cp, e.items)
	return cp
}

func (e *editor) saveOp() {
	if err := e.save(); err != nil {
		e.status = "save failed: " + err.Error()
		return
	}
	e.status = "saved " + filepath.Base(e.path)
}

func (e *editor) startEdit() {
	if len(e.items) == 0 {
		return
	}
	e.mode = modeEdit
	e.editWhat = editNote
	e.inputBytes = []byte(e.items[e.cursor].Note)
}

// startDurEdit edits the number that shapes the current item: a still's or
// title card's on-screen duration, an overlay's duration, or a bed's gain.
// Clips set their length via their in/out (⏎ → mpv), and sections have none.
func (e *editor) startDurEdit() {
	if len(e.items) == 0 {
		return
	}
	switch e.items[e.cursor].Kind {
	case model.KindImage, model.KindTitle, model.KindOverlay:
		e.mode = modeEdit
		e.editWhat = editDur
		e.inputBytes = []byte(trimf(e.items[e.cursor].Dur))
	case model.KindAudio:
		e.mode = modeEdit
		e.editWhat = editGain
		e.inputBytes = []byte(trimf(e.items[e.cursor].Gain))
	case model.KindSection:
		e.status = "sections have no duration"
	default:
		e.status = "use ⏎ to set a clip's in/out (or +/- to nudge the out point)"
	}
}

// nudge is the no-typing fine adjust: out point ±0.5s on clips, duration
// ±0.5s on stills/cards/overlays, gain ±1dB on beds.
func (e *editor) nudge(dir float64) {
	if len(e.items) == 0 {
		return
	}
	it := &e.items[e.cursor]
	switch it.Kind {
	case model.KindSection:
		e.status = "sections have nothing to nudge"
		return
	case model.KindImage, model.KindTitle, model.KindOverlay:
		e.pushUndo()
		it.Dur += dir * 0.5
		if it.Dur < 0.5 {
			it.Dur = 0.5
		}
		e.status = fmt.Sprintf("duration %ss · w to save", trimf(it.Dur))
	case model.KindAudio:
		e.pushUndo()
		it.Gain += dir
		e.status = fmt.Sprintf("gain %sdB · w to save", trimf(it.Gain))
	default:
		e.pushUndo()
		out := it.Out + dir*0.5
		if out < it.In+0.1 {
			out = it.In + 0.1
		}
		it.Out = out
		e.status = fmt.Sprintf("out %s (%ss) · w to save", mmss(it.Out), trimf(it.Duration()))
	}
	e.dirty = true
	e.forceScene = true
}

// nudgeIn is the in-point counterpart of nudge: < and > move a clip's start
// by half a second, clamped to the clip.
func (e *editor) nudgeIn(dir float64) {
	if len(e.items) == 0 {
		return
	}
	it := &e.items[e.cursor]
	if it.Kind != model.KindVideo {
		e.status = "only clips have an in point (< and > move it)"
		return
	}
	e.pushUndo()
	in := it.In + dir*0.5
	if in < 0 {
		in = 0
	}
	if in > it.Out-0.1 {
		in = it.Out - 0.1
	}
	it.In = in
	e.dirty = true
	e.forceScene = true
	e.status = fmt.Sprintf("in %s (%ss) · w to save", mmss(it.In), trimf(it.Duration()))
}

// startSplit cuts the clip under the cursor in two at a point picked in mpv
// (detached, like everything else): seek, Enter, done.
func (e *editor) startSplit() {
	if len(e.items) == 0 {
		return
	}
	it := e.items[e.cursor]
	if it.Kind != model.KindVideo {
		e.status = "only clips can be split (s)"
		return
	}
	if e.reselectBusy {
		e.status = "an mpv pick is already open (confirm or close it first)"
		return
	}
	e.reselectBusy = true
	idx, file := e.cursor, it.File
	mid := it.In + it.Duration()/2
	go func() {
		t, ok, err := mpv.PickTime(e.p, file, mid)
		e.reselectCh <- reselectRes{idx: idx, file: file, at: t, ok: ok, err: err, split: true}
	}()
	e.status = "mpv opened: seek to the cut point, Enter splits · the editor stays live"
}

// applySplit lands a picked split point: the scene becomes two adjacent
// scenes cut at that source time.
func (e *editor) applySplit(r reselectRes) {
	idx := r.idx
	if idx >= len(e.items) || e.items[idx].File != r.file || e.items[idx].Kind != model.KindVideo {
		e.status = "split confirmed but the scene moved (nothing applied)"
		return
	}
	it := e.items[idx]
	if r.at <= it.In+0.05 || r.at >= it.Out-0.05 {
		e.status = fmt.Sprintf("split point %s is outside this scene's %s–%s", mmss(r.at), mmss(it.In), mmss(it.Out))
		return
	}
	e.pushUndo()
	left, right := it, it
	left.Out = r.at
	right.In = r.at
	e.items[idx] = left
	e.items = append(e.items, model.SequenceItem{})
	copy(e.items[idx+2:], e.items[idx+1:])
	e.items[idx+1] = right
	e.cursor = idx + 1
	e.marked = map[int]bool{}
	e.dirty = true
	e.forceScene = true
	e.status = fmt.Sprintf("split at %s into %ss + %ss · w to save", mmss(r.at), trimf(left.Duration()), trimf(right.Duration()))
}

// jumpSection moves the cursor to the previous/next section header (or the
// ends of the list when there is none in that direction).
func (e *editor) jumpSection(dir int) {
	if len(e.items) == 0 {
		return
	}
	for i := e.cursor + dir; i >= 0 && i < len(e.items); i += dir {
		if e.items[i].IsSection() {
			e.cursor = i
			return
		}
	}
	if dir < 0 {
		e.cursor = 0
	} else {
		e.cursor = len(e.items) - 1
	}
}

func (e *editor) startSearch() {
	e.mode = modeSearch
	e.inputBytes = nil
}

// findNext jumps to the next item (wrapping) whose file or note matches the
// last search, in either direction.
func (e *editor) findNext(dir int) {
	q := strings.ToLower(e.lastSearch)
	if q == "" {
		e.status = "no search yet (press / )"
		return
	}
	n := len(e.items)
	for step := 1; step <= n; step++ {
		i := ((e.cursor+dir*step)%n + n) % n
		it := e.items[i]
		if strings.Contains(strings.ToLower(it.File), q) ||
			strings.Contains(strings.ToLower(it.Note), q) {
			e.cursor = i
			e.forceScene = true
			e.status = fmt.Sprintf("match: %q · n/N for next/prev", e.lastSearch)
			return
		}
	}
	e.status = fmt.Sprintf("no match for %q", e.lastSearch)
}

// yank copies the marked scenes (or the current one) without deleting.
func (e *editor) yank() {
	if len(e.items) == 0 {
		return
	}
	var cp []model.SequenceItem
	if e.anyMarked() {
		for i, it := range e.items {
			if e.marked[i] {
				cp = append(cp, it)
			}
		}
	} else {
		cp = append(cp, e.items[e.cursor])
	}
	e.clipboard = cp
	e.status = fmt.Sprintf("yanked %d scene(s) · p to paste", len(cp))
}

// paste inserts the clipboard below the cursor (vim's p).
func (e *editor) paste() {
	if len(e.clipboard) == 0 {
		e.status = "nothing to paste (d cuts, y copies)"
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
	ins := make([]model.SequenceItem, len(e.clipboard))
	copy(ins, e.clipboard)
	e.items = append(e.items[:at], append(ins, e.items[at:]...)...)
	e.cursor = at
	e.marked = map[int]bool{} // index-based marks would misalign
	e.dirty = true
	e.forceScene = true
	e.status = fmt.Sprintf("pasted %d scene(s)", len(ins))
}

// addSection inserts a new "folder" header just below the cursor (or at the top
// of an empty sequence) and drops straight into editing its title.
func (e *editor) addSection() {
	e.pushUndo()
	at := e.cursor + 1
	if len(e.items) == 0 {
		at = 0
	}
	if at > len(e.items) {
		at = len(e.items)
	}
	e.items = append(e.items, model.SequenceItem{})
	copy(e.items[at+1:], e.items[at:])
	e.items[at] = model.SequenceItem{Kind: model.KindSection}
	e.cursor = at
	// Marks are index-based; inserting a row would misalign them, so clear.
	e.marked = map[int]bool{}
	e.dirty = true
	e.mode = modeEdit
	e.inputBytes = nil
	e.status = "new section — type a title, Enter to set"
	e.drawAll()
}

func (e *editor) commitEdit() {
	switch e.editWhat {
	case editDur:
		e.commitDur()
		return
	case editGain:
		e.commitGain()
		return
	case editTitleTemplate:
		e.commitTitleTemplate()
		return
	case editTitleText:
		e.commitTitleText()
		return
	case editAnimTemplate:
		e.commitAnimTemplate()
		return
	case editAnimText:
		e.commitAnimText()
		return
	case editBedFile:
		e.commitBedFile()
		return
	case editBedGain:
		e.commitBedGain()
		return
	case editOvlFile:
		e.commitOvlFile()
		return
	case editOvlSpec:
		e.commitOvlSpec()
		return
	}
	e.pushUndo()
	isSection := e.items[e.cursor].IsSection()
	e.items[e.cursor].Note = strings.TrimSpace(string(e.inputBytes))
	e.inputBytes = nil
	e.mode = modeNormal
	e.dirty = true
	if isSection {
		e.status = "section title set · w to save"
	} else {
		e.status = "note updated · w to save"
	}
}

// commitDur parses the inline input as a duration and applies it to the still.
// A bad value is rejected with a message rather than silently zeroing it.
func (e *editor) commitDur() {
	secs, err := model.ParseSeconds(string(e.inputBytes))
	e.mode = modeNormal
	e.editWhat = editNote
	e.inputBytes = nil
	if err != nil || secs <= 0 {
		e.status = "duration unchanged (want a positive number of seconds)"
		return
	}
	e.pushUndo()
	e.items[e.cursor].Dur = secs
	e.dirty = true
	e.forceScene = true
	e.status = fmt.Sprintf("duration %ss · w to save", trimf(secs))
}

// startGradeEdit opens the grade panel for the scene under the cursor: a live
// slider view over its colour-grade / film-grain parameters. The same values
// can be edited as text (inline note tokens, or 'grade' presets on the CLI);
// panel and text are two views of the one reversible key=value grade.
func (e *editor) startGradeEdit() {
	if len(e.items) == 0 {
		return
	}
	if !grade.GradableKind(e.items[e.cursor].Kind) {
		e.status = "only footage/cards take a grade (not beds or sections)"
		return
	}
	e.gradeIdx = 0
	e.screen = 2
	e.drawGrade()
	e.out.Flush()
}

// currentGrade parses the selected scene's grade from its note.
func (e *editor) currentGrade() *grade.Grade {
	_, g := grade.SplitNote(e.items[e.cursor].Note)
	return g
}

// setGradeParam writes one parameter back into the scene's note (clamped),
// keeping the human text. Neutral values drop out, so a grade is always the
// minimal reversible text.
func (e *editor) setGradeParam(name string, v float64) {
	g := e.currentGrade()
	specs := grade.Specs()
	for _, s := range specs {
		if s.Name == name {
			if v < s.Min {
				v = s.Min
			}
			if v > s.Max {
				v = s.Max
			}
		}
	}
	if err := g.Set(name, v); err != nil {
		e.status = err.Error()
		return
	}
	e.pushUndo()
	e.items[e.cursor].Note = grade.MergeIntoNote(e.items[e.cursor].Note, g)
	e.dirty = true
	e.forceScene = true
}

// handleGrade drives the grade panel: up/down pick a parameter, left/right (or
// h/l, -/+) adjust it, 0 resets it to neutral, r resets all, Tab/q/Esc closes.
func (e *editor) handleGrade(chunk []byte) {
	specs := grade.Specs()
	if len(chunk) >= 3 && chunk[0] == 0x1b && chunk[1] == '[' {
		switch chunk[2] {
		case 'A':
			e.gradeMove(-1)
		case 'B':
			e.gradeMove(1)
		case 'C':
			e.gradeAdjust(specs, 1)
		case 'D':
			e.gradeAdjust(specs, -1)
		}
		e.drawGrade()
		e.out.Flush()
		return
	}
	for _, b := range chunk {
		switch b {
		case 'j':
			e.gradeMove(1)
		case 'k':
			e.gradeMove(-1)
		case 'l', '+', '=', 'L':
			e.gradeAdjust(specs, 1)
		case 'h', '-', 'H':
			e.gradeAdjust(specs, -1)
		case '0':
			e.setGradeParam(specs[e.gradeIdx].Name, specs[e.gradeIdx].Neutral)
		case 'r':
			e.pushUndo()
			human, _ := grade.SplitNote(e.items[e.cursor].Note)
			e.items[e.cursor].Note = human
			e.dirty = true
			e.forceScene = true
			e.status = "grade cleared"
		case 0x09, 'q', 0x1b: // Tab/q/Esc: back to the editor
			e.screen = 0
			g := e.currentGrade()
			if g.IsNeutral() {
				e.status = "grade: neutral · w to save"
			} else {
				e.status = "grade " + g.String() + " · w to save"
			}
			e.redraw(true)
			e.onSceneChange()
			e.out.Flush()
			return
		}
	}
	e.drawGrade()
	e.out.Flush()
}

func (e *editor) gradeMove(d int) {
	e.gradeIdx += d
	n := len(grade.Specs())
	if e.gradeIdx < 0 {
		e.gradeIdx = 0
	}
	if e.gradeIdx >= n {
		e.gradeIdx = n - 1
	}
}

// gradeAdjust steps the selected parameter. The step is 5 for the wide
// 0..200 / -100..100 knobs, matching how the eye reads a slider.
func (e *editor) gradeAdjust(specs []grade.Spec, dir float64) {
	s := specs[e.gradeIdx]
	cur := e.currentGrade().Get(s.Name)
	e.setGradeParam(s.Name, cur+dir*5)
}

// commitGain parses the inline input as a bed gain in dB (negative is normal
// for music under a voice).
func (e *editor) commitGain() {
	db, err := model.ParseSeconds(string(e.inputBytes))
	e.mode = modeNormal
	e.editWhat = editNote
	e.inputBytes = nil
	if err != nil {
		e.status = "gain unchanged (want dB, e.g. -12)"
		return
	}
	e.pushUndo()
	e.items[e.cursor].Gain = db
	e.dirty = true
	e.forceScene = true
	e.status = fmt.Sprintf("gain %sdB · w to save", trimf(db))
}
