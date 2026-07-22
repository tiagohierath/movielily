package mpv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"movielily/internal/manim"
	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/typst"
)

// Review plays a sequence in mpv as a SIMULATION of the export, with nothing
// rendered and nothing written but a tiny playlist file: clips trimmed to
// their in/out, still images held for their duration (mpv's EDL does this
// natively), audio beds mixed underneath at their gain, and the picture
// letterboxed into the project's exact export frame. Full resolution, instant.

// edlQuote uses mpv's length-prefixed quoting so paths with spaces, commas or
// unicode survive intact: %<bytelen>%<string>.
func edlQuote(s string) string { return fmt.Sprintf("%%%d%%%s", len(s), s) }

// BuildEDL writes an mpv EDL for the timeline of a sequence (video clips AND
// still images; sections and audio beds are not timeline entries) and returns
// its path plus what it holds.
func BuildEDL(p *project.Project, name string, items []model.SequenceItem) (path string, clips, stills int, err error) {
	var b strings.Builder
	b.WriteString("# mpv EDL v0\n")
	for _, it := range items {
		if it.IsSection() || it.IsAudio() || it.IsOverlay() {
			continue
		}
		var abs string
		var e error
		switch it.Kind {
		case model.KindTitle:
			abs, e = typst.Render(p, it.File, it.Note) // cached PNG of the card
		case model.KindAnim:
			abs, e = manim.Render(p, it.File, it.Note) // cached clip of the card
		default:
			abs, e = p.ResolveFootage(it.File)
		}
		if e != nil {
			return "", 0, 0, e
		}
		if it.Kind == model.KindAnim {
			fmt.Fprintf(&b, "%s,0,%s\n", edlQuote(abs), model.FormatSeconds(it.Dur))
			clips++
			continue
		}
		if it.Kind == model.KindImage || it.Kind == model.KindTitle {
			// An image entry with an explicit length holds the frame on
			// screen for exactly that long (verified against mpv 0.3x).
			fmt.Fprintf(&b, "%s,0,%s\n", edlQuote(abs), model.FormatSeconds(it.Dur))
			stills++
		} else {
			fmt.Fprintf(&b, "%s,%s,%s\n", edlQuote(abs), model.FormatSeconds(it.In), model.FormatSeconds(it.Duration()))
			clips++
		}
	}
	if clips+stills == 0 {
		return "", 0, 0, fmt.Errorf("sequence %q has nothing to review", name)
	}
	if err := os.MkdirAll(p.SequencesDir(), 0o755); err != nil {
		return "", 0, 0, err
	}
	path = filepath.Join(p.SequencesDir(), "."+name+".review.edl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", 0, 0, err
	}
	return path, clips, stills, nil
}

// reviewArgs assembles the whole mpv invocation for Review. Split out so it
// can be exercised in tests without a terminal or a display.
//
// from is an index into items: the timeline starts at that item (0 = whole
// cut). Beds always join, trimmed by the timeline offset so the music sits
// exactly where it would in the full export.
func reviewArgs(p *project.Project, name string, items []model.SequenceItem, from int) (args []string, clips, stills, beds int, err error) {
	if from < 0 || from >= len(items) {
		from = 0
	}
	var offset float64 // timeline seconds skipped by starting at `from`
	for _, it := range items[:from] {
		offset += it.Duration()
	}
	var remaining float64
	for _, it := range items[from:] {
		remaining += it.Duration()
	}

	path, clips, stills, err := BuildEDL(p, name, items[from:])
	if err != nil {
		return nil, 0, 0, 0, err
	}

	w, h := p.Config.Width, p.Config.Height
	args = []string{
		path,
		"--force-media-title=movielily review · " + name,
		// The export's own framing: fit, letterbox, square pixels.
		fmt.Sprintf("--vf=lavfi=[scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1]", w, h, w, h),
	}

	// #mute clips are silent in the export; silence the same timeline windows
	// here so review sounds like the real thing.
	var muteWins []string
	{
		o := 0.0
		for _, it := range items[from:] {
			if it.Kind == model.KindVideo && !model.IsAudioFile(it.File) && model.HasTag(it.Note, "mute") {
				muteWins = append(muteWins, fmt.Sprintf("between(t,%s,%s)",
					model.FormatSeconds(o), model.FormatSeconds(o+it.Duration())))
			}
			o += it.Duration()
		}
	}
	muteChain := ""
	if len(muteWins) > 0 {
		muteChain = fmt.Sprintf("volume=0:enable='%s',", strings.Join(muteWins, "+"))
	}

	var bedItems []model.SequenceItem
	for _, it := range items {
		if it.IsAudio() {
			bedItems = append(bedItems, it)
		}
	}
	beds = len(bedItems)
	if beds == 0 {
		if muteChain != "" && clips > 0 {
			args = append(args, "--lavfi-complex=[aid1]"+strings.TrimSuffix(muteChain, ",")+"[ao]")
		}
		return args, clips, stills, 0, nil
	}

	// Beds join as external audio tracks and are mixed in one filter graph.
	// Track numbering: the EDL's own audio is [aid1] and externals follow, but
	// a stills-only timeline has no audio track at all, shifting the beds up.
	for _, bed := range bedItems {
		abs, e := p.ResolveFootage(bed.File)
		if e != nil {
			return nil, 0, 0, 0, e
		}
		args = append(args, "--audio-file="+abs)
	}

	// A bed's slice for this playback: skip what the timeline skipped, rebase
	// to 0, set its level.
	bedChain := func(bed model.SequenceItem) string {
		chain := ""
		if offset > 0 {
			chain = fmt.Sprintf("atrim=start=%s,asetpts=PTS-STARTPTS,", model.FormatSeconds(offset))
		}
		return chain + fmt.Sprintf("volume=%sdB", model.FormatSeconds(bed.Gain))
	}

	var g strings.Builder
	var mix []string
	if clips > 0 {
		// The timeline has sound of its own. Image slots leave holes in it,
		// so: aresample=async fills mid-timeline gaps with silence, and apad
		// pads a trailing still out to the exact timeline length; without
		// them the bed drifts out of sync after the first still (verified).
		fmt.Fprintf(&g, "[aid1]%saresample=async=1000:first_pts=0,apad=whole_dur=%s[t]", muteChain, model.FormatSeconds(remaining))
		mix = append(mix, "[t]")
		for i, bed := range bedItems {
			fmt.Fprintf(&g, ";[aid%d]%s[b%d]", 2+i, bedChain(bed), i)
			mix = append(mix, fmt.Sprintf("[b%d]", i))
		}
		fmt.Fprintf(&g, ";%samix=inputs=%d:duration=first:normalize=0[ao]", strings.Join(mix, ""), len(mix))
	} else {
		// Stills-only timeline: no [aid1], the first bed takes that slot; cap
		// playback at the timeline's end so a long song can't outlive it.
		if beds == 1 {
			fmt.Fprintf(&g, "[aid1]%s[ao]", bedChain(bedItems[0]))
		} else {
			for i, bed := range bedItems {
				fmt.Fprintf(&g, "[aid%d]%s[b%d];", 1+i, bedChain(bed), i)
				mix = append(mix, fmt.Sprintf("[b%d]", i))
			}
			fmt.Fprintf(&g, "%samix=inputs=%d:normalize=0[ao]", strings.Join(mix, ""), beds)
		}
		args = append(args, "--end="+model.FormatSeconds(remaining))
	}
	args = append(args, "--lavfi-complex="+g.String())
	return args, clips, stills, beds, nil
}

// Review plays the sequence from its start.
func Review(p *project.Project, name string, items []model.SequenceItem) error {
	return ReviewFrom(p, name, items, 0)
}

// ReviewFrom plays the sequence starting at the item at index `from`,
// simulating the export from that point on (beds included, correctly offset).
func ReviewFrom(p *project.Project, name string, items []model.SequenceItem, from int) error {
	args, clips, stills, beds, err := reviewArgs(p, name, items, from)
	if err != nil {
		return err
	}
	parts := []string{fmt.Sprintf("%d clip(s)", clips)}
	if stills > 0 {
		parts = append(parts, fmt.Sprintf("%d still(s)", stills))
	}
	if beds > 0 {
		parts = append(parts, fmt.Sprintf("%d bed(s)", beds))
	}
	overlays := 0
	for _, it := range items {
		if it.IsOverlay() {
			overlays++
		}
	}
	note := ""
	if overlays > 0 {
		note = fmt.Sprintf(" · %d overlay(s) appear in the export only", overlays)
	}
	fmt.Printf("reviewing %q: %s · simulated export, nothing rendered%s\n",
		name, strings.Join(parts, ", "), note)

	cmd := exec.Command("mpv", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// OpenDetached shows any media file in its own mpv window and returns at
// once: the TUI's "Enter opens things" for stills, cards, animations and
// beds. Images stay up until the window is closed.
func OpenDetached(path string) error {
	cmd := exec.Command("mpv",
		"--force-window=yes", "--keep-open=yes", "--image-display-duration=inf",
		"--title=movielily · "+filepath.Base(path), path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start mpv (is it installed?): %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// ReviewDetached starts the same simulated-export playback in its own mpv
// window and returns immediately, printing nothing: the TUI uses this so
// watching the cut never takes the editor over.
func ReviewDetached(p *project.Project, name string, items []model.SequenceItem, from int) error {
	args, _, _, _, err := reviewArgs(p, name, items, from)
	if err != nil {
		return err
	}
	cmd := exec.Command("mpv", args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start mpv (is it installed?): %w", err)
	}
	go func() { _ = cmd.Wait() }() // reap; the window lives its own life
	return nil
}
