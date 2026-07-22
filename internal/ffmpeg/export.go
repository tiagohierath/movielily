// Package ffmpeg renders a sequence to a single file. It builds one ffmpeg
// filter_complex that trims each clip, fits every item to the project's 4:3
// frame, and concatenates them — no intermediate files.
package ffmpeg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"movielily/internal/manim"
	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/typst"
)

// Export renders items to out using ffmpeg. Video clips are assumed to carry
// audio (v1 scope: MP4/H264); images get matching silence.
//
// The invariant: source footage + instructions = export. Footage is only ever
// read, so Export refuses to write its output over any source file.
func Export(p *project.Project, items []model.SequenceItem, out string) error {
	// Section headers are organisational only, audio beds run under the
	// timeline, and overlays ride on top of it; none of them may enter the
	// index-coupled concat graph below. Beds are mixed and overlays composited
	// after the concat. An overlay binds to the playable item directly above
	// it, so its on-screen window is resolved here while walking in order.
	type boundOverlay struct {
		it         model.SequenceItem
		start, end float64 // absolute timeline window
	}
	playable := items[:0:0]
	var beds []model.SequenceItem
	var overlays []boundOverlay
	offset, lastStart, lastDur := 0.0, 0.0, 0.0
	havePlayable := false
	for _, it := range items {
		switch {
		case it.IsSection():
		case it.IsAudio():
			beds = append(beds, it)
		case it.IsOverlay():
			if !havePlayable {
				return fmt.Errorf("overlay %q has no scene above it to ride on", it.File)
			}
			s := lastStart + it.In
			e := s + it.Dur
			if it.Dur <= 0 || e > lastStart+lastDur {
				e = lastStart + lastDur // to the end of its scene
			}
			if s >= e {
				fmt.Fprintf(os.Stderr, "warning: overlay %q starts after its scene ends, skipping\n", it.File)
				continue
			}
			overlays = append(overlays, boundOverlay{it: it, start: s, end: e})
		default:
			lastStart, lastDur, havePlayable = offset, it.Duration(), true
			offset += it.Duration()
			playable = append(playable, it)
		}
	}
	items = playable
	if len(items) == 0 {
		return fmt.Errorf("nothing to export: sequence is empty")
	}
	w, h, fps, crf := p.Config.Width, p.Config.Height, p.Config.FPS, p.Config.CRF

	outAbs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if footAbs, err := filepath.Abs(p.Footage()); err == nil {
		if rel, err := filepath.Rel(footAbs, outAbs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("refusing to export into footage/ — movielily never modifies source footage")
		}
	}

	args := []string{"-y"}
	var fc strings.Builder
	var vlabels, alabels []string
	input := 0

	for i, it := range items {
		var abs string
		var err error
		switch it.Kind {
		case model.KindTitle:
			// A title card is a generated still: render (or reuse) its PNG
			// and treat it exactly like an image from here on.
			abs, err = typst.Render(p, it.File, it.Note)
		case model.KindAnim:
			abs, err = manim.Render(p, it.File, it.Note)
		default:
			abs, err = p.ResolveFootage(it.File)
		}
		if err != nil {
			return err
		}
		if abs == outAbs {
			return fmt.Errorf("refusing to overwrite source footage %q with the export", it.File)
		}
		vlab, alab := fmt.Sprintf("v%d", i), fmt.Sprintf("a%d", i)

		switch {
		case it.Kind == model.KindImage || it.Kind == model.KindTitle:
			if it.Dur <= 0 {
				return fmt.Errorf("image %q has non-positive duration", it.File)
			}
			dur := model.FormatSeconds(it.Dur)
			args = append(args, "-loop", "1", "-t", dur, "-i", abs)
			vIdx := input
			input++
			args = append(args, "-f", "lavfi", "-t", dur, "-i", "anullsrc=r=48000:cl=stereo")
			aIdx := input
			input++
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", vIdx, vchainFor(it, w, h, fps), vlab)
			fmt.Fprintf(&fc, "[%d:a]%s[%s];", aIdx, achain(), alab)
		case it.Kind == model.KindAnim:
			// Rendered animation: silent by design (beds carry the music), so
			// pair its picture with generated silence, trimmed to the length
			// measured when the card was added.
			dur := model.FormatSeconds(it.Dur)
			args = append(args, "-t", dur, "-i", abs)
			vIdx := input
			input++
			args = append(args, "-f", "lavfi", "-t", dur, "-i", "anullsrc=r=48000:cl=stereo")
			aIdx := input
			input++
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", vIdx, vchainFor(it, w, h, fps), vlab)
			fmt.Fprintf(&fc, "[%d:a]%s[%s];", aIdx, achain(), alab)
		case model.IsAudioFile(it.File):
			// A voice/narration segment: the sound occupies the timeline and
			// the picture is a black canvas (decorate it with overlays/cards).
			if it.Out <= it.In {
				return fmt.Errorf("clip %q has out (%s) <= in (%s)", it.File, model.FormatSeconds(it.Out), model.FormatSeconds(it.In))
			}
			dur := model.FormatSeconds(it.Duration())
			args = append(args, "-ss", model.FormatSeconds(it.In), "-t", dur, "-i", abs)
			aIdx := input
			input++
			args = append(args, "-f", "lavfi", "-t", dur, "-i",
				fmt.Sprintf("color=black:s=%dx%d:r=%d", w, h, fps))
			vIdx := input
			input++
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", vIdx, vchainFor(it, w, h, fps), vlab)
			fmt.Fprintf(&fc, "[%d:a]%s[%s];", aIdx, achain(), alab)
		default: // video
			if it.Out <= it.In {
				return fmt.Errorf("clip %q has out (%s) <= in (%s)", it.File, model.FormatSeconds(it.Out), model.FormatSeconds(it.In))
			}
			args = append(args, "-ss", model.FormatSeconds(it.In), "-t", model.FormatSeconds(it.Duration()), "-i", abs)
			idx := input
			input++
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", idx, vchainFor(it, w, h, fps), vlab)
			fmt.Fprintf(&fc, "[%d:a]%s[%s];", idx, achain(), alab)
		}
		vlabels = append(vlabels, "["+vlab+"]")
		alabels = append(alabels, "["+alab+"]")
	}

	for i := range items {
		fc.WriteString(vlabels[i])
		fc.WriteString(alabels[i])
	}
	fmt.Fprintf(&fc, "concat=n=%d:v=1:a=1[outv][outa]", len(items))

	// Overlays: each is an extra looped image input composited onto the
	// concatenated picture only inside its scene's window (enable=between).
	// PNG transparency is respected; the chain ends back at yuv420p.
	videoOut := "[outv]"
	if len(overlays) > 0 {
		cur := "[outv]"
		for i, ov := range overlays {
			abs, err := p.ResolveFootage(ov.it.File)
			if err != nil {
				return err
			}
			args = append(args, "-loop", "1", "-i", abs)
			corner, pct, err := model.ParsePlace(ov.it.Place)
			if err != nil {
				return err
			}
			scale, pos := overlayScalePos(corner, pct, w, h)
			fmt.Fprintf(&fc, ";[%d:v]%s[o%d];%s[o%d]overlay=%s:enable='between(t,%s,%s)'[vo%d]",
				input, scale, i, cur, i, pos,
				model.FormatSeconds(ov.start), model.FormatSeconds(ov.end), i)
			input++
			cur = fmt.Sprintf("[vo%d]", i)
		}
		fmt.Fprintf(&fc, ";%sformat=yuv420p[vfin]", cur)
		videoOut = "[vfin]"
	}

	// Audio beds: each starts at 0 and is mixed UNDER the timeline's own sound
	// at its gain. duration=first ties the mix to the concat audio, so a long
	// song is simply cut when the video ends; normalize=0 keeps the clips'
	// sound at full level instead of averaging it down per input.
	audioOut := "[outa]"
	if len(beds) > 0 {
		mix := "[outa]"
		for i, bed := range beds {
			abs, err := p.ResolveFootage(bed.File)
			if err != nil {
				return err
			}
			if abs == outAbs {
				return fmt.Errorf("refusing to overwrite source footage %q with the export", bed.File)
			}
			args = append(args, "-i", abs)
			lab := fmt.Sprintf("bed%d", i)
			fmt.Fprintf(&fc, ";[%d:a]%s,volume=%sdB[%s]", input, achain(), model.FormatSeconds(bed.Gain), lab)
			input++
			mix += "[" + lab + "]"
		}
		fmt.Fprintf(&fc, ";%samix=inputs=%d:duration=first:normalize=0[mixa]", mix, len(beds)+1)
		audioOut = "[mixa]"
	}

	// Delivery settings tuned for YouTube's upload recommendations: H.264
	// High profile, constant frame rate, a keyframe every 2 seconds, 2
	// B-frames, BT.709 flagged, AAC-LC 320k at 48kHz, faststart for streaming.
	args = append(args,
		"-filter_complex", fc.String(),
		"-map", videoOut, "-map", audioOut,
		"-c:v", "libx264", "-preset", "medium", "-crf", strconv.Itoa(crf),
		"-profile:v", "high", "-level", "4.2",
		"-pix_fmt", "yuv420p", "-r", strconv.Itoa(fps),
		"-g", strconv.Itoa(fps*2), "-bf", "2",
		"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709",
		"-c:a", "aac", "-b:a", "320k", "-ar", "48000",
		"-movflags", "+faststart",
		out,
	)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// vchain scales to fit, pads to the 4:3 frame, squares pixels, fixes fps and
// pixel format so every segment is concat-compatible.
func vchain(w, h, fps int) string {
	return fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=%d,format=yuv420p",
		w, h, w, h, fps,
	)
}

// vchainFor picks the item's fit. The default letterboxes (the whole picture,
// bars if shapes differ); a #cover tag in the note fills the frame instead,
// cropping the edges that don't fit. Same grammar as every other tag.
func vchainFor(it model.SequenceItem, w, h, fps int) string {
	for _, t := range model.Tags(it.Note) {
		if t == "cover" {
			return fmt.Sprintf(
				"scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,setsar=1,fps=%d,format=yuv420p",
				w, h, w, h, fps,
			)
		}
	}
	return vchain(w, h, fps)
}

// overlayScalePos sizes an overlay to pct%% of the frame width (aspect kept)
// and anchors it: corners inset by a margin, c centered, full fitted whole.
func overlayScalePos(corner string, pct, w, h int) (scale, pos string) {
	const margin = 24
	if corner == "full" {
		return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", w, h),
			"x=(main_w-overlay_w)/2:y=(main_h-overlay_h)/2"
	}
	ow := (w * pct / 100) &^ 1 // even width keeps yuv subsampling happy
	scale = fmt.Sprintf("scale=%d:-2", ow)
	switch corner {
	case "tl":
		pos = fmt.Sprintf("x=%d:y=%d", margin, margin)
	case "tr":
		pos = fmt.Sprintf("x=main_w-overlay_w-%d:y=%d", margin, margin)
	case "bl":
		pos = fmt.Sprintf("x=%d:y=main_h-overlay_h-%d", margin, margin)
	case "c":
		pos = "x=(main_w-overlay_w)/2:y=(main_h-overlay_h)/2"
	default: // br
		pos = fmt.Sprintf("x=main_w-overlay_w-%d:y=main_h-overlay_h-%d", margin, margin)
	}
	return scale, pos
}

// achain normalises audio so every segment is concat-compatible.
func achain() string {
	return "aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo"
}
