package tui

import (
	"fmt"
	"io"
	"movielily/internal/mpv"
	"movielily/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
