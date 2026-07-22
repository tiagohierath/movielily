package ffmpeg

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"

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

// SpokenRegions runs silencedetect over an audio file and returns the
// NON-silent stretches: the spoken takes of a continuous narration recording,
// with the pauses, breaths and dead air between them removed. noise is the
// silence floor in dB (e.g. -35), minGap the silence length (s) that counts
// as a break.
func SpokenRegions(src string, noise float64, minGap float64) (regions [][2]float64, total float64, err error) {
	cmd := exec.Command("ffmpeg", "-nostdin", "-i", src,
		"-af", fmt.Sprintf("silencedetect=noise=%sdB:d=%s", model.FormatSeconds(noise), model.FormatSeconds(minGap)),
		"-f", "null", "-")
	out, _ := cmd.CombinedOutput() // ffmpeg exits 0 here; output is on stderr
	text := string(out)

	if m := regexp.MustCompile(`Duration: (\d+):(\d+):(\d+\.?\d*)`).FindStringSubmatch(text); m != nil {
		hh, _ := strconv.ParseFloat(m[1], 64)
		mm, _ := strconv.ParseFloat(m[2], 64)
		ss, _ := strconv.ParseFloat(m[3], 64)
		total = hh*3600 + mm*60 + ss
	}
	if total == 0 {
		return nil, 0, fmt.Errorf("could not read duration of %s", src)
	}

	starts := regexp.MustCompile(`silence_start: (-?\d+\.?\d*)`).FindAllStringSubmatch(text, -1)
	ends := regexp.MustCompile(`silence_end: (-?\d+\.?\d*)`).FindAllStringSubmatch(text, -1)
	pos := 0.0
	for i, s := range starts {
		sStart, _ := strconv.ParseFloat(s[1], 64)
		if sStart > pos {
			regions = append(regions, [2]float64{pos, sStart})
		}
		if i < len(ends) {
			pos, _ = strconv.ParseFloat(ends[i][1], 64)
		} else {
			pos = total // silence runs to the end of the file
		}
	}
	if pos < total {
		regions = append(regions, [2]float64{pos, total})
	}
	return regions, total, nil
}

// Frame extracts one FULL-resolution frame at `at` seconds as a PNG (the
// thumbnail workflow; Thumbnail above is the small preview variant).
func Frame(src string, at float64, outPNG string) error {
	args := []string{"-nostdin", "-loglevel", "error", "-y"}
	if at > 0 {
		args = append(args, "-ss", model.FormatSeconds(at))
	}
	args = append(args, "-i", src, "-map", "0:v:0", "-frames:v", "1", "-f", "image2", outPNG)
	cmd := exec.Command("ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg frame: %v: %s", err, out)
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
