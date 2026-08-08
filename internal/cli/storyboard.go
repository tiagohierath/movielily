package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"milklily/internal/ffmpeg"
	"milklily/internal/manim"
	"milklily/internal/model"
	"milklily/internal/project"
	"milklily/internal/store"
	"milklily/internal/typst"
)

func newStoryboardCmd() *cobra.Command {
	var aspect string
	var rows int

	cmd := &cobra.Command{
		Use:   "storyboard <sequence> <out.typ|out.pdf>",
		Short: "Export a printable Typst storyboard book",
		Long: "storyboard writes a printable Typst book for a sequence: six rows per\n" +
			"page, each row with the board image on the left and shot notes, tags,\n" +
			"frame counts and timing on the right. If <out> ends in .pdf, milklily\n" +
			"also runs typst and leaves the editable .typ beside it.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			if rows <= 0 {
				return fmt.Errorf("--rows must be positive")
			}
			ratioName, err := storyboardAspect(p, aspect)
			if err != nil {
				return err
			}
			typPath, pdfPath, compilePDF, err := storyboardOutPaths(args[1])
			if err != nil {
				return err
			}
			if err := refuseInsideFootage(p, typPath); err != nil {
				return err
			}
			if compilePDF {
				if err := refuseInsideFootage(p, pdfPath); err != nil {
					return err
				}
			}
			items, err := store.LoadSequence(p.Sequence(args[0]))
			if err != nil {
				return err
			}
			if len(items) == 0 {
				return fmt.Errorf("sequence %q is empty or missing", args[0])
			}
			items, err = store.Expand(p.SequencesDir(), items)
			if err != nil {
				return err
			}

			assetDir := strings.TrimSuffix(typPath, filepath.Ext(typPath)) + ".assets"
			if err := os.MkdirAll(assetDir, 0o755); err != nil {
				return err
			}
			shots, beds, err := buildStoryboardShots(p, items, assetDir)
			if err != nil {
				return err
			}
			if len(shots) == 0 {
				return fmt.Errorf("sequence %q has no visual shots to print", args[0])
			}
			if err := writeStoryboardTyp(p, args[0], typPath, assetDir, ratioName, rows, shots, beds); err != nil {
				return err
			}
			fmt.Printf("storyboard typ: %s\n", typPath)
			if compilePDF {
				if err := typst.Compile(typPath, pdfPath); err != nil {
					return err
				}
				fmt.Printf("storyboard pdf: %s\n", pdfPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&aspect, "aspect", "auto", "image box aspect: auto, 4:3, or 16:9")
	cmd.Flags().IntVar(&rows, "rows", 6, "storyboard rows per printed page")
	return cmd
}

type storyboardShot struct {
	Num      int
	Section  string
	Kind     string
	File     string
	Asset    string
	Start    float64
	Duration float64
	Frames   int
	Note     string
	Tags     []string
	Meta     []string
}

func buildStoryboardShots(p *project.Project, items []model.SequenceItem, assetDir string) ([]storyboardShot, []string, error) {
	var shots []storyboardShot
	var beds []string
	section := ""
	elapsed := 0.0
	for _, it := range items {
		switch {
		case it.IsSection():
			section = it.Note
			continue
		case it.IsAudio():
			beds = append(beds, fmt.Sprintf("%s  %sdB  %s", it.File, model.FormatSeconds(it.Gain), it.Note))
			continue
		case it.IsOverlay():
			if len(shots) > 0 {
				last := &shots[len(shots)-1]
				last.Meta = append(last.Meta, fmt.Sprintf("overlay: %s +%ss for %ss @ %s",
					it.File, model.FormatSeconds(it.In), model.FormatSeconds(it.Dur), it.Place))
				last.Tags = appendTags(last.Tags, model.Tags(it.Note))
			}
			continue
		}

		dur := it.Duration()
		if dur <= 0 {
			continue
		}
		n := len(shots) + 1
		asset, err := storyboardAsset(p, it, assetDir, n)
		if err != nil {
			return nil, nil, err
		}
		kind := string(it.Kind)
		if it.Kind == model.KindVideo && model.IsAudioFile(it.File) {
			kind = "voice"
		}
		shot := storyboardShot{
			Num:      n,
			Section:  section,
			Kind:     kind,
			File:     it.File,
			Asset:    asset,
			Start:    elapsed,
			Duration: dur,
			Frames:   secondsToFrames(dur, p.Config.FPS),
			Note:     model.StripTags(it.Note),
			Tags:     model.Tags(it.Note),
			Meta: []string{
				fmt.Sprintf("%s  %sf  %ss", kind, model.FormatSeconds(float64(secondsToFrames(dur, p.Config.FPS))), model.FormatSeconds(dur)),
				fmt.Sprintf("start %s  frame %d", clockChapters(elapsed), secondsToFrames(elapsed, p.Config.FPS)),
				it.File,
			},
		}
		if section != "" {
			shot.Meta = append([]string{"section: " + section}, shot.Meta...)
		}
		shots = append(shots, shot)
		elapsed += dur
	}
	return shots, beds, nil
}

func storyboardAsset(p *project.Project, it model.SequenceItem, assetDir string, n int) (string, error) {
	outPNG := filepath.Join(assetDir, fmt.Sprintf("shot-%04d.png", n))
	switch it.Kind {
	case model.KindImage:
		abs, err := p.ResolveFootage(it.File)
		if err != nil {
			return "", err
		}
		ext := strings.ToLower(filepath.Ext(abs))
		if ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
			out := filepath.Join(assetDir, fmt.Sprintf("shot-%04d%s", n, ext))
			return out, copyFile(abs, out)
		}
		return outPNG, ffmpeg.Thumbnail(abs, 0, true, outPNG)
	case model.KindTitle:
		src, err := typst.Render(p, it.File, it.Note)
		if err != nil {
			return "", err
		}
		return outPNG, copyFile(src, outPNG)
	case model.KindAnim:
		src, err := manim.Render(p, it.File, it.Note)
		if err != nil {
			return "", err
		}
		return outPNG, ffmpeg.Thumbnail(src, 0, false, outPNG)
	default:
		abs, err := p.ResolveFootage(it.File)
		if err != nil {
			return "", err
		}
		if model.IsAudioFile(it.File) {
			return outPNG, ffmpeg.Waveform(abs, it.In, it.Out, outPNG)
		}
		return outPNG, ffmpeg.Thumbnail(abs, it.In, false, outPNG)
	}
}

func writeStoryboardTyp(p *project.Project, seq, typPath, assetDir, aspect string, rows int, shots []storyboardShot, beds []string) error {
	if err := os.MkdirAll(filepath.Dir(typPath), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	pages := (len(shots) + rows - 1) / rows
	seq = strings.TrimSuffix(filepath.Base(seq), ".txt")
	fmt.Fprintf(&b, "#set page(paper: \"a4\", margin: 8mm)\n")
	fmt.Fprintf(&b, "#set text(size: 7.8pt, fill: black)\n")
	fmt.Fprintf(&b, "#let border = 1.4pt + black\n")
	fmt.Fprintf(&b, "#let image-ratio = %s\n", typRatio(aspect))
	fmt.Fprintf(&b, "#let row-h = 42mm\n\n")
	fmt.Fprintf(&b, "#let blankrow() = grid(columns: (1.12fr, 0.88fr), column-gutter: 0pt,\n")
	fmt.Fprintf(&b, "  rect(width: 100%%, height: row-h, stroke: border),\n")
	fmt.Fprintf(&b, "  rect(width: 100%%, height: row-h, stroke: border),\n")
	fmt.Fprintf(&b, ")\n\n")
	fmt.Fprintf(&b, "#let shotrow(num, img, meta, tags, note) = grid(columns: (1.12fr, 0.88fr), column-gutter: 0pt,\n")
	fmt.Fprintf(&b, "  rect(width: 100%%, height: row-h, stroke: border, inset: 3pt)[\n")
	fmt.Fprintf(&b, "    #box(width: 100%%, height: 100%%)[#image(img, width: 100%%, height: 100%%, fit: \"contain\")]\n")
	fmt.Fprintf(&b, "  ],\n")
	fmt.Fprintf(&b, "  rect(width: 100%%, height: row-h, stroke: border, inset: 4pt)[\n")
	fmt.Fprintf(&b, "    #text(size: 8.8pt, weight: \"bold\")[SHOT #num]\n")
	fmt.Fprintf(&b, "    #v(1pt)\n")
	fmt.Fprintf(&b, "    #text(size: 6.8pt)[#meta]\n")
	fmt.Fprintf(&b, "    #if tags != \"\" [#v(1pt)#text(size: 6.8pt, fill: rgb(90, 90, 90))[#tags]]\n")
	fmt.Fprintf(&b, "    #v(2pt)\n")
	fmt.Fprintf(&b, "    #text(size: 8pt)[#note]\n")
	fmt.Fprintf(&b, "  ],\n")
	fmt.Fprintf(&b, ")\n\n")

	for page := 0; page < pages; page++ {
		if page > 0 {
			fmt.Fprintf(&b, "#pagebreak()\n")
		}
		fmt.Fprintf(&b, "#grid(columns: (1fr, auto), column-gutter: 8mm,\n")
		fmt.Fprintf(&b, "  [#text(size: 12pt, weight: \"bold\")[MILKLILY STORYBOARD / #%s]],\n", typString(seq))
		fmt.Fprintf(&b, "  [#text(size: 7pt)[#%s / #%s / page %d of %d]],\n", typString(p.Config.Name), typString(aspect), page+1, pages)
		fmt.Fprintf(&b, ")\n")
		if page == 0 && len(beds) > 0 {
			fmt.Fprintf(&b, "#text(size: 6.8pt)[audio beds: #%s]\n", typString(strings.Join(beds, " / ")))
		}
		fmt.Fprintf(&b, "#v(3mm)\n")
		for i := 0; i < rows; i++ {
			idx := page*rows + i
			if idx >= len(shots) {
				fmt.Fprintf(&b, "#blankrow()\n")
				continue
			}
			shot := shots[idx]
			rel, err := filepath.Rel(filepath.Dir(typPath), shot.Asset)
			if err != nil {
				return err
			}
			meta := strings.Join(shot.Meta, "\n")
			tags := strings.Join(shot.Tags, " ")
			note := shot.Note
			if strings.TrimSpace(note) == "" {
				note = " "
			}
			fmt.Fprintf(&b, "#shotrow(%s, %s, %s, %s, %s)\n",
				typString(fmt.Sprintf("%04d", shot.Num)),
				typString(filepath.ToSlash(rel)),
				typString(meta),
				typString(tags),
				typString(note),
			)
		}
	}
	return os.WriteFile(typPath, []byte(b.String()), 0o644)
}

func storyboardAspect(p *project.Project, s string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "", "auto":
		if p.Config.Height > 0 && float64(p.Config.Width)/float64(p.Config.Height) > 1.55 {
			return "16:9", nil
		}
		return "4:3", nil
	case "4:3", "4x3":
		return "4:3", nil
	case "16:9", "16x9":
		return "16:9", nil
	default:
		return "", fmt.Errorf("invalid --aspect %q (want auto, 4:3, or 16:9)", s)
	}
}

func storyboardOutPaths(out string) (typPath, pdfPath string, compilePDF bool, err error) {
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", "", false, err
	}
	ext := strings.ToLower(filepath.Ext(abs))
	switch ext {
	case ".pdf":
		return strings.TrimSuffix(abs, ext) + ".typ", abs, true, nil
	case ".typ":
		return abs, "", false, nil
	case "":
		return abs + ".typ", "", false, nil
	default:
		return "", "", false, fmt.Errorf("storyboard output must be .typ or .pdf")
	}
}

func typRatio(aspect string) string {
	if aspect == "16:9" {
		return "16 / 9"
	}
	return "4 / 3"
}

func typString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "")
	return "\"" + s + "\""
}

func appendTags(tags []string, extra []string) []string {
	seen := map[string]bool{}
	for _, t := range tags {
		seen[t] = true
	}
	for _, t := range extra {
		if !seen[t] {
			tags = append(tags, t)
			seen[t] = true
		}
	}
	return tags
}
