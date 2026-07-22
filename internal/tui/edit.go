// Package tui implements movielily's interactive editor: a "more visible"
// view of a sequence (the edit-decision list). It is a hand-rolled terminal UI
// — no heavy framework — so it can place kitty-graphics image previews precisely
// without a render loop fighting over the screen.
//
// The editor only ever rewrites the sequence's plain-text file; footage is read
// (for frame previews) but never touched, keeping movielily's invariant.
package tui

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	_ "image/png" // register the PNG decoder for image.DecodeConfig
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	xterm "golang.org/x/term"

	"movielily/internal/ffmpeg"
	"movielily/internal/manim"
	"movielily/internal/model"
	"movielily/internal/mpv"
	"movielily/internal/project"
	"movielily/internal/store"
	"movielily/internal/typst"
)

const (
	modeNormal = iota
	modeEdit
	modeSearch
	modePalette
)

// inline-edit targets (what the modeEdit input line is editing)
const (
	editNote = iota
	editDur
	editGain
	editTitleTemplate // title-card wizard step 1: which template
	editTitleText     // title-card wizard step 2: the card's text
	editAnimTemplate  // animated-card wizard step 1
	editAnimText      // animated-card wizard step 2
	editBedFile       // bed wizard step 1: which audio file
	editBedGain       // bed wizard step 2: gain in dB
	editOvlFile       // overlay wizard step 1: which image
	editOvlSpec       // overlay wizard step 2: "at dur place"
)

type previewReq struct {
	gen               int
	isImage           bool
	firstSrc, lastSrc string
	firstAt, lastAt   float64

	// title cards render their PNG in the preview goroutine (typst is fast)
	titleTpl, titleText string
	// voice segments preview as a waveform of their exact slice
	wave            bool
	waveIn, waveOut float64
}

type previewRes struct {
	gen         int
	first, last string // png paths ("" if none/failed)
}

type editor struct {
	p    *project.Project
	name string
	path string

	items  []model.SequenceItem
	cursor int
	top    int // first visible row index
	marked map[int]bool
	undo   [][]model.SequenceItem
	dirty  bool

	mode       int
	inputBytes []byte // text being edited (note, title, duration, gain, search)
	editWhat   int    // which field the modeEdit input line is editing
	status     string

	clipboard    []model.SequenceItem // last deleted/yanked scenes, for p
	redoStack    [][]model.SequenceItem
	lastSearch   string
	helpOpen     bool
	wantReview   int    // 0 none · 1 from cursor · 2 whole cut
	lastTemplate string // last title-card template used, prefilled on T

	// animated-card wizard (A): the render runs suspended, like vim/mpv
	lastAnimTemplate string
	pendingAnimTpl   string
	pendingAnimText  string
	wantAnim         bool

	// snapshots tab (Tab): the git branch graph, read-only
	screen    int // 0 = editor · 1 = snapshots
	snapLines []string
	snapTop   int

	// async redo-in/out (Enter) and split-point picks (s): mpv runs in its
	// own window while the TUI stays live; results come back through this
	// channel (split=true carries a picked point instead of a trim)
	reselectCh   chan reselectRes
	reselectBusy bool

	palSel      int    // command palette (:) selection index
	pendingFile string // first answer of the bed/overlay wizards

	w, h  int
	kitty bool
	out   *bufio.Writer

	// stdin reader coordination — lets us hand the terminal to an external
	// editor (vim) without the reader goroutine stealing its keystrokes.
	paused       int32 // atomic
	parkedCh     chan struct{}
	resumeCh     chan struct{}
	wantVim      bool
	wantReselect bool

	// layout (recomputed on resize)
	leftW, rightW              int
	listTop, listBottom        int
	imgCol, imgCols, imgRows   int
	firstLabelRow, firstImgRow int
	lastLabelRow, lastImgRow   int
	detailsRow                 int

	// async preview
	gen               int
	curFirst, curLast string
	forceScene        bool
	tmpDir            string
	cache             map[string]string // previewer goroutine only
	reqCh             chan previewReq
	resCh             chan previewRes

	discard bool
}

// Edit opens the named sequence in the interactive editor. A missing sequence
// is seeded from the project's selects so there is something to look at.
func Edit(p *project.Project, name string) error {
	if !xterm.IsTerminal(int(os.Stdin.Fd())) || !xterm.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("movielily edit needs an interactive terminal")
	}

	path := p.Sequence(name)
	items, err := store.LoadSequence(path)
	if err != nil {
		return err
	}
	seeded := false
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) && len(items) == 0 {
		if sels, e := store.LoadSelects(p.Selects()); e == nil && len(sels) > 0 {
			for _, s := range sels {
				items = append(items, s.AsItem())
			}
			seeded = true
		}
	}

	tmp, err := os.MkdirTemp("", "movielily-edit-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	e := &editor{
		p:          p,
		name:       name,
		path:       path,
		items:      items,
		marked:     map[int]bool{},
		out:        bufio.NewWriter(os.Stdout),
		kitty:      kittySupported(),
		tmpDir:     tmp,
		cache:      map[string]string{},
		reqCh:      make(chan previewReq, 1),
		resCh:      make(chan previewRes, 1),
		parkedCh:   make(chan struct{}, 1),
		resumeCh:   make(chan struct{}, 1),
		reselectCh: make(chan reselectRes, 1),
	}
	if seeded {
		e.dirty = true
		e.status = fmt.Sprintf("seeded %d scene(s) from selects — press w to save", len(items))
	}

	st, err := xterm.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer xterm.Restore(int(os.Stdin.Fd()), st)

	e.w, e.h, _ = xterm.GetSize(int(os.Stdout.Fd()))
	if e.w < 40 || e.h < 10 {
		e.w, e.h = max(e.w, 80), max(e.h, 24)
	}
	e.computeLayout()

	io.WriteString(e.out, altScreenOn+hideCursor)
	e.out.Flush()
	defer func() {
		if e.kitty {
			kittyDeleteAll(e.out)
		}
		io.WriteString(e.out, showCursor+altScreenOff)
		e.out.Flush()
	}()

	go e.previewLoop()

	rawCh := make(chan []byte, 8)
	go func() {
		b := make([]byte, 64)
		for {
			if atomic.LoadInt32(&e.paused) == 1 {
				// Park while an external editor owns the terminal.
				select {
				case e.parkedCh <- struct{}{}:
				default:
				}
				<-e.resumeCh
				continue
			}
			// A short read deadline lets us notice a pause request mid-read
			// (on a TTY the poller honours deadlines). ErrDeadlineExceeded is
			// expected and just loops.
			_ = os.Stdin.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, err := os.Stdin.Read(b)
			if n > 0 && atomic.LoadInt32(&e.paused) == 0 {
				c := make([]byte, n)
				copy(c, b[:n])
				rawCh <- c
			}
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) {
					continue
				}
				return
			}
		}
	}()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	e.redraw(true)
	e.onSceneChange()
	e.out.Flush()

	for {
		select {
		case chunk := <-rawCh:
			quit := e.handleInput(chunk)
			if e.wantVim {
				e.wantVim = false
				if err := e.openInVim(st); err != nil {
					return err
				}
			}
			if e.wantReselect {
				e.wantReselect = false
				e.reselect()
			}
			if e.wantReview != 0 {
				whole := e.wantReview == 2
				e.wantReview = 0
				e.reviewOp(whole)
			}
			if e.wantAnim {
				e.wantAnim = false
				e.animRenderOp(st)
			}
			if quit {
				if e.dirty && !e.discard {
					if err := e.save(); err != nil {
						return err
					}
				}
				return nil
			}
		case res := <-e.resCh:
			if res.gen == e.gen {
				e.curFirst, e.curLast = res.first, res.last
				e.drawImages()
				e.out.Flush()
			}
		case r := <-e.reselectCh:
			e.applyReselect(r)
		case <-winch:
			e.w, e.h, _ = xterm.GetSize(int(os.Stdout.Fd()))
			e.computeLayout()
			e.redraw(true)
			e.onSceneChange()
			e.out.Flush()
		}
	}
}

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
	for rank := 0; rank <= 2; rank++ {
		for _, c := range palette {
			if fuzzyRank(c.name, q) == rank {
				out = append(out, c)
			}
		}
		if q == "" {
			break // empty query already collected everything at rank 2
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

// ---- watch the cut (r / R) -------------------------------------------------

// reviewOp plays the current in-memory cut (saved or not) as a simulated
// export: r from the cursor's scene, R from the top. mpv opens its own
// window; the editor never closes or blocks.
func (e *editor) reviewOp(whole bool) {
	if len(e.items) == 0 {
		return
	}
	from := 0
	if !whole {
		from = e.cursor
	}
	if err := mpv.ReviewDetached(e.p, e.name, e.items, from); err != nil {
		e.status = "mpv: " + err.Error()
	} else if whole {
		e.status = "playing the whole cut in an mpv window (nothing rendered)"
	} else {
		e.status = "playing from the cursor in an mpv window (nothing rendered)"
	}
	e.drawAll()
	e.out.Flush()
}

// ---- snapshots tab (Tab) ---------------------------------------------------

// openSnapshots loads the git branch graph for the project and switches to
// the read-only snapshots screen. Snapshots stay plain git: branch and merge
// with git itself; this tab is for SEEING the versions.
func (e *editor) openSnapshots() {
	e.snapLines = nil
	e.snapTop = 0
	if _, err := os.Stat(filepath.Join(e.p.Root, ".git")); err != nil {
		e.snapLines = []string{
			"no snapshots yet.",
			"",
			"take one from the shell:  movielily snapshot \"first cut\"",
			"exports auto-snapshot once a repo exists.",
		}
	} else {
		branch := gitOut(e.p.Root, "rev-parse", "--abbrev-ref", "HEAD")
		graph := gitOut(e.p.Root, "log", "--graph", "--all", "--decorate",
			"--date=format:%Y-%m-%d %H:%M", "--pretty=format:%h %ad %d %s", "-n", "300")
		if graph == "" {
			graph = "(no snapshots on any branch yet)"
		}
		e.snapLines = append(e.snapLines, "on branch: "+branch, "")
		e.snapLines = append(e.snapLines, strings.Split(graph, "\n")...)
	}
	e.screen = 1
	e.drawSnapshots()
	e.out.Flush()
}

func gitOut(root string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

func (e *editor) handleSnapshots(chunk []byte) {
	for _, b := range chunk {
		switch b {
		case 'j':
			if e.snapTop < len(e.snapLines)-1 {
				e.snapTop++
			}
		case 'k':
			if e.snapTop > 0 {
				e.snapTop--
			}
		case 'g':
			e.snapTop = 0
		case 'G':
			e.snapTop = max(0, len(e.snapLines)-(e.h-2))
		case 0x09, 'q', 0x1b: // Tab, q or Esc: back to the editor
			e.screen = 0
			e.redraw(true)
			e.onSceneChange()
			e.out.Flush()
			return
		}
	}
	e.drawSnapshots()
	e.out.Flush()
}

func (e *editor) drawSnapshots() {
	io.WriteString(e.out, clearScreen)
	if e.kitty {
		kittyDeleteAll(e.out)
	}
	head := fmt.Sprintf(" snapshots · %s · every commit is a version of the movie", e.p.Config.Name)
	io.WriteString(e.out, moveTo(1, 1)+"\x1b[7m"+padRight(trunc(head, e.w), e.w)+"\x1b[0m")
	rows := e.h - 2
	for i := 0; i < rows; i++ {
		idx := e.snapTop + i
		line := ""
		if idx < len(e.snapLines) {
			line = e.snapLines[idx]
		}
		style := ""
		if strings.Contains(line, "(") && strings.Contains(line, "->") {
			style = "\x1b[33m" // branch/HEAD decorations pop in yellow
		}
		io.WriteString(e.out, moveTo(2+i, 1)+style+visPad(trunc(line, e.w), e.w)+"\x1b[0m")
	}
	foot := " j/k scroll · Tab/q back · branch & merge with plain git (footage never enters the repo)"
	io.WriteString(e.out, moveTo(e.h, 1)+"\x1b[7m"+padRight(trunc(foot, e.w), e.w)+"\x1b[0m")
}

// sectionStats counts the playable scenes that fall under the section at idx
// (everything up to the next section) and their total duration.
func (e *editor) sectionStats(idx int) (n int, dur float64) {
	for i := idx + 1; i < len(e.items); i++ {
		if e.items[i].IsSection() {
			break
		}
		n++
		dur += e.items[i].Duration()
	}
	return n, dur
}

// sceneCount is the number of playable items (sections excluded).
func (e *editor) sceneCount() int {
	n := 0
	for _, it := range e.items {
		if !it.IsSection() {
			n++
		}
	}
	return n
}

func (e *editor) anyMarked() bool {
	for _, v := range e.marked {
		if v {
			return true
		}
	}
	return false
}

func (e *editor) pushUndo() {
	e.undo = append(e.undo, e.snapshotItems())
	if len(e.undo) > 100 {
		e.undo = e.undo[1:]
	}
	e.redoStack = nil // a fresh edit invalidates the redo line
}

func (e *editor) save() error {
	lines := []string{"# " + e.name + " — edited with movielily edit"}
	for _, it := range e.items {
		lines = append(lines, it.String())
	}
	if err := store.WriteLines(e.path, lines); err != nil {
		return err
	}
	e.dirty = false
	return nil
}

// ---- external programs (vim, mpv) -----------------------------------------

// suspend hands the terminal to an external program: it drops any images,
// leaves the alt screen, restores cooked mode, and parks the stdin reader so
// the child gets the keyboard cleanly.
func (e *editor) suspend(st *xterm.State) {
	if e.kitty {
		kittyDeleteAll(e.out)
	}
	io.WriteString(e.out, showCursor+altScreenOff)
	e.out.Flush()
	xterm.Restore(int(os.Stdin.Fd()), st)
	atomic.StoreInt32(&e.paused, 1)
	select {
	case <-e.parkedCh:
	case <-time.After(500 * time.Millisecond):
	}
	_ = os.Stdin.SetReadDeadline(time.Time{}) // clear deadline while the child runs
}

// resume takes the terminal back after an external program exits: raw mode,
// alt screen, reader un-parked, and the layout recomputed in case of a resize.
// The caller is responsible for redrawing. st is updated in place so the
// deferred Restore in Edit still targets the original cooked state.
func (e *editor) resume(st *xterm.State) {
	if ns, err := xterm.MakeRaw(int(os.Stdin.Fd())); err == nil {
		*st = *ns
	}
	atomic.StoreInt32(&e.paused, 0)
	select {
	case e.resumeCh <- struct{}{}:
	default:
	}
	io.WriteString(e.out, altScreenOn+hideCursor)
	e.w, e.h, _ = xterm.GetSize(int(os.Stdout.Fd()))
	if e.w < 40 || e.h < 10 {
		e.w, e.h = max(e.w, 80), max(e.h, 24)
	}
	e.computeLayout()
}

// openInVim writes the sequence to disk, opens the plain-text file in vim, and
// reloads it on return — so the same edit-decision list can be edited either
// way and the two stay in lock-step.
func (e *editor) openInVim(st *xterm.State) error {
	if err := e.save(); err != nil { // ensure vim sees the current state (and the file exists)
		e.status = "save failed: " + err.Error()
		e.drawAll()
		e.out.Flush()
		return nil
	}

	e.suspend(st)
	argv := append(vimCommand(), e.path)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	e.resume(st)

	// Pick up whatever vim left behind.
	if items, err := store.LoadSequence(e.path); err != nil {
		e.status = "reload failed: " + err.Error()
	} else {
		e.items = items
		e.marked = map[int]bool{}
		e.undo = nil
		e.dirty = false
		e.clampCursor()
		if runErr != nil {
			e.status = "vim: " + runErr.Error()
		} else {
			e.status = "reloaded from " + filepath.Base(e.path)
		}
	}

	e.redraw(true)
	e.onSceneChange()
	e.out.Flush()
	return nil
}

// reselectRes carries a confirmed mpv redo-in/out (or a split point, when
// split is set) back into the main loop.
type reselectRes struct {
	idx     int
	file    string
	in, out float64
	ok      bool
	err     error
	split   bool
	at      float64 // split: the picked source time
}

// reselect plays the current scene's full source clip in mpv so its in/out
// can be redone (Enter in the list). mpv opens its OWN window and the editor
// stays fully usable; the confirmed trim lands back via reselectCh. Sections
// and stills have no trim to redo.
func (e *editor) reselect() {
	if len(e.items) == 0 {
		return
	}
	if e.reselectBusy {
		e.status = "an mpv redo in/out is already open (confirm or close it first)"
		e.drawAll()
		e.out.Flush()
		return
	}
	it := e.items[e.cursor]
	// Enter OPENS whatever the cursor is on, always in a detached mpv window
	// so the editor never closes: clips fall through to the redo-in/out flow
	// below; everything else is simply shown or played.
	open := func(path, what string) {
		if err := mpv.OpenDetached(path); err != nil {
			e.status = "mpv: " + err.Error()
		} else {
			e.status = "opened " + what + " in an mpv window"
		}
		e.drawAll()
		e.out.Flush()
	}
	fail := func(msg string) {
		e.status = msg
		e.drawAll()
		e.out.Flush()
	}
	switch {
	case it.IsSection():
		fail("sections have no footage to open")
		return
	case it.Kind == model.KindUse:
		fail("this splices sequence " + strings.TrimSuffix(it.File, ".txt") + " here (edit it with: movielily edit " + strings.TrimSuffix(it.File, ".txt") + ")")
		return
	case it.Kind == model.KindImage || it.Kind == model.KindOverlay:
		if abs, err := e.p.ResolveFootage(it.File); err != nil {
			fail(err.Error())
		} else {
			open(abs, it.File)
		}
		return
	case it.Kind == model.KindAudio:
		if abs, err := e.p.ResolveFootage(it.File); err != nil {
			fail(err.Error())
		} else {
			open(abs, "bed "+it.File)
		}
		return
	case it.Kind == model.KindTitle:
		if png, err := typst.Render(e.p, it.File, it.Note); err != nil {
			fail("typst: " + err.Error())
		} else {
			open(png, "card "+strconv.Quote(it.Note))
		}
		return
	case it.Kind == model.KindAnim:
		if clip, ok := manim.Cached(e.p, it.File, it.Note); ok {
			open(clip, "animation "+strconv.Quote(it.Note))
		} else {
			fail("animation not rendered yet (it renders on export/review)")
		}
		return
	}

	e.reselectBusy = true
	idx, file := e.cursor, it.File
	go func(in, out float64) {
		nin, nout, ok, err := mpv.Reselect(e.p, file, in, out)
		e.reselectCh <- reselectRes{idx: idx, file: file, in: nin, out: nout, ok: ok, err: err}
	}(it.In, it.Out)
	e.status = "mpv opened: i/o set in/out, Enter confirms · the editor stays live"
	e.drawAll()
	e.out.Flush()
}

// applyReselect lands a finished redo-in/out. The list may have changed while
// mpv was open, so the scene is re-found by index/file before applying.
func (e *editor) applyReselect(r reselectRes) {
	e.reselectBusy = false
	switch {
	case r.err != nil:
		e.status = "mpv: " + r.err.Error()
	case r.split && r.ok:
		e.applySplit(r)
	case r.split:
		e.status = "split cancelled"
	case !r.ok:
		e.status = "in/out unchanged"
	default:
		idx := r.idx
		if idx >= len(e.items) || e.items[idx].File != r.file {
			idx = -1
			for i, it := range e.items { // the scene moved; find it again
				if it.Kind == model.KindVideo && it.File == r.file {
					idx = i
					break
				}
			}
		}
		if idx < 0 {
			e.status = "trim confirmed but the scene is gone (nothing applied)"
		} else {
			e.pushUndo()
			e.items[idx].In = r.in
			e.items[idx].Out = r.out
			e.dirty = true
			e.forceScene = true
			e.status = fmt.Sprintf("in/out %s–%s · w to save", mmss(r.in), mmss(r.out))
		}
	}
	e.drawAll()
	e.onSceneChange()
	e.out.Flush()
}

// vimCommand is the editor to launch. It honours $MOVIELILY_EDITOR for an
// override (so flags work, e.g. "vim -u NONE"), but defaults to plain vim
// rather than $EDITOR so the format opens in vim, not neovim or anything else.
func vimCommand() []string {
	if v := strings.TrimSpace(os.Getenv("MOVIELILY_EDITOR")); v != "" {
		if fields := strings.Fields(v); len(fields) > 0 {
			return fields
		}
	}
	return []string{"vim"}
}

// ---- preview (async) ------------------------------------------------------

func (e *editor) onSceneChange() {
	e.drawRight()
	if e.kitty {
		kittyDeleteAll(e.out)
	}
	e.requestPreview()
}

func (e *editor) requestPreview() {
	e.gen++
	e.curFirst, e.curLast = "", ""
	if !e.kitty || len(e.items) == 0 {
		return
	}
	it := e.items[e.cursor]
	if it.IsSection() || it.IsAudio() || it.Kind == model.KindUse {
		return
	}
	req := previewReq{gen: e.gen}
	switch {
	case it.Kind == model.KindTitle:
		// Rendered (or fetched from cache) by the preview goroutine.
		req.isImage = true
		req.titleTpl, req.titleText = it.File, it.Note
	case it.Kind == model.KindAnim:
		// Only preview an ALREADY rendered card; a cursor movement must
		// never kick off a heavy manim render in the background.
		clip, ok := manim.Cached(e.p, it.File, it.Note)
		if !ok {
			return
		}
		req.firstSrc, req.firstAt = clip, 0
		last := it.Dur - 0.05
		if last < 0 {
			last = 0
		}
		req.lastSrc, req.lastAt = clip, last
	case it.Kind == model.KindOverlay:
		abs, err := e.p.ResolveFootage(it.File)
		if err != nil {
			return
		}
		req.isImage = true
		req.firstSrc = abs
	case it.Kind == model.KindImage:
		abs, err := e.p.ResolveFootage(it.File)
		if err != nil {
			return
		}
		req.isImage = true
		req.firstSrc = abs
	case model.IsAudioFile(it.File):
		abs, err := e.p.ResolveFootage(it.File)
		if err != nil {
			return
		}
		req.wave = true
		req.firstSrc, req.waveIn, req.waveOut = abs, it.In, it.Out
	default:
		abs, err := e.p.ResolveFootage(it.File)
		if err != nil {
			return
		}
		req.firstSrc, req.firstAt = abs, it.In
		last := it.Out - 0.05
		if last < it.In {
			last = it.In
		}
		req.lastSrc, req.lastAt = abs, last
	}
	select { // drop a stale pending request
	case <-e.reqCh:
	default:
	}
	e.reqCh <- req
}

func (e *editor) previewLoop() {
	for req := range e.reqCh {
		// Coalesce: only render the most recent request.
	drain:
		for {
			select {
			case nr := <-e.reqCh:
				req = nr
			default:
				break drain
			}
		}
		res := previewRes{gen: req.gen}
		switch {
		case req.titleTpl != "":
			if png, err := typst.Render(e.p, req.titleTpl, req.titleText); err == nil {
				res.first = e.thumb(png, 0, true)
			}
		case req.wave:
			res.first = e.waveThumb(req.firstSrc, req.waveIn, req.waveOut)
		default:
			if req.firstSrc != "" {
				res.first = e.thumb(req.firstSrc, req.firstAt, req.isImage)
			}
			if req.lastSrc != "" {
				res.last = e.thumb(req.lastSrc, req.lastAt, req.isImage)
			}
		}
		e.resCh <- res
	}
}

func (e *editor) thumb(src string, at float64, isImage bool) string {
	key := fmt.Sprintf("%s@%s|%v", src, model.FormatSeconds(at), isImage)
	if p, ok := e.cache[key]; ok {
		return p
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	out := filepath.Join(e.tmpDir, fmt.Sprintf("%08x.png", h.Sum32()))
	if err := ffmpeg.Thumbnail(src, at, isImage, out); err != nil {
		return ""
	}
	e.cache[key] = out
	return out
}

func (e *editor) waveThumb(src string, in, out float64) string {
	key := fmt.Sprintf("wave|%s@%s-%s", src, model.FormatSeconds(in), model.FormatSeconds(out))
	if p, ok := e.cache[key]; ok {
		return p
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	png := filepath.Join(e.tmpDir, fmt.Sprintf("%08x.png", h.Sum32()))
	if err := ffmpeg.Waveform(src, in, out, png); err != nil {
		return ""
	}
	e.cache[key] = png
	return png
}

// ---- rendering ------------------------------------------------------------

func (e *editor) computeLayout() {
	e.rightW = e.w * 42 / 100
	if e.rightW < 34 {
		e.rightW = 34
	}
	if e.rightW > e.w-26 {
		e.rightW = e.w - 26
	}
	if e.rightW < 20 {
		e.rightW = 20
	}
	e.leftW = e.w - e.rightW - 1

	e.listTop = 2
	e.listBottom = e.h - 1
	e.imgCol = e.leftW + 3
	e.imgCols = e.rightW - 4
	if e.imgCols < 10 {
		e.imgCols = e.rightW - 2
	}

	avail := e.listBottom - e.listTop + 1
	e.imgRows = (avail - 2 - 5 - 2) / 2
	for {
		if e.imgRows < 3 {
			e.imgRows = 3
		}
		if e.imgRows > 16 {
			e.imgRows = 16
		}
		e.firstLabelRow = e.listTop
		e.firstImgRow = e.firstLabelRow + 1
		e.lastLabelRow = e.firstImgRow + e.imgRows + 1
		e.lastImgRow = e.lastLabelRow + 1
		e.detailsRow = e.lastImgRow + e.imgRows + 1
		if e.detailsRow <= e.h-1 || e.imgRows <= 3 {
			break
		}
		e.imgRows--
	}
}

func (e *editor) redraw(full bool) {
	if full {
		io.WriteString(e.out, clearScreen)
	}
	e.drawHeader()
	e.drawList()
	e.drawFooter()
	if full {
		e.drawRight()
	}
}

func (e *editor) drawAll() {
	e.drawHeader()
	e.drawList()
	e.drawFooter()
}

func (e *editor) drawHeader() {
	total := 0.0
	for _, it := range e.items {
		total += it.Duration()
	}
	d := ""
	if e.dirty {
		d = " ●"
	}
	s := fmt.Sprintf(" movielily edit · %s · %d scene(s) · %s%s", e.name, e.sceneCount(), mmss(total), d)
	io.WriteString(e.out, moveTo(1, 1)+"\x1b[7m"+padRight(trunc(s, e.w), e.w)+"\x1b[0m")
}

func (e *editor) drawFooter() {
	var s string
	switch {
	case e.mode == modeSearch:
		s = " search ▸ " + string(e.inputBytes) + "▏"
	case e.mode == modeEdit:
		label := "note"
		switch e.editWhat {
		case editDur:
			label = "duration (s)"
		case editGain:
			label = "gain (dB)"
		case editTitleTemplate:
			label = "card template (Enter accepts)"
		case editTitleText:
			label = "card text"
		case editAnimTemplate:
			label = "anim template (Enter accepts)"
		case editAnimText:
			label = "anim text"
		case editBedFile:
			label = "bed audio file"
		case editBedGain:
			label = "bed gain (dB)"
		case editOvlFile:
			label = "overlay image"
		case editOvlSpec:
			label = "overlay: at dur [place]"
		default:
			if len(e.items) > 0 && e.items[e.cursor].IsSection() {
				label = "title"
			}
		}
		s = " " + label + " ▸ " + string(e.inputBytes) + "▏"
	case e.status != "":
		s = " " + e.status
	default:
		s = " ? help · : commands · j/k · ⏎ in/out · r watch · T card · e note · t number · d/p cut/paste · w save · q quit"
	}
	io.WriteString(e.out, moveTo(e.h, 1)+"\x1b[7m"+padRight(trunc(s, e.w), e.w)+"\x1b[0m")
}

// drawHelp paints the key reference over the list; any key closes it.
func (e *editor) drawHelp() {
	lines := []string{
		"  movielily edit · keys                                ",
		"                                                       ",
		"  j/k ↑/↓  move            ⏎    open item / redo in-out  ",
		"  J/K      reorder         +/-  nudge out/duration/gain ",
		"  s        split the clip at a point picked in mpv      ",
		"  < / >    nudge the clip's in point                    ",
		"  g/G      top / bottom    t    duration (gain on beds) ",
		"  [/]      prev/next sect  e    edit note or card text  ",
		"  space    mark            y    yank marked/current     ",
		"  d        cut             p    paste below cursor      ",
		"  /        search          n/N  next / prev match       ",
		"  r        watch from here (simulated export, no render)",
		"  R        watch the whole cut                          ",
		"  T        title card      A    animated card           ",
		"  o        new section     v    edit the file in vim    ",
		"  u        undo            ^R   redo                    ",
		"  Tab      snapshots tab (git branch graph)             ",
		"  :        command palette (fuzzy: type a few letters)  ",
		"  w        save            q/Q  quit (save / discard)   ",
		"                                                       ",
		"  any key closes this                                   ",
	}
	boxW := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > boxW {
			boxW = n
		}
	}
	if boxW > e.w-2 {
		boxW = e.w - 2
	}
	top := (e.h - len(lines)) / 2
	if top < 1 {
		top = 1
	}
	col := (e.w - boxW) / 2
	if col < 1 {
		col = 1
	}
	if e.kitty {
		kittyDeleteAll(e.out)
	}
	for i, l := range lines {
		if top+i > e.h {
			break
		}
		e.put(top+i, col, "\x1b[7m"+padRight(trunc(l, boxW), boxW)+"\x1b[0m")
	}
}

// span is one styled run of a list row. The text is always plain (no escapes)
// so its visible width is just its rune count; style holds SGR params ("" for
// the terminal default). Rows are built as spans so the cursor highlight, the
// fixed-column alignment, and width-correct truncation are all exact.
type span struct {
	text  string
	style string
}

// listMetrics are the per-frame measurements that shape every row: how scenes
// are numbered and how wide each column is. Computed once per draw so all
// rows stay on one grid.
type listMetrics struct {
	hasSections bool
	sceneNo     []int // item index -> 1-based scene number (0 for sections)
	numW        int
	fileW       int
	indent      int
}

func (e *editor) metrics() listMetrics {
	m := listMetrics{sceneNo: make([]int, len(e.items))}
	n, maxNo := 0, 0
	for i, it := range e.items {
		if it.IsSection() {
			m.hasSections = true
			continue
		}
		n++
		m.sceneNo[i], maxNo = n, n
	}
	if m.numW = len(strconv.Itoa(maxNo)); m.numW < 2 {
		m.numW = 2
	}
	if m.hasSections {
		m.indent = 2
	}
	switch {
	case e.leftW >= 54:
		m.fileW = 16
	case e.leftW >= 40:
		m.fileW = 12
	default:
		m.fileW = 9
	}
	return m
}

func (e *editor) drawList() {
	visible := e.listBottom - e.listTop + 1
	if e.cursor < e.top {
		e.top = e.cursor
	}
	if e.cursor >= e.top+visible {
		e.top = e.cursor - visible + 1
	}
	if e.top < 0 {
		e.top = 0
	}
	m := e.metrics()
	for i := 0; i < visible; i++ {
		row := e.listTop + i
		idx := e.top + i
		var line string
		switch {
		case idx < len(e.items):
			var spans []span
			if e.items[idx].IsSection() {
				spans = e.sectionSpans(idx, m)
			} else {
				spans = e.sceneSpans(idx, m)
			}
			line = renderRow(spans, e.leftW, idx == e.cursor)
		case idx == 0 && len(e.items) == 0:
			hint := "  (empty sequence — press o to add a section · v for vim · q to quit)"
			line = "\x1b[2m" + visPad(hint, e.leftW) + "\x1b[0m"
		default:
			line = strings.Repeat(" ", e.leftW)
		}
		io.WriteString(e.out, moveTo(row, 1)+line+e.dividerCell(i, visible))
	}
}

// renderRow lays spans into a field of exactly w cells. The cursor row is drawn
// reverse-video with per-span colours dropped (a reset would cancel the
// inverse); every other row keeps its colours and is space-padded to width.
func renderRow(spans []span, w int, cursor bool) string {
	if cursor {
		var plain strings.Builder
		for _, s := range spans {
			plain.WriteString(s.text)
		}
		return "\x1b[7m" + visPad(plain.String(), w) + "\x1b[0m"
	}
	var b strings.Builder
	width := 0
	for _, s := range spans {
		if width >= w {
			break
		}
		t := s.text
		if width+len([]rune(t)) > w {
			t = trunc(t, w-width)
		}
		width += len([]rune(t))
		if s.style != "" {
			b.WriteString("\x1b[" + s.style + "m" + t + "\x1b[0m")
		} else {
			b.WriteString(t)
		}
	}
	if width < w {
		b.WriteString(strings.Repeat(" ", w-width))
	}
	return b.String()
}

func (e *editor) sceneSpans(idx int, m listMetrics) []span {
	it := e.items[idx]
	sp := make([]span, 0, 8)
	if m.indent > 0 {
		sp = append(sp, span{text: strings.Repeat(" ", m.indent)})
	}
	if e.marked[idx] {
		sp = append(sp, span{text: "▌ ", style: "1;33"})
	} else {
		// One glyph + one colour per kind, so the list reads at a glance:
		// clips plain, voice green, stills magenta, cards yellow, animated
		// cards cyan, beds blue, overlays dim magenta.
		icon, style := "▶ ", "2"
		switch {
		case it.Kind == model.KindImage:
			icon, style = "▦ ", "35"
		case it.Kind == model.KindTitle:
			icon, style = "▣ ", "33"
		case it.Kind == model.KindAnim:
			icon, style = "✦ ", "36"
		case it.Kind == model.KindAudio:
			icon, style = "♪ ", "34"
		case it.Kind == model.KindOverlay:
			icon, style = "◱ ", "2;35"
		case it.Kind == model.KindUse:
			icon, style = "⧉ ", "1;36"
		case model.IsAudioFile(it.File):
			icon, style = "∿ ", "32"
		}
		sp = append(sp, span{text: icon, style: style})
	}
	sp = append(sp, span{text: padLeft(strconv.Itoa(m.sceneNo[idx]), m.numW) + " ", style: "2"})
	sp = append(sp, span{text: padRight(it.File, m.fileW) + "  "})
	sp = append(sp, span{text: padLeft(durLabel(it), 5) + " ", style: "2"})
	return appendNoteSpans(sp, it.Note)
}

func (e *editor) sectionSpans(idx int, m listMetrics) []span {
	title := strings.TrimSpace(e.items[idx].Note)
	if title == "" {
		title = "untitled section"
	}
	n, dur := e.sectionStats(idx)
	head := "▌ " + strings.ToUpper(title)
	stats := fmt.Sprintf("%d · %s", n, mmss(dur))
	sp := []span{{text: head, style: "1;36"}}
	if leaderW := e.leftW - len([]rune(head)) - len([]rune(stats)) - 2; leaderW >= 1 {
		sp = append(sp, span{text: " " + strings.Repeat("·", leaderW) + " ", style: "2"})
		sp = append(sp, span{text: stats, style: "36"})
	}
	return sp
}

func durLabel(it model.SequenceItem) string {
	switch it.Kind {
	case model.KindAudio:
		return trimf(it.Gain) + "dB"
	case model.KindOverlay:
		return mmss(it.Dur)
	case model.KindUse:
		return "seq"
	}
	return mmss(it.Duration())
}

// tagInline matches an inline #hashtag (mirrors model's tag grammar) so the
// list can highlight tags where they sit inside a note.
var tagInline = regexp.MustCompile(`#[\p{L}\p{N}_\-]+`)

// appendNoteSpans appends the note as the row's trailing free text, splitting
// out #tags so they can be coloured in place.
func appendNoteSpans(dst []span, note string) []span {
	note = strings.TrimSpace(note)
	if note == "" {
		return dst
	}
	locs := tagInline.FindAllStringIndex(note, -1)
	pos := 0
	for _, loc := range locs {
		if loc[0] > pos {
			dst = append(dst, span{text: note[pos:loc[0]]})
		}
		dst = append(dst, span{text: note[loc[0]:loc[1]], style: "33"})
		pos = loc[1]
	}
	if pos < len(note) {
		dst = append(dst, span{text: note[pos:]})
	}
	return dst
}

// dividerCell draws the column rule between the list and the preview, doubling
// as a scrollbar: a brighter thumb marks where the viewport sits when the list
// is taller than the screen.
func (e *editor) dividerCell(i, visible int) string {
	ch, style := "│", "90"
	if n := len(e.items); n > visible && visible > 0 {
		thumb := visible * visible / n
		if thumb < 1 {
			thumb = 1
		}
		pos := e.top * visible / n
		if pos+thumb > visible {
			pos = visible - thumb
		}
		if i >= pos && i < pos+thumb {
			ch, style = "┃", "1;36"
		}
	}
	return "\x1b[" + style + "m" + ch + "\x1b[0m"
}

func (e *editor) drawRight() {
	for r := e.listTop; r <= e.listBottom; r++ {
		e.put(r, e.leftW+2, strings.Repeat(" ", e.rightW))
	}
	if len(e.items) == 0 {
		return
	}
	it := e.items[e.cursor]
	col := e.leftW + 3

	if it.IsSection() {
		title := strings.TrimSpace(it.Note)
		if title == "" {
			title = "(untitled section)"
		}
		n, dur := e.sectionStats(e.cursor)
		e.put(e.firstLabelRow, col, "\x1b[1;36msection\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[1m"+trunc(title, e.rightW-2)+"\x1b[0m")
		e.put(e.firstImgRow+2, col, fmt.Sprintf("%d scene(s) · %s", n, mmss(dur)))
		e.put(e.firstImgRow+4, col, "\x1b[2me rename · o new section · d delete\x1b[0m")
		return
	}

	switch {
	case !e.kitty:
		e.put(e.firstLabelRow, col, "\x1b[2mpreview needs a kitty-compatible terminal\x1b[0m")
	case it.Kind == model.KindAudio:
		e.put(e.firstLabelRow, col, "\x1b[1;34maudio bed\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[2mplays under the whole export\x1b[0m")
	case it.Kind == model.KindTitle:
		e.put(e.firstLabelRow, col, "\x1b[1;33mtitle card\x1b[0m  \x1b[2m"+trunc(it.File, 20)+"\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[2mrendering…\x1b[0m")
	case it.Kind == model.KindAnim:
		e.put(e.firstLabelRow, col, "\x1b[1;36manimated card\x1b[0m  \x1b[2m"+trunc(it.File, 20)+"\x1b[0m")
		if _, ok := manim.Cached(e.p, it.File, it.Note); ok {
			e.put(e.firstImgRow, col, "\x1b[2mrendering…\x1b[0m")
		} else {
			e.put(e.firstImgRow, col, "\x1b[2mnot rendered yet (renders on export/review)\x1b[0m")
		}
	case it.Kind == model.KindUse:
		e.put(e.firstLabelRow, col, "\x1b[1;36mnested sequence\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[2msplices "+trunc(strings.TrimSuffix(it.File, ".txt"), 24)+" here on review/export\x1b[0m")
	case it.Kind == model.KindOverlay:
		e.put(e.firstLabelRow, col, "\x1b[1;35moverlay\x1b[0m  \x1b[2mrides the scene above\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[2mrendering…\x1b[0m")
	case it.Kind == model.KindImage:
		e.put(e.firstLabelRow, col, "\x1b[1;35mimage\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[2mrendering…\x1b[0m")
	case model.IsAudioFile(it.File):
		e.put(e.firstLabelRow, col, "\x1b[1;32mvoice\x1b[0m  \x1b[2mwaveform of this segment\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[2mrendering…\x1b[0m")
	default:
		e.put(e.firstLabelRow, col, "\x1b[1mfirst frame\x1b[0m  \x1b[2m"+mmss(it.In)+"\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[2mrendering…\x1b[0m")
		e.put(e.lastLabelRow, col, "\x1b[1mlast frame\x1b[0m  \x1b[2m"+mmss(it.Out)+"\x1b[0m")
		e.put(e.lastImgRow, col, "\x1b[2mrendering…\x1b[0m")
	}

	// Where this scene sits in the finished movie, for orientation (and for
	// eyeballing YouTube chapter times).
	at := 0.0
	for i := 0; i < e.cursor && i < len(e.items); i++ {
		at += e.items[i].Duration()
	}

	dr := e.detailsRow
	e.put(dr, col, "\x1b[1m"+trunc(it.File, e.rightW-2)+"\x1b[0m")
	switch {
	case it.Kind == model.KindImage:
		e.put(dr+1, col, fmt.Sprintf("still · %ss · at %s", trimf(it.Dur), mmss(at)))
	case it.Kind == model.KindTitle:
		e.put(dr+1, col, fmt.Sprintf("card · %ss · at %s", trimf(it.Dur), mmss(at)))
	case it.Kind == model.KindAnim:
		e.put(dr+1, col, fmt.Sprintf("anim · %ss · at %s", trimf(it.Dur), mmss(at)))
	case it.Kind == model.KindOverlay:
		e.put(dr+1, col, fmt.Sprintf("+%ss for %ss @ %s", trimf(it.In), trimf(it.Dur), it.Place))
	case it.Kind == model.KindAudio:
		e.put(dr+1, col, fmt.Sprintf("bed · %sdB · under the whole cut", trimf(it.Gain)))
	case model.IsAudioFile(it.File):
		e.put(dr+1, col, fmt.Sprintf("voice %s → %s  (%ss) · at %s", mmss(it.In), mmss(it.Out), trimf(it.Duration()), mmss(at)))
	default:
		e.put(dr+1, col, fmt.Sprintf("%s → %s  (%ss) · at %s", mmss(it.In), mmss(it.Out), trimf(it.Duration()), mmss(at)))
	}
	if tags := model.Tags(it.Note); len(tags) > 0 {
		e.put(dr+2, col, "\x1b[33m"+trunc(strings.Join(tags, " "), e.rightW-2)+"\x1b[0m")
	}
	if note := strings.TrimSpace(it.Note); note != "" {
		e.put(dr+3, col, "\x1b[2m"+trunc(note, e.rightW-2)+"\x1b[0m")
	}
}

func (e *editor) drawImages() {
	if !e.kitty {
		return
	}
	kittyDeleteAll(e.out)
	clear := func(top int) {
		for r := 0; r < e.imgRows; r++ {
			e.put(top+r, e.imgCol, strings.Repeat(" ", e.imgCols))
		}
	}
	if e.curFirst != "" {
		clear(e.firstImgRow)
		e.placeImage(e.curFirst, e.firstImgRow)
	}
	if e.curLast != "" {
		clear(e.lastImgRow)
		e.placeImage(e.curLast, e.lastImgRow)
	}
	io.WriteString(e.out, moveTo(e.h, e.w)) // park cursor
}

// placeImage draws the PNG at path inside the imgCols×imgRows box whose top is
// `top`, preserving the image's aspect ratio (no stretching) and centering it
// within the box.
func (e *editor) placeImage(path string, top int) {
	cols, rows := e.fitCells(path)
	colOff := (e.imgCols - cols) / 2
	rowOff := (e.imgRows - rows) / 2
	if colOff < 0 {
		colOff = 0
	}
	if rowOff < 0 {
		rowOff = 0
	}
	kittyPlace(e.out, path, top+rowOff, e.imgCol+colOff, cols, rows)
}

// fitCells returns the largest cols×rows cell box (within imgCols×imgRows) that
// matches the image's pixel aspect ratio, so kitty's scale-to-fill does not
// distort it. It accounts for the terminal's non-square cells.
func (e *editor) fitCells(path string) (cols, rows int) {
	maxC, maxR := e.imgCols, e.imgRows
	iw, ih := imageSize(path)
	if iw <= 0 || ih <= 0 {
		return maxC, maxR
	}
	cw, ch := cellPixels() // pixels per cell (width, height)
	// A cols×rows box has pixel aspect (cols*cw)/(rows*ch); set it equal to the
	// image's iw/ih and solve for the cols:rows ratio.
	ratio := (float64(iw) / float64(ih)) * (ch / cw)
	cols = maxC
	rows = int(math.Round(float64(cols) / ratio))
	if rows > maxR {
		rows = maxR
		cols = int(math.Round(float64(rows) * ratio))
	}
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	if cols > maxC {
		cols = maxC
	}
	if rows > maxR {
		rows = maxR
	}
	return cols, rows
}

// imageSize reads a PNG's pixel dimensions from its header (cheap; no full
// decode). Returns 0,0 if it can't be read.
func imageSize(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// cellPixels returns the terminal's pixel size of one character cell. It asks
// the TTY (TIOCGWINSZ); when that reports nothing (some terminals), it falls
// back to a typical cell that is roughly twice as tall as it is wide.
func cellPixels() (w, h float64) {
	if ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ); err == nil &&
		ws.Xpixel > 0 && ws.Ypixel > 0 && ws.Col > 0 && ws.Row > 0 {
		return float64(ws.Xpixel) / float64(ws.Col), float64(ws.Ypixel) / float64(ws.Row)
	}
	return 1, 2
}

func (e *editor) put(row, col int, s string) {
	if row < 1 || row > e.h || col < 1 || col > e.w {
		return
	}
	// Truncate by *visible* width even when s carries SGR escapes — otherwise an
	// over-long right-pane label wraps onto, and corrupts, the left pane.
	io.WriteString(e.out, moveTo(row, col)+visTrunc(s, e.w-col+1))
}

// ---- terminal + kitty helpers ---------------------------------------------

const (
	esc          = "\x1b"
	altScreenOn  = esc + "[?1049h"
	altScreenOff = esc + "[?1049l"
	hideCursor   = esc + "[?25l"
	showCursor   = esc + "[?25h"
	clearScreen  = esc + "[2J"
)

func moveTo(row, col int) string { return fmt.Sprintf("\x1b[%d;%dH", row, col) }

func kittySupported() bool {
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return true
	}
	if strings.Contains(os.Getenv("TERM"), "kitty") {
		return true
	}
	if os.Getenv("GHOSTTY_RESOURCES_DIR") != "" || os.Getenv("GHOSTTY_BIN_DIR") != "" {
		return true
	}
	tp := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	return strings.Contains(tp, "wezterm") || strings.Contains(tp, "ghostty")
}

// kittyPlace transmits the PNG at path by reference and displays it scaled into
// a cols×rows cell box whose top-left is (row,col). q=2 silences the terminal's
// acknowledgements so they don't land in our input stream; C=1 keeps the cursor
// put; t=f leaves the file in place (we clean the temp dir ourselves).
func kittyPlace(w *bufio.Writer, path string, row, col, cols, rows int) {
	b64 := base64.StdEncoding.EncodeToString([]byte(path))
	fmt.Fprintf(w, "\x1b[%d;%dH", row, col)
	fmt.Fprintf(w, "\x1b_Ga=T,f=100,t=f,c=%d,r=%d,C=1,q=2;%s\x1b\\", cols, rows, b64)
}

func kittyDeleteAll(w *bufio.Writer) { io.WriteString(w, "\x1b_Ga=d,q=2\x1b\\") }

// ---- small helpers --------------------------------------------------------

func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

func padRight(s string, w int) string {
	r := []rune(s)
	if len(r) >= w {
		return trunc(s, w)
	}
	return s + strings.Repeat(" ", w-len(r))
}

func padLeft(s string, w int) string {
	if r := []rune(s); len(r) < w {
		return strings.Repeat(" ", w-len(r)) + s
	}
	return s
}

// isSGRFinal reports whether r ends an ANSI escape sequence we care about (an
// SGR sequence ends in a letter, e.g. the 'm' of "\x1b[1;36m").
func isSGRFinal(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// visWidth is s's width in terminal cells, ignoring ANSI SGR escapes. Every
// visible rune counts as one cell, matching this file's single-width assumption.
func visWidth(s string) int {
	n, inEsc := 0, false
	for _, r := range s {
		switch {
		case inEsc:
			if isSGRFinal(r) {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			n++
		}
	}
	return n
}

// visTrunc truncates s to at most w visible cells — ANSI SGR escapes pass
// through without counting — appending an ellipsis when anything is cut. A
// reset is appended whenever s carried an escape, so styling never leaks past.
func visTrunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if visWidth(s) <= w {
		return s
	}
	var b strings.Builder
	hadEsc, inEsc, count := false, false, 0
	for _, r := range s {
		switch {
		case inEsc:
			b.WriteRune(r)
			if isSGRFinal(r) {
				inEsc = false
			}
		case r == 0x1b:
			inEsc, hadEsc = true, true
			b.WriteRune(r)
		case count == w-1:
			b.WriteRune('…')
			count++
		default:
			b.WriteRune(r)
			count++
		}
		if count >= w {
			break
		}
	}
	if hadEsc {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// visPad right-pads s with spaces to exactly w visible cells (truncating when
// longer), so styled segments line up on a fixed grid.
func visPad(s string, w int) string {
	n := visWidth(s)
	if n >= w {
		return visTrunc(s, w)
	}
	return s + strings.Repeat(" ", w-n)
}

func mmss(t float64) string {
	if t < 0 {
		t = 0
	}
	s := int(t + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func trimf(f float64) string { return model.FormatSeconds(math.Round(f*10) / 10) }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
