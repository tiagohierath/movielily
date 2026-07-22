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
// with git itself; this tab is for SEEING the versions and jumping between
// them. It is a proper two-pane browser: a selectable commit list with the
// branch graph on the left, and what that snapshot changed on the right.
type snapCommit struct {
	graph string // the │ ├ * columns git draws for the branch topology
	hash  string
	date  string
	refs  string // (HEAD -> main, tag: …) decorations
	msg   string
}

func (e *editor) openSnapshots() {
	e.snaps = nil
	e.snapSel = 0
	e.snapTop = 0
	e.snapBranch = ""
	if _, err := os.Stat(filepath.Join(e.p.Root, ".git")); err == nil {
		e.snapBranch = gitOut(e.p.Root, "rev-parse", "--abbrev-ref", "HEAD")
		// A rare US field separator keeps messages with spaces intact while the
		// graph prefix (which git draws before %H) stays outside the fields.
		raw := gitOut(e.p.Root, "log", "--graph", "--all", "--decorate",
			"--date=format:%Y-%m-%d %H:%M", "--pretty=format:\x1f%h\x1f%ad\x1f%d\x1f%s", "-n", "500")
		for _, line := range strings.Split(raw, "\n") {
			g, rest, ok := strings.Cut(line, "\x1f")
			if !ok {
				// A graph-only connector line (│ / \) with no commit; attach it
				// to the previous commit's graph so topology still reads.
				if n := len(e.snaps); n > 0 {
					e.snaps[n-1].graph += "\n" + line
				}
				continue
			}
			f := strings.SplitN(rest, "\x1f", 4)
			for len(f) < 4 {
				f = append(f, "")
			}
			e.snaps = append(e.snaps, snapCommit{graph: strings.TrimRight(g, " "),
				hash: f[0], date: f[1], refs: strings.TrimSpace(f[2]), msg: f[3]})
		}
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
	if len(chunk) >= 3 && chunk[0] == 0x1b && chunk[1] == '[' { // arrows
		switch chunk[2] {
		case 'A':
			e.snapMove(-1)
		case 'B':
			e.snapMove(1)
		}
		e.drawSnapshots()
		e.out.Flush()
		return
	}
	for _, b := range chunk {
		switch b {
		case 'j':
			e.snapMove(1)
		case 'k':
			e.snapMove(-1)
		case 'g':
			e.snapSel, e.snapTop = 0, 0
		case 'G':
			e.snapSel = len(e.snaps) - 1
		case '\r', '\n':
			e.snapRestore()
			return
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

func (e *editor) snapMove(d int) {
	e.snapSel += d
	if e.snapSel < 0 {
		e.snapSel = 0
	}
	if e.snapSel >= len(e.snaps) {
		e.snapSel = len(e.snaps) - 1
	}
}

// snapRestore checks the selected snapshot's files out over the working tree,
// after snapshotting the current state so it is itself reversible. It never
// touches footage (footage/ is gitignored).
func (e *editor) snapRestore() {
	if e.snapSel < 0 || e.snapSel >= len(e.snaps) {
		return
	}
	ref := e.snaps[e.snapSel].hash
	if e.dirty {
		_ = e.save() // don't lose unsaved edits before git rewrites the files
	}
	gitOut(e.p.Root, "stash", "push", "-u", "-m", "movielily: before restoring "+ref)
	if out := gitOut(e.p.Root, "checkout", ref, "--", "."); true {
		_ = out
	}
	// Reload the (possibly changed) sequence from disk.
	if items, err := store.LoadSequence(e.path); err == nil {
		e.items = items
		e.marked = map[int]bool{}
		e.undo, e.redoStack = nil, nil
		e.dirty = true // the restore is an unsaved change until w
		e.clampCursor()
	}
	e.screen = 0
	e.status = "restored snapshot " + ref + " · w to keep · u… no, use Tab→snapshots to jump back"
	e.redraw(true)
	e.onSceneChange()
	e.out.Flush()
}

func (e *editor) drawSnapshots() {
	io.WriteString(e.out, clearScreen)
	if e.kitty {
		kittyDeleteAll(e.out)
	}
	title := " snapshots"
	if e.snapBranch != "" {
		title += " · on " + e.snapBranch
	}
	title += " · every commit is a version of the movie"
	io.WriteString(e.out, moveTo(1, 1)+"\x1b[7m"+padRight(trunc(title, e.w), e.w)+"\x1b[0m")

	if len(e.snaps) == 0 {
		e.put(3, 2, "\x1b[2mno snapshots yet.\x1b[0m")
		e.put(5, 2, "take one from the shell:  movielily snapshot \"first cut\"")
		e.put(6, 2, "exports auto-snapshot once a repo exists.")
		foot := " Tab/q back"
		io.WriteString(e.out, moveTo(e.h, 1)+"\x1b[7m"+padRight(trunc(foot, e.w), e.w)+"\x1b[0m")
		return
	}

	rows := e.h - 2
	// Keep the selection in view.
	if e.snapSel < e.snapTop {
		e.snapTop = e.snapSel
	}
	if e.snapSel >= e.snapTop+rows {
		e.snapTop = e.snapSel - rows + 1
	}
	listW := e.w * 60 / 100
	if listW < 30 {
		listW = e.w
	}
	for i := 0; i < rows; i++ {
		idx := e.snapTop + i
		if idx >= len(e.snaps) {
			io.WriteString(e.out, moveTo(2+i, 1)+strings.Repeat(" ", listW))
			continue
		}
		c := e.snaps[idx]
		gLine := c.graph
		if j := strings.IndexByte(gLine, '\n'); j >= 0 {
			gLine = gLine[:j] // first graph row carries the commit
		}
		row := fmt.Sprintf("%s %s %s %s", gLine, c.hash, c.date, c.msg)
		if idx == e.snapSel {
			io.WriteString(e.out, moveTo(2+i, 1)+"\x1b[7m"+visPad(trunc(row, listW), listW)+"\x1b[0m")
		} else {
			// hash cyan, refs (if any) yellow, rest default.
			line := fmt.Sprintf("\x1b[2m%s\x1b[0m \x1b[36m%s\x1b[0m \x1b[2m%s\x1b[0m %s",
				gLine, c.hash, c.date, trunc(c.msg, listW-len(gLine)-len(c.hash)-len(c.date)-4))
			io.WriteString(e.out, moveTo(2+i, 1)+visPad(line, listW))
		}
	}

	// Right pane: the selected snapshot's refs + what it changed.
	if listW < e.w {
		col := listW + 2
		for r := 2; r <= e.h-1; r++ {
			e.put(r, listW+1, "\x1b[90m│\x1b[0m")
		}
		c := e.snaps[e.snapSel]
		e.put(2, col, "\x1b[1m"+trunc(c.hash+"  "+c.date, e.w-col)+"\x1b[0m")
		r := 3
		if c.refs != "" {
			e.put(r, col, "\x1b[33m"+trunc(c.refs, e.w-col)+"\x1b[0m")
			r++
		}
		e.put(r+1, col, trunc(c.msg, e.w-col))
		stat := gitOut(e.p.Root, "show", "--stat", "--oneline", "--format=", c.hash)
		sr := r + 3
		for _, l := range strings.Split(stat, "\n") {
			if sr > e.h-1 || l == "" {
				break
			}
			e.put(sr, col, "\x1b[2m"+trunc(strings.TrimSpace(l), e.w-col)+"\x1b[0m")
			sr++
		}
	}

	foot := " j/k select · ⏎ restore this version · Tab/q back · branch & merge with plain git"
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
