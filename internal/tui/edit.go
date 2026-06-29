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
	"movielily/internal/model"
	"movielily/internal/mpv"
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
	inputBytes []byte // text being edited (note, title, or duration)
	editDur    bool   // the inline edit targets a still's duration, not its note
	status     string

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
			if e.wantReselect {
				e.wantReselect = false
				e.reselect(st)
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
			case 'e':
				e.startEdit()
			case 't':
				e.startDurEdit()
			case '\r', '\n':
				e.wantReselect = true
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
			e.editDur = false
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
	e.editDur = false
	e.inputBytes = []byte(e.items[e.cursor].Note)
}

// startDurEdit edits a still image's on-screen duration. Clips set their length
// via their in/out (⏎ → mpv), and sections have none — so this is image-only.
func (e *editor) startDurEdit() {
	if len(e.items) == 0 {
		return
	}
	switch e.items[e.cursor].Kind {
	case model.KindImage:
		e.mode = modeEdit
		e.editDur = true
		e.inputBytes = []byte(trimf(e.items[e.cursor].Dur))
	case model.KindSection:
		e.status = "sections have no duration"
	default:
		e.status = "use ⏎ to set a clip's in/out"
	}
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
	if e.editDur {
		e.commitDur()
		return
	}
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

// commitDur parses the inline input as a duration and applies it to the still.
// A bad value is rejected with a message rather than silently zeroing it.
func (e *editor) commitDur() {
	secs, err := model.ParseSeconds(string(e.inputBytes))
	e.mode = modeNormal
	e.editDur = false
	e.inputBytes = nil
	if err != nil || secs <= 0 {
		e.status = "duration unchanged (want a positive number of seconds)"
		return
	}
	e.pushUndo()
	e.items[e.cursor].Dur = secs
	e.dirty = true
	e.forceScene = true
	e.status = fmt.Sprintf("duration → %ss — w to save", trimf(secs))
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

// reselect plays the current scene's full source clip in mpv so its in/out can
// be redone (Enter in the list). Sections and stills have no trim to redo.
func (e *editor) reselect(st *xterm.State) {
	if len(e.items) == 0 {
		return
	}
	it := e.items[e.cursor]
	switch {
	case it.IsSection():
		e.status = "sections have no footage to play"
		e.drawAll()
		e.out.Flush()
		return
	case it.Kind == model.KindImage:
		e.status = "stills have no in/out — press t to set the duration"
		e.drawAll()
		e.out.Flush()
		return
	}

	e.suspend(st)
	in, out, ok, err := mpv.Reselect(e.p, it.File, it.In, it.Out)
	e.resume(st)

	switch {
	case err != nil:
		e.status = "mpv: " + err.Error()
	case ok:
		e.pushUndo()
		e.items[e.cursor].In = in
		e.items[e.cursor].Out = out
		e.dirty = true
		e.status = fmt.Sprintf("in/out → %s–%s — w to save", mmss(in), mmss(out))
	default:
		e.status = "in/out unchanged"
	}

	e.redraw(true)
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
		switch {
		case e.editDur:
			label = "duration (s)"
		case len(e.items) > 0 && e.items[e.cursor].IsSection():
			label = "title"
		}
		s = " " + label + " ▸ " + string(e.inputBytes) + "▏"
	case e.status != "":
		s = " " + e.status
	default:
		s = " j/k move · J/K reorder · ⏎ redo in/out · e note · t still-dur · o section · v vim · space mark · d del · u undo · w save · q quit"
	}
	io.WriteString(e.out, moveTo(e.h, 1)+"\x1b[7m"+padRight(trunc(s, e.w), e.w)+"\x1b[0m")
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
// are numbered, how wide each column is, and whether the optional pacing bar
// fits. Computed once per draw so all rows stay on one grid.
type listMetrics struct {
	hasSections bool
	sceneNo     []int // item index -> 1-based scene number (0 for sections)
	numW        int
	fileW       int
	indent      int
	maxDur      float64
	showBar     bool
	barW        int
}

func (e *editor) metrics() listMetrics {
	m := listMetrics{sceneNo: make([]int, len(e.items)), barW: 6}
	n, maxNo := 0, 0
	for i, it := range e.items {
		if it.IsSection() {
			m.hasSections = true
			continue
		}
		n++
		m.sceneNo[i], maxNo = n, n
		if d := it.Duration(); d > m.maxDur {
			m.maxDur = d
		}
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
	// Columns up to and including the duration; the bar+note share what's left.
	fixed := m.indent + 2 + m.numW + 1 + m.fileW + 2 + 5 + 1
	m.showBar = m.maxDur > 0 && e.leftW-fixed >= m.barW+2
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
		icon := "▶ "
		if it.Kind == model.KindImage {
			icon = "▦ "
		}
		sp = append(sp, span{text: icon, style: "2"})
	}
	sp = append(sp, span{text: padLeft(strconv.Itoa(m.sceneNo[idx]), m.numW) + " ", style: "2"})
	sp = append(sp, span{text: padRight(it.File, m.fileW) + "  "})
	sp = append(sp, span{text: padLeft(durLabel(it), 5) + " ", style: "2"})
	if m.showBar {
		filled := barFill(it.Duration(), m.maxDur, m.barW)
		sp = append(sp, span{text: strings.Repeat("▰", filled), style: "36"})
		sp = append(sp, span{text: strings.Repeat("▱", m.barW-filled) + " ", style: "2"})
	}
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
	return mmss(it.Duration())
}

// barFill is how many of w cells a clip of length d fills relative to the
// longest clip — a clip with any length never rounds away to nothing.
func barFill(d, max float64, w int) int {
	if max <= 0 {
		return 0
	}
	n := int(math.Round(d / max * float64(w)))
	if n < 1 && d > 0 {
		n = 1
	}
	if n > w {
		n = w
	}
	return n
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
