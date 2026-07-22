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
	"errors"
	"fmt"
	_ "image/png" // register the PNG decoder for image.DecodeConfig
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	xterm "golang.org/x/term"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
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
	editGrade         // colour grade / grain tokens for the current scene
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

	// snapshots tab (Tab): interactive commit browser
	screen     int // 0 = editor · 1 = snapshots · 2 = grade panel
	snaps      []snapCommit
	snapSel    int
	snapTop    int
	snapBranch string

	// grade panel (c on a scene, or :grade): a live slider view over the
	// scene's colour-grade parameters. gradeIdx is the selected parameter.
	gradeIdx int

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
	wantYoutube  bool

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
			if e.wantYoutube {
				e.wantYoutube = false
				e.youtubeOp(st)
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
