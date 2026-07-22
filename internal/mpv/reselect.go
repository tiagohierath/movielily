package mpv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"movielily/internal/project"
)

// Reselect plays the whole clip in mpv, pre-seeded with the given IN/OUT, and
// lets the user pick a new trim: i sets IN, o sets OUT, Enter confirms. It
// returns the chosen IN/OUT only when confirmed (ok); quitting mpv (q) leaves
// ok false so the caller keeps the old values. Unlike Watch it writes no
// project files — the caller decides what to do with the result. This is what
// the TUI's Enter key uses to redo a scene's in/out against the full source.
func Reselect(p *project.Project, clip string, curIn, curOut float64) (in, out float64, ok bool, err error) {
	abs, err := p.ResolveFootage(clip)
	if err != nil {
		return 0, 0, false, err
	}

	socket := filepath.Join(os.TempDir(), fmt.Sprintf("movielily-%d.sock", os.Getpid()))
	defer os.Remove(socket)

	cmd := exec.Command("mpv",
		"--input-ipc-server="+socket,
		"--force-window=yes",
		"--keep-open=yes",
		"--osd-level=1",
		"--title=movielily — redo in/out — "+filepath.Base(abs),
		abs,
	)
	// No stdio: mpv runs in its own window and the OSD carries the whole UI,
	// so the caller's terminal (the TUI) stays untouched and fully live.
	if err := cmd.Start(); err != nil {
		return 0, 0, false, fmt.Errorf("could not start mpv (is it installed?): %w", err)
	}

	client, err := Dial(socket, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return 0, 0, false, err
	}
	defer client.Close()

	for key, msg := range map[string]string{"i": "ml-in", "o": "ml-out", "ENTER": "ml-select"} {
		if err := client.Bind(key, msg); err != nil {
			return 0, 0, false, fmt.Errorf("binding key %q: %w", key, err)
		}
	}

	inPt, outPt := curIn, curOut
	haveIn := true
	haveOut := curOut > curIn
	if !haveOut {
		outPt = 0
	}
	_ = drawUI(client, haveIn, haveOut, inPt, outPt, 0, 0, nil)
	if curIn > 0 {
		_ = client.SetProp("time-pos", curIn) // start near the existing IN
	}

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	for {
		select {
		case <-done:
			return 0, 0, false, nil
		case ev := <-client.Events():
			if ev.Event != "client-message" || len(ev.Args) == 0 {
				continue
			}
			switch ev.Args[0] {
			case "ml-in":
				if t, e := client.TimePos(); e == nil {
					inPt, haveIn = t, true
					_ = drawUI(client, haveIn, haveOut, inPt, outPt, 0, 0, nil)
				}
			case "ml-out":
				if t, e := client.TimePos(); e == nil {
					outPt, haveOut = t, true
					_ = drawUI(client, haveIn, haveOut, inPt, outPt, 0, 0, nil)
				}
			case "ml-select":
				if !haveIn || !haveOut || outPt <= inPt {
					client.OSD("set IN (i) and OUT (o) first")
					continue
				}
				_, _ = client.Command("quit")
				<-done
				return inPt, outPt, true, nil
			}
		}
	}
}
