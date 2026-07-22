package tui

import (
	"fmt"
	"io"
	"movielily/internal/manim"
	"movielily/internal/model"
	"movielily/internal/mpv"
	"movielily/internal/store"
	"movielily/internal/typst"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	xterm "golang.org/x/term"
)

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
