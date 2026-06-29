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

	"movielily/internal/model"
	"movielily/internal/project"
)

// Export renders items to out using ffmpeg. Video clips are assumed to carry
// audio (v1 scope: MP4/H264); images get matching silence.
//
// The invariant: source footage + instructions = export. Footage is only ever
// read, so Export refuses to write its output over any source file.
func Export(p *project.Project, items []model.SequenceItem, out string) error {
	// Section headers are organisational only; they contribute no footage and
	// must not enter the index-coupled concat graph below.
	playable := items[:0:0]
	for _, it := range items {
		if !it.IsSection() {
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
		abs, err := p.ResolveFootage(it.File)
		if err != nil {
			return err
		}
		if abs == outAbs {
			return fmt.Errorf("refusing to overwrite source footage %q with the export", it.File)
		}
		vlab, alab := fmt.Sprintf("v%d", i), fmt.Sprintf("a%d", i)

		switch it.Kind {
		case model.KindImage:
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
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", vIdx, vchain(w, h, fps), vlab)
			fmt.Fprintf(&fc, "[%d:a]%s[%s];", aIdx, achain(), alab)
		default: // video
			if it.Out <= it.In {
				return fmt.Errorf("clip %q has out (%s) <= in (%s)", it.File, model.FormatSeconds(it.Out), model.FormatSeconds(it.In))
			}
			args = append(args, "-ss", model.FormatSeconds(it.In), "-t", model.FormatSeconds(it.Duration()), "-i", abs)
			idx := input
			input++
			fmt.Fprintf(&fc, "[%d:v]%s[%s];", idx, vchain(w, h, fps), vlab)
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

	args = append(args,
		"-filter_complex", fc.String(),
		"-map", "[outv]", "-map", "[outa]",
		"-c:v", "libx264", "-preset", "medium", "-crf", strconv.Itoa(crf),
		"-pix_fmt", "yuv420p", "-r", strconv.Itoa(fps),
		"-c:a", "aac", "-b:a", "192k",
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

// achain normalises audio so every segment is concat-compatible.
func achain() string {
	return "aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo"
}
