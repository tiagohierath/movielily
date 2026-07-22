package tui

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"image"
	"io"
	"math"
	"movielily/internal/manim"
	"movielily/internal/model"
	"os"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"movielily/internal/grade"
)

// ---- rendering ------------------------------------------------------------

// drawGrade paints the colour-grade panel: one row per parameter with a
// slider showing its position between min and max, plus the live key=value
// text (the exact tokens stored in the note) so the panel and the text form
// are visibly the same thing.
func (e *editor) drawGrade() {
	io.WriteString(e.out, clearScreen)
	if e.kitty {
		kittyDeleteAll(e.out)
	}
	it := e.items[e.cursor]
	g := e.currentGrade()
	title := fmt.Sprintf(" grade · %s%s", it.File, tern(g.IsNeutral(), "  (neutral)", ""))
	io.WriteString(e.out, moveTo(1, 1)+"\x1b[7m"+padRight(trunc(title, e.w), e.w)+"\x1b[0m")

	specs := grade.Specs()
	barW := 24
	for i, s := range specs {
		row := 3 + i*2
		v := g.Get(s.Name)
		// slider position of v within [min,max]
		frac := (v - s.Min) / (s.Max - s.Min)
		filled := int(frac*float64(barW) + 0.5)
		if filled < 0 {
			filled = 0
		}
		if filled > barW {
			filled = barW
		}
		bar := strings.Repeat("─", filled) + "●" + strings.Repeat("─", barW-filled)
		name := fmt.Sprintf("%-11s", s.Name)
		val := fmt.Sprintf("%6s", num2(v))
		neutralMark := ""
		if v == s.Neutral {
			neutralMark = " \x1b[2m(neutral)\x1b[0m"
		}
		if i == e.gradeIdx {
			e.put(row, 2, "\x1b[7m▸ "+name+"\x1b[0m \x1b[36m"+bar+"\x1b[0m "+val+neutralMark)
			e.put(row+1, 4, "\x1b[2m"+trunc(s.Help, e.w-6)+"\x1b[0m")
		} else {
			e.put(row, 2, "  \x1b[1m"+name+"\x1b[0m \x1b[2m"+bar+"\x1b[0m "+val+neutralMark)
		}
	}

	// The live text form, exactly what lands in the note.
	txt := g.String()
	if txt == "" {
		txt = "(none)"
	}
	e.put(e.h-2, 2, "\x1b[2mtext:\x1b[0m "+trunc(txt, e.w-8))
	foot := " j/k pick · h/l or ←/→ adjust · 0 reset param · r clear all · Tab/q back (w saves)"
	io.WriteString(e.out, moveTo(e.h, 1)+"\x1b[7m"+padRight(trunc(foot, e.w), e.w)+"\x1b[0m")
}

func tern(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func num2(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

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
