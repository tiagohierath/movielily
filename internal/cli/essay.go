package cli

import (
	"bufio"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"milklily/internal/model"
	"milklily/internal/project"
	"milklily/internal/store"
)

// essay turns simple timestamp cues into a narration-first video essay. Mark
// timestamps while listening with `milklily watch narration.wav` (press m),
// then this command asks for the image to show at each one.
func newEssayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "essay <sequence> <narration>",
		Short: "Build a simple image-over-narration essay from watch markers",
		Long: "Listen with 'milklily watch <narration>' and press m whenever the\n" +
			"image should change. Then run essay: type an image name at each cue,\n" +
			"or press Enter to leave that moment black. It creates a fresh sequence\n" +
			"with the narration as one continuous track and full-screen images at\n" +
			"your timestamps.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			seqPath := p.Sequence(args[0])
			existing, err := store.LoadSequence(seqPath)
			if err != nil {
				return err
			}
			if len(existing) != 0 {
				return fmt.Errorf("sequence %q already has items; use a new sequence name", args[0])
			}
			narration, err := storeExistingMedia(p, args[1])
			if err != nil {
				return err
			}
			if !model.IsAudioFile(narration) {
				return fmt.Errorf("narration must be an audio file")
			}
			markers, err := store.LoadMarkers(p.Markers())
			if err != nil {
				return err
			}
			var cues []model.Marker
			for _, m := range markers {
				if m.File == narration {
					cues = append(cues, m)
				}
			}
			if len(cues) == 0 {
				return fmt.Errorf("no markers for %s; run 'milklily watch %s' and press m at each image change", narration, args[1])
			}
			sort.Slice(cues, func(i, j int) bool { return cues[i].Time < cues[j].Time })
			duration, err := mediaDuration(p, narration)
			if err != nil {
				return err
			}

			in := bufio.NewReader(cmd.InOrStdin())
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "\n%s — %d image cue(s)\n", filepath.Base(narration), len(cues))
			fmt.Fprintln(out, "Type an image name/path for each timestamp. Enter = leave it black.")
			fmt.Fprintln(out)
			images := make([]string, len(cues))
			for i, cue := range cues {
				if cue.Time >= duration {
					continue
				}
				hint := strings.TrimSpace(cue.Note)
				if hint != "" {
					hint = " — " + hint
				}
				fmt.Fprintf(out, "%s%s  image: ", model.FormatSeconds(cue.Time), hint)
				answer, err := in.ReadString('\n')
				if err != nil && strings.TrimSpace(answer) == "" {
					return err
				}
				answer = strings.TrimSpace(answer)
				if answer == "" {
					continue
				}
				image, err := storeExistingMedia(p, answer)
				if err != nil {
					return err
				}
				if !isEssayImage(image) {
					return fmt.Errorf("%s is not an image", answer)
				}
				images[i] = image
			}

			lines := essayLines(args[0], narration, duration, cues, images)
			if err := store.WriteLines(seqPath, lines); err != nil {
				return err
			}
			fmt.Fprintf(out, "\ncreated sequences/%s.txt — next: milklily edit %s\n", strings.TrimSuffix(filepath.Base(seqPath), ".txt"), args[0])
			return nil
		},
	}
	return cmd
}

func mediaDuration(p *project.Project, stored string) (float64, error) {
	abs, err := p.ResolveFootage(stored)
	if err != nil {
		return 0, err
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", abs).Output()
	if err != nil {
		return 0, fmt.Errorf("could not read narration duration (is ffprobe installed?): %w", err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid narration duration for %s", stored)
	}
	return duration, nil
}

func isEssayImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	default:
		return false
	}
}

func essayLines(sequence, narration string, duration float64, cues []model.Marker, images []string) []string {
	lines := []string{"# " + sequence + " - image essay", model.SequenceItem{Kind: model.KindVideo, File: narration, In: 0, Out: duration, Note: "narration"}.String()}
	for i, cue := range cues {
		if i >= len(images) || images[i] == "" || cue.Time >= duration {
			continue
		}
		end := duration
		if i+1 < len(cues) && cues[i+1].Time < end {
			end = cues[i+1].Time
		}
		if end <= cue.Time {
			continue
		}
		lines = append(lines, model.SequenceItem{Kind: model.KindOverlay, File: images[i], In: cue.Time, Dur: end - cue.Time, Place: "full", Note: cue.Note}.String())
	}
	return lines
}
