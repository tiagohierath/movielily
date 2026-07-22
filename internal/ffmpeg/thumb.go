package ffmpeg

import (
	"fmt"
	"os/exec"

	"movielily/internal/model"
)

// Thumbnail extracts a single frame from src at time `at` (seconds) and writes
// it as a PNG to outPNG, scaled to ~480px wide. For images, `at` is ignored.
// It only ever reads src — the footage invariant holds here too.
func Thumbnail(src string, at float64, isImage bool, outPNG string) error {
	args := []string{"-nostdin", "-loglevel", "error", "-y"}
	if !isImage && at > 0 {
		// Fast input seeking: accurate enough for a preview, much quicker than
		// decoding from the start of the clip.
		args = append(args, "-ss", model.FormatSeconds(at))
	}
	args = append(args,
		"-i", src,
		"-map", "0:v:0",
		"-frames:v", "1",
		"-vf", "scale=480:-2",
		"-f", "image2",
		outPNG,
	)
	cmd := exec.Command("ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg thumbnail: %v: %s", err, out)
	}
	return nil
}

// Waveform draws the [in,out] slice of an audio file as a waveform PNG, the
// preview a voice segment gets instead of a first frame. Read-only, like
// everything else that touches footage.
func Waveform(src string, in, out float64, outPNG string) error {
	args := []string{"-nostdin", "-loglevel", "error", "-y"}
	if in > 0 {
		args = append(args, "-ss", model.FormatSeconds(in))
	}
	if out > in {
		args = append(args, "-t", model.FormatSeconds(out-in))
	}
	args = append(args,
		"-i", src,
		"-filter_complex", "showwavespic=s=480x180:colors=white:split_channels=0",
		"-frames:v", "1",
		"-f", "image2",
		outPNG,
	)
	cmd := exec.Command("ffmpeg", args...)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg waveform: %v: %s", err, msg)
	}
	return nil
}
