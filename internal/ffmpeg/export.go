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

	"movielily/internal/grade"
	"movielily/internal/manim"
	"movielily/internal/model"
	"movielily/internal/project"
	"movielily/internal/timeline"
	"movielily/internal/typst"
)

// Export renders items to out using ffmpeg. Video clips are assumed to carry
// audio (v1 scope: MP4/H264); images get matching silence, and a #mute tag on
// a clip swaps its sound for silence (b-roll over a narration bed).
//
// Finishing touches applied automatically: ~15ms audio micro-fades at every
// join (no clicks), a fade from/to black on the whole picture, a music-bed
// fade-out at the end, optional #duck sidechain ducking of beds under the
// timeline's sound, and a final loudnorm to YouTube's -14 LUFS.
//
// draft renders at half resolution with fast settings, for a quick full look.
//
// The invariant: source footage + instructions = export. Footage is only ever
// read, so Export refuses to write its output over any source file.
func Export(p *project.Project, items []model.SequenceItem, out string, draft bool) error {
	// Resolve the sequence (use-splices, overlay windows, offsets) once; the
	// timeline package owns those semantics so export and review can't drift.
	plan, warnings, err := timeline.Resolve(p.SequencesDir(), items)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning: "+w)
	}
	beds := plan.Beds
	overlays := plan.Overlays
	total := plan.Total // full runtime, for end fades and duck/bed windows
	if len(plan.Scenes) == 0 {
		return fmt.Errorf("nothing to export: sequence is empty")
	}
	sceneItems := make([]model.SequenceItem, len(plan.Scenes))
	for i, s := range plan.Scenes {
		sceneItems[i] = s.Item
	}
	items = sceneItems
	w, h, fps, crf := p.Config.Width, p.Config.Height, p.Config.FPS, p.Config.CRF
	if draft {
		w, h, crf = (w/2)&^1, (h/2)&^1, 28
	}

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
		// Colour grade / film grain for this item, from its note (inline
		// params + optional #grade:preset). Pure text, applied only here.
		gr, err := grade.For(p.GradesDir(), it.Note)
		if err != nil {
			return err
		}
		gradeF := gr.Filter()
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
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", vIdx, vchainFor(it, w, h, fps, gradeF), vlab)
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
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", vIdx, vchainFor(it, w, h, fps, gradeF), vlab)
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
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", vIdx, vchainFor(it, w, h, fps, gradeF), vlab)
			fmt.Fprintf(&fc, "[%d:a]%s[%s];", aIdx, achainClip(it), alab)
		default: // video
			if it.Out <= it.In {
				return fmt.Errorf("clip %q has out (%s) <= in (%s)", it.File, model.FormatSeconds(it.Out), model.FormatSeconds(it.In))
			}
			dur := model.FormatSeconds(it.Duration())
			args = append(args, "-ss", model.FormatSeconds(it.In), "-t", dur, "-i", abs)
			idx := input
			input++
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", idx, vchainFor(it, w, h, fps, gradeF), vlab)
			if model.HasTag(it.Note, "mute") || !HasAudio(abs) {
				// #mute b-roll, or footage that simply has no audio stream:
				// pair the picture with silence instead of a missing [i:a].
				args = append(args, "-f", "lavfi", "-t", dur, "-i", "anullsrc=r=48000:cl=stereo")
				aIdx := input
				input++
				fmt.Fprintf(&fc, "[%d:a]%s[%s];", aIdx, achain(), alab)
			} else {
				fmt.Fprintf(&fc, "[%d:a]%s[%s];", idx, achainClip(it), alab)
			}
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
			abs, err := ResolveOverlay(p, ov.Item)
			if err != nil {
				return err
			}
			// The -t bound matters: an unbounded -loop 1 input never ends and
			// can keep ffmpeg from ever finishing the encode. The looped image
			// only needs to exist until its window closes; overlay's default
			// repeatlast covers the rest of the film.
			args = append(args, "-loop", "1", "-t", model.FormatSeconds(ov.End), "-i", abs)
			corner, pct, err := model.ParsePlace(ov.Item.Place)
			if err != nil {
				return err
			}
			scale, pos := overlayScalePos(corner, pct, w, h)
			fmt.Fprintf(&fc, ";[%d:v]%s[o%d];%s[o%d]overlay=%s:enable='between(t,%s,%s)'[vo%d]",
				input, scale, i, cur, i, pos,
				model.FormatSeconds(ov.Start), model.FormatSeconds(ov.End), i)
			input++
			cur = fmt.Sprintf("[vo%d]", i)
		}
		fmt.Fprintf(&fc, ";%sformat=yuv420p[vfin]", cur)
		videoOut = "[vfin]"
	}

	// Audio beds: each starts at 0 and is mixed UNDER the timeline's own sound
	// at its gain, with a fade-out as the film ends so the music never hard
	// cuts. A #duck bed is additionally sidechain-compressed by the timeline's
	// sound, dipping automatically whenever someone talks. duration=first ties
	// the mix to the concat audio (a long song is simply cut); normalize=0
	// keeps the timeline at full level instead of averaging it down.
	audioOut := "[outa]"
	if len(beds) > 0 {
		var plain, ducked []string
		for i, bed := range beds {
			abs, err := p.ResolveFootage(bed.File)
			if err != nil {
				return err
			}
			if abs == outAbs {
				return fmt.Errorf("refusing to overwrite source footage %q with the export", bed.File)
			}
			args = append(args, "-i", abs)
			lab := fmt.Sprintf("[bed%d]", i)
			fmt.Fprintf(&fc, ";[%d:a]%s%s", input, bedChain(bed, total), lab)
			input++
			if model.HasTag(bed.Note, "duck") {
				ducked = append(ducked, lab)
			} else {
				plain = append(plain, lab)
			}
		}
		timeline := "[outa]"
		if len(ducked) > 0 {
			// One copy of the timeline keys the compressor, the other stays
			// in the mix untouched.
			fc.WriteString(";[outa]asplit=2[tl][key]")
			timeline = "[tl]"
			sub := ducked[0]
			if len(ducked) > 1 {
				fmt.Fprintf(&fc, ";%samix=inputs=%d:normalize=0[bsub]", strings.Join(ducked, ""), len(ducked))
				sub = "[bsub]"
			}
			fmt.Fprintf(&fc, ";%s[key]sidechaincompress=threshold=0.03:ratio=8:attack=20:release=400[bduck]", sub)
			plain = append(plain, "[bduck]")
		}
		fmt.Fprintf(&fc, ";%s%samix=inputs=%d:duration=first:normalize=0[mixa]",
			timeline, strings.Join(plain, ""), len(plain)+1)
		audioOut = "[mixa]"
	}

	// Finishing: fade the whole picture from and to black, and normalise the
	// final mix to YouTube's -14 LUFS (loudnorm resamples internally, so pin
	// the rate back to 48k for the AAC encode).
	if total > 2 {
		fmt.Fprintf(&fc, ";%sfade=t=in:st=0:d=0.3,fade=t=out:st=%s:d=0.8[vfade]",
			videoOut, model.FormatSeconds(total-0.8))
		videoOut = "[vfade]"
	}
	fmt.Fprintf(&fc, ";%sloudnorm=I=-14:TP=-1.5:LRA=11,aresample=48000[afin]", audioOut)
	audioOut = "[afin]"

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

// vchain scales to fit, pads to the 4:3 frame, squares pixels and fixes fps.
// The colour-grade filter (may be empty) runs after the geometry, and the
// pixel format is pinned last so every segment stays concat-compatible.
func vchain(w, h, fps int, gradeF string) string {
	base := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=%d",
		w, h, w, h, fps,
	)
	return withGrade(base, gradeF)
}

// vchainFor picks the item's fit. The default letterboxes (the whole picture,
// bars if shapes differ); a #cover tag in the note fills the frame instead,
// cropping the edges that don't fit. gradeF is the item's colour grade / grain
// (empty when neutral). Same grammar as every other tag.
func vchainFor(it model.SequenceItem, w, h, fps int, gradeF string) string {
	if model.HasTag(it.Note, "cover") {
		base := fmt.Sprintf(
			"scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,setsar=1,fps=%d",
			w, h, w, h, fps,
		)
		return withGrade(base, gradeF)
	}
	return vchain(w, h, fps, gradeF)
}

// withGrade appends the grade filter (if any) after the geometry chain, then
// pins the pixel format.
func withGrade(base, gradeF string) string {
	if gradeF != "" {
		base += "," + gradeF
	}
	return base + ",format=yuv420p"
}

// ResolveOverlay turns an overlay's file into a real image: a plain image
// from footage/, or a typst template (titles/*.typ) rendered with the
// overlay's note (tags stripped) as its text, giving reusable lower-thirds,
// citations and credits that ride a scene. Shared with the review simulation.
func ResolveOverlay(p *project.Project, it model.SequenceItem) (string, error) {
	if strings.HasSuffix(it.File, ".typ") {
		return typst.Render(p, it.File, model.StripTags(it.Note))
	}
	return p.ResolveFootage(it.File)
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

// bedChain builds one bed's filter chain: placement tags (#from_S skips into
// the source, #at_S delays entry into the film, #for_S bounds how long it
// plays), the bed's gain, and a 1.5s fade-out where it ends (its own #for_
// end, or the end of the film).
func bedChain(bed model.SequenceItem, total float64) string {
	chain := achain()
	from, _ := model.TagNumber(bed.Note, "from")
	at, _ := model.TagNumber(bed.Note, "at")
	forDur, hasFor := model.TagNumber(bed.Note, "for")
	if hasFor && forDur > 0 {
		chain += fmt.Sprintf(",atrim=start=%s:end=%s,asetpts=PTS-STARTPTS",
			model.FormatSeconds(from), model.FormatSeconds(from+forDur))
	} else if from > 0 {
		chain += fmt.Sprintf(",atrim=start=%s,asetpts=PTS-STARTPTS", model.FormatSeconds(from))
	}
	if at > 0 {
		chain += fmt.Sprintf(",adelay=%d:all=1", int(at*1000))
	}
	chain += fmt.Sprintf(",volume=%sdB", model.FormatSeconds(bed.Gain))
	end := total
	if hasFor && forDur > 0 && at+forDur < end {
		end = at + forDur
	}
	if end > 3 {
		chain += fmt.Sprintf(",afade=t=out:st=%s:d=1.5", model.FormatSeconds(end-1.5))
	}
	return chain
}

// achainClip is achain plus ~15ms micro-fades at both ends, so joins between
// scenes can never click or pop, plus per-item corrections from tags: #NdB
// gain, and #clean (highpass + gentle denoise, the same treatment the Navy
// Lily recorder gives narration). Too-short clips skip the fades.
func achainClip(it model.SequenceItem) string {
	const f = 0.015
	dur := it.Duration()
	chain := achain()
	if model.HasTag(it.Note, "clean") {
		chain += ",highpass=f=80,afftdn=nr=12:nf=-25"
	}
	if gainDB, ok := model.GainTag(it.Note); ok && gainDB != 0 {
		chain += fmt.Sprintf(",volume=%sdB", model.FormatSeconds(gainDB))
	}
	if dur <= 4*f {
		return chain
	}
	return fmt.Sprintf("%s,afade=t=in:st=0:d=%g,afade=t=out:st=%s:d=%g",
		chain, f, model.FormatSeconds(dur-f), f)
}

// HasAudio reports whether the file carries an audio stream. Real-world
// footage (screen captures, some phone clips) sometimes has none; the export
// substitutes silence instead of failing on a missing [i:a].
func HasAudio(path string) bool {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "a",
		"-show_entries", "stream=codec_type", "-of", "csv=p=0", path).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// OverlayScalePos sizes an overlay to pct% of the frame width (aspect kept)
// and anchors it: corners inset by a margin, c centered, full fitted whole.
// Shared by the export graph and the review simulation.
func OverlayScalePos(corner string, pct, w, h int) (scale, pos string) {
	return overlayScalePos(corner, pct, w, h)
}
