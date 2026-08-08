package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"milklily/internal/model"
	"milklily/internal/project"
	"milklily/internal/store"
)

type pictogrepMeta struct {
	Tags  []string `json:"tags"`
	Query string   `json:"query"`
}

func newIntakeCmd() *cobra.Command {
	var frames int
	var tag string
	cmd := &cobra.Command{
		Use:   "intake <boards|refs> <sequence>",
		Short: "Append unused Pictogrep boards or tagged references to a sequence",
		Long: "intake is the explicit bridge from Pictogrep into a film. boards reads\n" +
			"storyboards/inbox/; refs reads refs/visual/pictogrep/ (or just one tag\n" +
			"with --tag). It appends only unused image paths and leaves source media\n" +
			"untouched. Pictogrep storyboard sidecars provide the initial note/tags.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			if frames <= 0 {
				return fmt.Errorf("--frames must be positive")
			}
			root, err := intakeRoot(p, args[0], tag)
			if err != nil {
				return err
			}
			n, err := intakeImages(p, args[1], root, frames)
			if err != nil {
				return err
			}
			fmt.Printf("intake %s: imported %d image(s) into sequences/%s.txt\n", args[0], n, strings.TrimSuffix(filepath.Base(args[1]), ".txt"))
			return nil
		},
	}
	cmd.Flags().IntVar(&frames, "frames", 48, "default still duration in frames")
	cmd.Flags().StringVar(&tag, "tag", "", "only import this Pictogrep reference tag (refs only)")
	return cmd
}

func intakeRoot(p *project.Project, source, tag string) (string, error) {
	switch source {
	case "boards":
		if tag != "" {
			return "", fmt.Errorf("--tag only applies to intake refs")
		}
		return p.StoryboardInboxDir(), nil
	case "refs":
		root := filepath.Join(p.RefsDir(), "visual", "pictogrep")
		if tag == "" {
			return root, nil
		}
		clean := filepath.Base(tag)
		if clean == "." || clean == "" || clean != tag {
			return "", fmt.Errorf("invalid tag %q", tag)
		}
		return filepath.Join(root, clean), nil
	default:
		return "", fmt.Errorf("source must be boards or refs, got %q", source)
	}
}

func intakeImages(p *project.Project, sequence, root string, frames int) (int, error) {
	items, err := store.LoadSequence(p.Sequence(sequence))
	if err != nil {
		return 0, err
	}
	used := map[string]bool{}
	for _, item := range items {
		if item.Kind == model.KindImage {
			used[item.File] = true
		}
	}
	files, err := intakeImageFiles(root)
	if err != nil {
		return 0, err
	}
	duration := float64(frames) / float64(p.Config.FPS)
	added := 0
	for _, file := range files {
		stored := p.StoreName(file)
		if used[stored] {
			continue
		}
		items = append(items, model.SequenceItem{Kind: model.KindImage, File: stored, Dur: duration, Note: intakeNote(file)})
		used[stored] = true
		added++
	}
	if added == 0 {
		return 0, nil
	}
	lines := []string{"# " + strings.TrimSuffix(filepath.Base(sequence), ".txt") + " - edited with milklily"}
	for _, item := range items {
		lines = append(lines, item.String())
	}
	return added, store.WriteLines(p.Sequence(sequence), lines)
}

func intakeImageFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && isStoryboardImage(d.Name()) {
			files = append(files, path)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func intakeNote(image string) string {
	note := strings.TrimSuffix(filepath.Base(image), filepath.Ext(image))
	metaPath := strings.TrimSuffix(image, filepath.Ext(image)) + ".json"
	body, err := os.ReadFile(metaPath)
	if err != nil {
		return note
	}
	var meta pictogrepMeta
	if json.Unmarshal(body, &meta) != nil {
		return note
	}
	if query := strings.TrimSpace(meta.Query); query != "" {
		note = query
	}
	for _, tag := range meta.Tags {
		for _, parsed := range model.Tags("#" + tag) {
			note += " " + parsed
		}
	}
	return strings.TrimSpace(note)
}
