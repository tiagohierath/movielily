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
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	xterm "golang.org/x/term"

	"movielily/internal/ffmpeg"
	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

const (
	modeNormal = iota
	modeEdit
)

type previewReq struct {
	gen               int
	isImage           bool
	firstSrc, lastSrc string
	firstAt, lastAt   float64
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
	inputBytes []byte // note being edited
	status     string

	w, h  int
	kitty bool
	out   *bufio.Writer

	// stdin reader coordination — lets us hand the terminal to an external
	// editor (vim) without the reader goroutine stealing its keystrokes.
	paused   int32 // atomic
	parkedCh chan struct{}
	resumeCh chan struct{}
	wantVim  bool

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
		p:        p,
		name:     name,
		path:     path,
		items:    items,
		marked:   map[int]bool{},
		out:      bufio.NewWriter(os.Stdout),
		kitty:    kittySupported(),
		tmpDir:   tmp,
		cache:    map[string]string{},
		reqCh:    make(chan previewReq, 1),
		resCh:    make(chan previewRes, 1),
		parkedCh: make(chan struct{}, 1),
		resumeCh: make(chan struct{}, 1),
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
	if e.mode == modeEdit {
		e.handleEdit(chunk)
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
			case ' ':
				e.toggleMark()
			case 'e', '\r', '\n':
				e.startEdit()
			case 'o':
				e.addSection()
			case 'v':
				e.wantVim = true
			case 'd':
				e.deleteSel()
			case 'u':
				e.undoOp()
			case 'w':
				e.saveOp()
			}
		}
	}

	if e.mode == modeEdit { // startEdit switched us
		e.drawFooter()
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
		for i, it := range e.items {
			if !e.marked[i] {
				kept = append(kept, it)
			}
		}
		n := len(e.items) - len(kept)
		e.items = kept
		e.marked = map[int]bool{}
		e.status = fmt.Sprintf("deleted %d scene(s)", n)
	} else {
		e.items = append(e.items[:e.cursor], e.items[e.cursor+1:]...)
		e.status = "deleted scene"
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
	last := len(e.undo) - 1
	e.items = e.undo[last]
	e.undo = e.undo[:last]
	e.marked = map[int]bool{}
	e.clampCursor()
	e.dirty = true
	e.forceScene = true
	e.status = "undo"
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
	e.inputBytes = []byte(e.items[e.cursor].Note)
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
	e.pushUndo()
	isSection := e.items[e.cursor].IsSection()
	e.items[e.cursor].Note = strings.TrimSpace(string(e.inputBytes))
	e.inputBytes = nil
	e.mode = modeNormal
	e.dirty = true
	if isSection {
		e.status = "section title set — w to save"
	} else {
		e.status = "note updated — w to save"
	}
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
	cp := make([]model.SequenceItem, len(e.items))
	copy(cp, e.items)
	e.undo = append(e.undo, cp)
	if len(e.undo) > 100 {
		e.undo = e.undo[1:]
	}
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

// ---- external editor ------------------------------------------------------

// openInVim writes the sequence to disk, suspends the TUI, opens the plain-text
// file in vim, and reloads it on return — so the same edit-decision list can be
// edited either way and the two stay in lock-step. st is updated in place so
// the deferred Restore in Edit still targets the original cooked state.
func (e *editor) openInVim(st *xterm.State) error {
	if err := e.save(); err != nil { // ensure vim sees the current state (and the file exists)
		e.status = "save failed: " + err.Error()
		e.drawAll()
		e.out.Flush()
		return nil
	}

	// Hand the terminal to vim: drop images, leave the alt screen, restore
	// cooked mode, and park the reader so it doesn't eat vim's keystrokes.
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
	_ = os.Stdin.SetReadDeadline(time.Time{}) // clear deadline while vim runs

	argv := append(vimCommand(), e.path)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()

	// Take the terminal back.
	if ns, err := xterm.MakeRaw(int(os.Stdin.Fd())); err == nil {
		*st = *ns
	}
	atomic.StoreInt32(&e.paused, 0)
	select {
	case e.resumeCh <- struct{}{}:
	default:
	}
	io.WriteString(e.out, altScreenOn+hideCursor)

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

	e.w, e.h, _ = xterm.GetSize(int(os.Stdout.Fd()))
	if e.w < 40 || e.h < 10 {
		e.w, e.h = max(e.w, 80), max(e.h, 24)
	}
	e.computeLayout()
	e.redraw(true)
	e.onSceneChange()
	e.out.Flush()
	return nil
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
	if it.IsSection() {
		return
	}
	abs, err := e.p.ResolveFootage(it.File)
	if err != nil {
		return
	}
	req := previewReq{gen: e.gen, isImage: it.Kind == model.KindImage}
	if it.Kind == model.KindImage {
		req.firstSrc, req.firstAt = abs, 0
	} else {
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
		if req.firstSrc != "" {
			res.first = e.thumb(req.firstSrc, req.firstAt, req.isImage)
		}
		if req.lastSrc != "" {
			res.last = e.thumb(req.lastSrc, req.lastAt, req.isImage)
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
	case e.mode == modeEdit:
		label := "note"
		if len(e.items) > 0 && e.items[e.cursor].IsSection() {
			label = "title"
		}
		s = " " + label + " ▸ " + string(e.inputBytes) + "▏"
	case e.status != "":
		s = " " + e.status
	default:
		s = " j/k move · J/K reorder · o section · v vim · space mark · e note · d del · u undo · w save · q quit"
	}
	io.WriteString(e.out, moveTo(e.h, 1)+"\x1b[7m"+padRight(trunc(s, e.w), e.w)+"\x1b[0m")
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
	for i := 0; i < visible; i++ {
		row := e.listTop + i
		idx := e.top + i
		var line string
		switch {
		case idx < len(e.items):
			line = e.renderItem(idx)
		case idx == 0 && len(e.items) == 0:
			line = "  (empty sequence — press q to quit)"
		}
		content := padRight(line, e.leftW)
		isSection := idx < len(e.items) && e.items[idx].IsSection()
		io.WriteString(e.out, moveTo(row, 1))
		switch {
		case idx == e.cursor && len(e.items) > 0:
			io.WriteString(e.out, "\x1b[7m"+content+"\x1b[0m")
		case isSection:
			io.WriteString(e.out, "\x1b[1;36m"+content+"\x1b[0m")
		default:
			io.WriteString(e.out, content)
		}
		io.WriteString(e.out, "\x1b[90m│\x1b[0m")
	}
}

func (e *editor) renderItem(idx int) string {
	it := e.items[idx]
	if it.IsSection() {
		title := strings.TrimSpace(it.Note)
		if title == "" {
			title = "untitled section"
		}
		return "▌ " + strings.ToUpper(title)
	}
	mark := " "
	if e.marked[idx] {
		mark = "◉"
	}
	num := fmt.Sprintf("%2d", idx+1)
	if it.Kind == model.KindImage {
		return fmt.Sprintf("%s %s %s  img %ss  %s", mark, num, padRight(it.File, 16), trimf(it.Dur), it.Note)
	}
	return fmt.Sprintf("%s %s %s  %s–%s  %s", mark, num, padRight(it.File, 16), mmss(it.In), mmss(it.Out), it.Note)
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
	case it.Kind == model.KindImage:
		e.put(e.firstLabelRow, col, "\x1b[1mimage\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[2mrendering…\x1b[0m")
	default:
		e.put(e.firstLabelRow, col, "\x1b[1mfirst frame\x1b[0m  \x1b[2m"+mmss(it.In)+"\x1b[0m")
		e.put(e.firstImgRow, col, "\x1b[2mrendering…\x1b[0m")
		e.put(e.lastLabelRow, col, "\x1b[1mlast frame\x1b[0m  \x1b[2m"+mmss(it.Out)+"\x1b[0m")
		e.put(e.lastImgRow, col, "\x1b[2mrendering…\x1b[0m")
	}

	dr := e.detailsRow
	e.put(dr, col, "\x1b[1m"+trunc(it.File, e.rightW-2)+"\x1b[0m")
	if it.Kind == model.KindImage {
		e.put(dr+1, col, "still · "+trimf(it.Dur)+"s")
	} else {
		e.put(dr+1, col, fmt.Sprintf("%s → %s  (%ss)", mmss(it.In), mmss(it.Out), trimf(it.Duration())))
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
		kittyPlace(e.out, e.curFirst, e.firstImgRow, e.imgCol, e.imgCols, e.imgRows)
	}
	if e.curLast != "" {
		clear(e.lastImgRow)
		kittyPlace(e.out, e.curLast, e.lastImgRow, e.imgCol, e.imgCols, e.imgRows)
	}
	io.WriteString(e.out, moveTo(e.h, e.w)) // park cursor
}

func (e *editor) put(row, col int, s string) {
	if row < 1 || row > e.h || col < 1 || col > e.w {
		return
	}
	if !strings.Contains(s, "\x1b") {
		s = trunc(s, e.w-col+1)
	}
	io.WriteString(e.out, moveTo(row, col)+s)
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
