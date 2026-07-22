package tui

import (
	"fmt"
	"strings"
)

// ---- input ----------------------------------------------------------------

func (e *editor) handleInput(chunk []byte) (quit bool) {
	if e.screen == 1 {
		e.handleSnapshots(chunk)
		return false
	}
	if e.mode == modeEdit {
		e.handleEdit(chunk)
		return false
	}
	if e.mode == modeSearch {
		e.handleSearch(chunk)
		return false
	}
	if e.mode == modePalette {
		return e.handlePalette(chunk)
	}
	if e.helpOpen { // any key closes the help overlay
		e.helpOpen = false
		e.redraw(true)
		e.onSceneChange()
		e.out.Flush()
		return false
	}

	oldCursor := e.cursor
	e.status = ""

	// Arrow keys arrive as a single ESC [ A/B chunk in raw mode.
	if len(chunk) >= 3 && chunk[0] == 0x1b && chunk[1] == '[' {
		switch chunk[2] {
		case 'A':
			e.up()
		case 'B':
			e.down()
		}
	} else {
		for _, b := range chunk {
			switch b {
			case 'q':
				return true
			case 'Q':
				e.discard = true
				return true
			case 'j':
				e.down()
			case 'k':
				e.up()
			case 'g':
				e.cursor = 0
			case 'G':
				if len(e.items) > 0 {
					e.cursor = len(e.items) - 1
				}
			case 'J':
				e.moveDown()
			case 'K':
				e.moveUp()
			case '[':
				e.jumpSection(-1)
			case ']':
				e.jumpSection(1)
			case ' ':
				e.toggleMark()
			case 'e':
				e.startEdit()
			case 't':
				e.startDurEdit()
			case '+', '=':
				e.nudge(1)
			case '-':
				e.nudge(-1)
			case '>':
				e.nudgeIn(1)
			case '<':
				e.nudgeIn(-1)
			case 's':
				e.startSplit()
			case '\r', '\n':
				e.wantReselect = true
			case 'o':
				e.addSection()
			case 'T':
				e.startTitleCard()
			case 'A':
				e.startAnimCard()
			case 0x09: // Tab: the snapshots (git graph) tab
				e.openSnapshots()
			case 'v':
				e.wantVim = true
			case 'd':
				e.deleteSel()
			case 'y':
				e.yank()
			case 'p':
				e.paste()
			case 'u':
				e.undoOp()
			case 0x12: // ctrl-r
				e.redoOp()
			case '/':
				e.startSearch()
			case ':':
				e.mode = modePalette
				e.inputBytes = nil
				e.palSel = 0
			case 'n':
				e.findNext(1)
			case 'N':
				e.findNext(-1)
			case 'r':
				e.wantReview = 1
			case 'R':
				e.wantReview = 2
			case 'w':
				e.saveOp()
			case '?':
				e.helpOpen = true
			}
		}
	}

	if e.mode == modePalette { // ':' opened the palette this chunk
		e.drawAll()
		e.drawPalette()
		e.out.Flush()
		return false
	}
	if e.mode != modeNormal { // an inline edit or search prompt took over
		e.drawFooter()
		e.out.Flush()
		return false
	}
	if e.helpOpen {
		e.drawHelp()
		e.out.Flush()
		return false
	}
	if e.screen == 1 {
		e.drawSnapshots()
		e.out.Flush()
		return false
	}

	e.clampCursor()
	e.drawAll()
	if e.cursor != oldCursor || e.forceScene {
		e.forceScene = false
		e.onSceneChange()
	}
	e.out.Flush()
	return false
}

func (e *editor) handleEdit(chunk []byte) {
	for _, b := range chunk {
		switch {
		case b == 0x1b:
			e.mode = modeNormal
			e.editWhat = editNote
			e.status = "edit cancelled"
			e.drawAll()
			e.onSceneChange()
			e.out.Flush()
			return
		case b == '\r' || b == '\n':
			e.commitEdit()
			e.drawAll()
			e.onSceneChange()
			e.out.Flush()
			return
		case b == 0x7f || b == 0x08:
			r := []rune(string(e.inputBytes))
			if len(r) > 0 {
				e.inputBytes = []byte(string(r[:len(r)-1]))
			}
		case b < 0x20:
			// ignore other control bytes
		default:
			e.inputBytes = append(e.inputBytes, b)
		}
	}
	e.drawFooter()
	e.out.Flush()
}

// handleSearch reads the / prompt; Enter jumps to the first match, n/N repeat.
func (e *editor) handleSearch(chunk []byte) {
	for _, b := range chunk {
		switch {
		case b == 0x1b:
			e.mode = modeNormal
			e.inputBytes = nil
			e.status = "search cancelled"
			e.drawAll()
			e.out.Flush()
			return
		case b == '\r' || b == '\n':
			e.mode = modeNormal
			e.lastSearch = strings.TrimSpace(string(e.inputBytes))
			e.inputBytes = nil
			e.findNext(1)
			e.clampCursor()
			e.drawAll()
			e.onSceneChange()
			e.out.Flush()
			return
		case b == 0x7f || b == 0x08:
			r := []rune(string(e.inputBytes))
			if len(r) > 0 {
				e.inputBytes = []byte(string(r[:len(r)-1]))
			}
		case b < 0x20:
		default:
			e.inputBytes = append(e.inputBytes, b)
		}
	}
	e.drawFooter()
	e.out.Flush()
}

// ---- command palette (:) ---------------------------------------------------

// palCmd is one palette entry. Names are nouns, not abbreviations: they are
// typed rarely enough that saving three characters doesn't matter, and the
// fuzzy filter means a few letters find any of them.
type palCmd struct {
	name string
	desc string
	run  func(e *editor) (quit bool)
}

var palette = []palCmd{
	{"watch", "play from the cursor in mpv (no render)", func(e *editor) bool { e.wantReview = 1; return false }},
	{"watch-all", "play the whole cut in mpv (no render)", func(e *editor) bool { e.wantReview = 2; return false }},
	{"title-card", "insert a typst title card below the cursor", func(e *editor) bool { e.startTitleCard(); return false }},
	{"animated-card", "insert a manim animated card below the cursor", func(e *editor) bool { e.startAnimCard(); return false }},
	{"split", "cut the clip in two at a point picked in mpv", func(e *editor) bool { e.startSplit(); return false }},
	{"bed", "add a music/narration bed under the whole cut", func(e *editor) bool { e.startBed(); return false }},
	{"overlay", "put an image on top of the scene at the cursor", func(e *editor) bool { e.startOverlay(); return false }},
	{"section", "insert a section header below the cursor", func(e *editor) bool { e.addSection(); return false }},
	{"note", "edit the scene's note (or card text)", func(e *editor) bool { e.startEdit(); return false }},
	{"duration", "edit the scene's duration (gain on beds)", func(e *editor) bool { e.startDurEdit(); return false }},
	{"search", "find scenes by file name or note", func(e *editor) bool { e.startSearch(); return false }},
	{"delete", "cut the marked scenes (or the current one)", func(e *editor) bool { e.deleteSel(); return false }},
	{"yank", "copy the marked scenes (or the current one)", func(e *editor) bool { e.yank(); return false }},
	{"paste", "paste the cut/yanked scenes below the cursor", func(e *editor) bool { e.paste(); return false }},
	{"undo", "undo the last change", func(e *editor) bool { e.undoOp(); return false }},
	{"redo", "redo an undone change", func(e *editor) bool { e.redoOp(); return false }},
	{"vim", "edit the sequence file in vim", func(e *editor) bool { e.wantVim = true; return false }},
	{"snapshots", "the git version graph (Tab does this too)", func(e *editor) bool { e.openSnapshots(); return false }},
	{"youtube", "post the last render to YouTube (private)", func(e *editor) bool { e.wantYoutube = true; return false }},
	{"help", "the key reference", func(e *editor) bool { e.helpOpen = true; return false }},
	{"top", "jump to the first scene", func(e *editor) bool { e.cursor = 0; return false }},
	{"bottom", "jump to the last scene", func(e *editor) bool {
		if len(e.items) > 0 {
			e.cursor = len(e.items) - 1
		}
		return false
	}},
	{"save", "write the sequence file", func(e *editor) bool { e.saveOp(); return false }},
	{"quit", "leave, saving if dirty", func(e *editor) bool { return true }},
	{"quit-discard", "leave without saving", func(e *editor) bool { e.discard = true; return true }},
}

// fuzzyRank scores a candidate against the query: -1 no match, lower is
// better (prefix beats substring beats scattered subsequence).
func fuzzyRank(name, q string) int {
	if q == "" {
		return 2
	}
	if strings.HasPrefix(name, q) {
		return 0
	}
	if strings.Contains(name, q) {
		return 1
	}
	i := 0
	for _, r := range name {
		if i < len(q) && byte(r) == q[i] {
			i++
		}
	}
	if i == len(q) {
		return 2
	}
	return -1
}

func (e *editor) paletteMatches() []palCmd {
	q := strings.ToLower(strings.TrimSpace(string(e.inputBytes)))
	var out []palCmd
	// Each command matches exactly one rank, so walking ranks 0..2 lists every
	// candidate once, best matches first. An empty query is all-rank-2, i.e.
	// the whole palette in declaration order.
	for rank := 0; rank <= 2; rank++ {
		for _, c := range palette {
			if fuzzyRank(c.name, q) == rank {
				out = append(out, c)
			}
		}
	}
	return out
}

func (e *editor) handlePalette(chunk []byte) (quit bool) {
	for _, b := range chunk {
		switch {
		case b == 0x1b:
			e.mode = modeNormal
			e.inputBytes = nil
			e.redraw(true)
			e.onSceneChange()
			e.out.Flush()
			return false
		case b == '\r' || b == '\n':
			m := e.paletteMatches()
			e.mode = modeNormal
			e.inputBytes = nil
			e.redraw(true)
			if len(m) == 0 {
				e.status = "no such command (: again, or ? for keys)"
				e.drawAll()
				e.out.Flush()
				return false
			}
			sel := e.palSel
			if sel >= len(m) {
				sel = 0
			}
			e.status = ""
			quit = m[sel].run(e)
			if e.mode == modePalette { // a command can't reopen the palette
				e.mode = modeNormal
			}
			if !quit && e.mode == modeNormal && e.screen == 0 && !e.helpOpen {
				e.clampCursor()
				e.drawAll()
				e.onSceneChange()
			}
			if e.helpOpen {
				e.drawHelp()
			}
			if e.mode != modeNormal {
				e.drawFooter()
			}
			e.out.Flush()
			return quit
		case b == 0x0e || b == 0x09: // ctrl-n / Tab: next match
			e.palSel++
		case b == 0x10: // ctrl-p: previous match
			if e.palSel > 0 {
				e.palSel--
			}
		case b == 0x7f || b == 0x08:
			r := []rune(string(e.inputBytes))
			if len(r) > 0 {
				e.inputBytes = []byte(string(r[:len(r)-1]))
			}
			e.palSel = 0
		case b < 0x20:
		default:
			e.inputBytes = append(e.inputBytes, b)
			e.palSel = 0
		}
	}
	e.drawAll()
	e.drawPalette()
	e.out.Flush()
	return false
}

// drawPalette paints the ':' prompt and the ranked matches above the footer.
func (e *editor) drawPalette() {
	m := e.paletteMatches()
	show := len(m)
	if show > 8 {
		show = 8
	}
	if e.palSel >= len(m) && len(m) > 0 {
		e.palSel = len(m) - 1
	}
	boxW := e.w * 2 / 3
	if boxW < 40 {
		boxW = min(e.w-2, 40)
	}
	col := (e.w - boxW) / 2
	if col < 1 {
		col = 1
	}
	top := e.h - show - 3
	if top < 1 {
		top = 1
	}
	if e.kitty {
		kittyDeleteAll(e.out)
	}
	e.put(top, col, "\x1b[7m"+padRight(trunc(" : "+string(e.inputBytes)+"▏", boxW), boxW)+"\x1b[0m")
	for i := 0; i < show; i++ {
		line := fmt.Sprintf(" %-14s %s", m[i].name, m[i].desc)
		style := "\x1b[2m"
		if i == e.palSel {
			style = "\x1b[7m"
		}
		e.put(top+1+i, col, style+padRight(trunc(line, boxW), boxW)+"\x1b[0m")
	}
	if show == 0 {
		e.put(top+1, col, "\x1b[2m"+padRight(trunc(" no matching command", boxW), boxW)+"\x1b[0m")
	}
}
