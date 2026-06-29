package mpv

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/store"
)

const (
	hudID        = 1
	clockID      = 2
	WatchVersion = "0.3 (HUD + A-B loop + clock)"
)

// Watch opens a clip in mpv and logs from key presses while it plays:
//
//	m      add a marker at the current time
//	i / o  set IN / OUT points
//	Enter  save a select from IN..OUT
//
// A persistent on-screen HUD shows the current IN/OUT and counts, and the
// IN/OUT range is drawn on the seekbar via mpv's A-B loop (which also loops the
// trim so you can review it). Every action is echoed to the terminal too.
func Watch(p *project.Project, clip string) error {
	abs, err := p.ResolveFootage(clip)
	if err != nil {
		return err
	}
	stored := p.StoreName(clip)

	socket := filepath.Join(os.TempDir(), fmt.Sprintf("movielily-%d.sock", os.Getpid()))
	defer os.Remove(socket)

	cmd := exec.Command("mpv",
		"--input-ipc-server="+socket,
		"--force-window=yes",
		"--keep-open=yes",
		"--osd-level=1",
		"--title=movielily — "+filepath.Base(abs),
		abs,
	)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start mpv (is it installed?): %w", err)
	}

	client, err := Dial(socket, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	defer client.Close()

	mpvVer := "mpv"
	if d, err := client.Command("get_property", "mpv-version"); err == nil {
		_ = json.Unmarshal(d, &mpvVer)
	}
	fmt.Printf("movielily watch %s — connected to %s\n", WatchVersion, mpvVer)
	fmt.Println("keys: m=marker  i=IN  o=OUT  Enter=save select  q=quit")

	for key, msg := range map[string]string{"m": "ml-marker", "i": "ml-in", "o": "ml-out", "ENTER": "ml-select"} {
		if err := client.Bind(key, msg); err != nil {
			return fmt.Errorf("binding key %q: %w", key, err)
		}
	}

	var inPt, outPt float64
	var haveIn, haveOut bool
	markers, selects := 0, 0
	var selectChaps []pendingChapter
	uiWarned := false

	redraw := func() {
		err := drawUI(client, haveIn, haveOut, inPt, outPt, markers, selects, selectChaps)
		if err != nil && !uiWarned {
			uiWarned = true
			fmt.Printf("warning: mpv did not accept the on-screen UI: %v\n", err)
			fmt.Println("         (logging still works; your marks are saved to the text files)")
		}
	}
	redraw() // show the HUD immediately, before any key press

	// Live HH:MM:SS clock in the top-right corner, updated as the video plays.
	stopClock := make(chan struct{})
	defer close(stopClock)
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopClock:
				return
			case <-tick.C:
				if t, err := client.TimePos(); err == nil {
					_ = client.OSDOverlay(clockID, "{\\an9\\fs28\\bord2\\3c&H000000&\\1c&HFFFFFF&}"+clockHMS(t))
				}
			}
		}
	}()

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	for {
		select {
		case <-done:
			fmt.Printf("done — %d marker(s), %d select(s) added\n", markers, selects)
			return nil
		case ev := <-client.Events():
			if ev.Event != "client-message" || len(ev.Args) == 0 {
				continue
			}
			switch ev.Args[0] {
			case "ml-marker":
				t, err := client.TimePos()
				if err != nil {
					continue
				}
				if err := store.Append(p.Markers(), model.Marker{File: stored, Time: t}.String()); err != nil {
					return err
				}
				markers++
				fmt.Printf("  marker %d @ %ss (saved to markers.txt)\n", markers, model.FormatSeconds(t))
				redraw()
			case "ml-in":
				if t, err := client.TimePos(); err == nil {
					inPt, haveIn = t, true
					fmt.Printf("  IN  @ %ss\n", model.FormatSeconds(t))
					redraw()
				}
			case "ml-out":
				if t, err := client.TimePos(); err == nil {
					outPt, haveOut = t, true
					fmt.Printf("  OUT @ %ss\n", model.FormatSeconds(t))
					redraw()
				}
			case "ml-select":
				if !haveIn || !haveOut || outPt <= inPt {
					fmt.Println("  (set IN with i and OUT with o first)")
					client.OSD("set IN (i) and OUT (o) first")
					continue
				}
				if err := store.Append(p.Selects(), model.Select{File: stored, In: inPt, Out: outPt}.String()); err != nil {
					return err
				}
				selects++
				selectChaps = append(selectChaps,
					pendingChapter{inPt, fmt.Sprintf("⟦ sel %d", selects)},
					pendingChapter{outPt, fmt.Sprintf("sel %d ⟧", selects)},
				)
				fmt.Printf("  select %d: %ss–%ss saved\n", selects, model.FormatSeconds(inPt), model.FormatSeconds(outPt))
				haveIn, haveOut = false, false
				redraw()
			}
		}
	}
}

// drawUI updates the persistent HUD, the seekbar IN/OUT ticks (saved selects'
// boundaries plus the pending IN/OUT), and the A-B loop region for the pending
// IN/OUT. Markers are deliberately not shown on the timeline. Returns the first
// error encountered.
func drawUI(c *Client, haveIn, haveOut bool, inPt, outPt float64, markers, selects int, selectChaps []pendingChapter) error {
	if err := c.OSDOverlay(hudID, hud(haveIn, haveOut, inPt, outPt, markers, selects)); err != nil {
		return err
	}

	chs := make([]Chapter, 0, len(selectChaps)+2)
	for _, ch := range selectChaps {
		chs = append(chs, Chapter{Title: ch.title, Time: ch.time})
	}
	if haveIn {
		chs = append(chs, Chapter{Title: "▶ IN", Time: inPt})
	}
	if haveOut {
		chs = append(chs, Chapter{Title: "OUT ◀", Time: outPt})
	}
	sort.Slice(chs, func(i, j int) bool { return chs[i].Time < chs[j].Time })
	if err := c.SetChapters(chs); err != nil {
		return err
	}

	if haveIn {
		if err := c.SetProp("ab-loop-a", inPt); err != nil {
			return err
		}
	} else {
		_ = c.SetProp("ab-loop-a", "no")
	}
	if haveOut {
		if err := c.SetProp("ab-loop-b", outPt); err != nil {
			return err
		}
	} else {
		_ = c.SetProp("ab-loop-b", "no")
	}
	return nil
}

// pendingChapter is an internal chapter (time + title) before it becomes an
// mpv.Chapter.
type pendingChapter struct {
	time  float64
	title string
}

// hud builds the persistent on-screen status (ASS markup, top-left).
func hud(haveIn, haveOut bool, inPt, outPt float64, markers, selects int) string {
	in := "{\\1c&H888888&}IN  --"
	if haveIn {
		in = "{\\1c&H00FF00&}IN  " + clock(inPt)
	}
	out := "{\\1c&H888888&}OUT --"
	if haveOut {
		out = "{\\1c&H0000FF&}OUT " + clock(outPt)
	}
	return fmt.Sprintf(
		"{\\an7\\fs18\\bord2\\3c&H000000&\\1c&HFFFFFF&}movielily   m=mark  i/o=in/out  Enter=select\\N%s\\N%s\\N{\\1c&HFFFFFF&}markers %d   selects %d",
		in, out, markers, selects,
	)
}

// clock renders seconds as m:ss.s for the HUD.
func clock(t float64) string {
	if t < 0 {
		t = 0
	}
	m := int(t) / 60
	return fmt.Sprintf("%d:%04.1f", m, t-float64(m*60))
}

// clockHMS renders seconds as HH:MM:SS for the on-screen clock.
func clockHMS(t float64) string {
	if t < 0 {
		t = 0
	}
	s := int(t + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, (s%3600)/60, s%60)
}
