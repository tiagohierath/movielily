package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"movielily/internal/project"
)

// The uploader is Tiago's existing script (navylily-tools/youtube_upload.sh):
// it uploads one video from YT_OUTPUT_DIR as PRIVATE, reading a
// "<name>.title.txt" sidecar for the title. movielily reuses it rather than
// reimplementing OAuth, so posting a render is: stage the file into its own
// dir with a title sidecar, point the script at it, run it.

func youtubeScript() string {
	if s := strings.TrimSpace(os.Getenv("MOVIELILY_YOUTUBE")); s != "" {
		return s
	}
	return filepath.Join(os.Getenv("HOME"), "projects", "navylily-tools", "youtube_upload.sh")
}

func lastRenderFile(p *project.Project) string {
	return filepath.Join(p.Root, ".cache", "last-render.txt")
}

// recordLastRender remembers the path of the most recent real export.
func recordLastRender(p *project.Project, out string) {
	abs, err := filepath.Abs(out)
	if err != nil {
		return
	}
	dir := filepath.Join(p.Root, ".cache")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(lastRenderFile(p), []byte(abs+"\n"), 0o644)
}

// PostLastRender stages the given video (or the last render if file is empty)
// with a title sidecar and runs the uploader. Returns the resolved path so a
// caller can report it. The uploader runs attached (it may prompt for OAuth
// the first time), so callers from the TUI must suspend the terminal first.
func PostLastRender(p *project.Project, file, title string) (string, error) {
	if file == "" {
		data, err := os.ReadFile(lastRenderFile(p))
		if err != nil {
			return "", fmt.Errorf("no render recorded yet (run 'movielily export' first)")
		}
		file = strings.TrimSpace(string(data))
	}
	if _, err := os.Stat(file); err != nil {
		return "", fmt.Errorf("render not found: %s", file)
	}
	script := youtubeScript()
	if _, err := os.Stat(script); err != nil {
		return "", fmt.Errorf("uploader not found at %s (set MOVIELILY_YOUTUBE to your youtube_upload.sh)", script)
	}

	// Stage the one file in its own dir so the uploader can't pick a different
	// video, with the title sidecar the script reads.
	stage, err := os.MkdirTemp("", "movielily-yt-")
	if err != nil {
		return "", err
	}
	base := filepath.Base(file)
	link := filepath.Join(stage, base)
	if err := os.Symlink(file, link); err != nil {
		return "", err
	}
	if title == "" {
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	sidecar := strings.TrimSuffix(link, filepath.Ext(link)) + ".title.txt"
	if err := os.WriteFile(sidecar, []byte(title+"\n"), 0o644); err != nil {
		return "", err
	}

	cmd := exec.Command(script)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		"YT_OUTPUT_DIR="+stage,
		// Project-local state so movielily's uploads don't share (or trip) the
		// navylily daily-timer's cooldown, and on-demand posting isn't blocked.
		"YT_STATE_DIR="+filepath.Join(p.Root, ".cache", "youtube"),
		"YT_MIN_HOURS_BETWEEN=0",
	)
	return file, cmd.Run()
}

func newYoutubeCmd() *cobra.Command {
	var title string
	cmd := &cobra.Command{
		Use:   "youtube [video.mp4]",
		Short: "Post a render to YouTube (PRIVATE) via your youtube_upload.sh",
		Long: "youtube uploads the given file, or the last export if none is given, to\n" +
			"YouTube as a private video using your existing uploader script\n" +
			"(navylily-tools/youtube_upload.sh; override with MOVIELILY_YOUTUBE). The\n" +
			"first run opens the Google OAuth flow in a browser.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Open()
			if err != nil {
				return err
			}
			file := ""
			if len(args) == 1 {
				file = args[0]
			}
			path, err := PostLastRender(p, file, title)
			if err != nil {
				return err
			}
			fmt.Printf("posted %s to YouTube (private)\n", filepath.Base(path))
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "video title (defaults to the file name)")
	return cmd
}
