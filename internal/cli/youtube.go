package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
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

func resolveRender(p *project.Project, file string) (string, error) {
	if file == "" {
		data, err := os.ReadFile(lastRenderFile(p))
		if err != nil {
			return "", fmt.Errorf("no render recorded yet (run 'movielily export' first)")
		}
		file = strings.TrimSpace(string(data))
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("render not found: %s", abs)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("render is not a regular file: %s", abs)
	}
	return abs, nil
}

func youtubeQueueDir(script string) string {
	if q := strings.TrimSpace(os.Getenv("MOVIELILY_YOUTUBE_QUEUE")); q != "" {
		return q
	}
	return filepath.Join(filepath.Dir(script), "videos", "output")
}

func currentYouTubeCadence() string {
	stateDir := strings.TrimSpace(os.Getenv("YT_STATE_DIR"))
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "daily"
		}
		stateDir = filepath.Join(home, ".local", "state", "navylily-youtube")
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	if err != nil {
		return "daily"
	}
	var state struct {
		Cadence string `json:"cadence"`
	}
	if json.Unmarshal(data, &state) != nil {
		return "daily"
	}
	if state.Cadence == "weekly" || state.Cadence == "monthly" || state.Cadence == "daily" {
		return state.Cadence
	}
	return "daily"
}

func contentHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func writeSidecar(path, title string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".movielily-title-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.WriteString(tmp, title+"\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// QueueLastRender makes an immutable handoff to the shared navylily YouTube
// queue. A hard link keeps it cheap when possible; otherwise it copies the
// completed render. The timer/uploader then applies the selected cadence.
func QueueLastRender(p *project.Project, file, title string) (string, error) {
	file, err := resolveRender(p, file)
	if err != nil {
		return "", err
	}
	script := youtubeScript()
	if _, err := os.Stat(script); err != nil {
		return "", fmt.Errorf("uploader not found at %s (set MOVIELILY_YOUTUBE to your youtube_upload.sh)", script)
	}
	queue := youtubeQueueDir(script)
	if err := os.MkdirAll(queue, 0o755); err != nil {
		return "", err
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	sum, err := contentHash(file)
	if err != nil {
		return "", err
	}
	base, ext := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)), filepath.Ext(file)
	target := filepath.Join(queue, base+"-"+sum[:12]+ext)
	sidecar := strings.TrimSuffix(target, ext) + ".title.txt"
	if _, err := os.Stat(target); err == nil {
		if _, err := os.Stat(sidecar); os.IsNotExist(err) {
			if err := writeSidecar(sidecar, title); err != nil {
				return "", err
			}
		}
		return target, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// Write metadata first: the uploader only sees a video after this succeeds.
	if err := writeSidecar(sidecar, title); err != nil {
		return "", err
	}
	if err := os.Link(file, target); err == nil {
		return target, nil
	}
	// Different filesystems cannot hard-link. Copy to a temp file, then link it
	// into place atomically so a concurrent queue action cannot overwrite it.
	tmp, err := os.CreateTemp(queue, ".movielily-video-")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	src, err := os.Open(file)
	if err != nil {
		tmp.Close()
		return "", err
	}
	_, copyErr := io.Copy(tmp, src)
	closeErr := src.Close()
	if copyErr != nil || closeErr != nil {
		tmp.Close()
		if copyErr != nil {
			return "", copyErr
		}
		return "", closeErr
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tmpName, target); err != nil && !os.IsExist(err) {
		return "", err
	}
	return target, nil
}

// PostLastRender stages the given video (or the last render if file is empty)
// with a title sidecar and runs the uploader. Returns the resolved path so a
// caller can report it. The uploader runs attached (it may prompt for OAuth
// the first time), so callers from the TUI must suspend the terminal first.
func PostLastRender(p *project.Project, file, title string) (string, error) {
	file, err := resolveRender(p, file)
	if err != nil {
		return "", err
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
	var now bool
	cmd := &cobra.Command{
		Use:   "youtube [video.mp4]",
		Short: "Queue a render for YouTube via your shared uploader",
		Long: "youtube queues the given file, or the last export if none is given, for\n" +
			"the shared YouTube uploader. It posts on the cadence you selected there.\n" +
			"Pass --now only to upload immediately as a private video.\n" +
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
			var path string
			if now {
				fmt.Println("warning: --now bypasses the shared posting cadence")
				path, err = PostLastRender(p, file, title)
			} else {
				path, err = QueueLastRender(p, file, title)
			}
			if err != nil {
				return err
			}
			if now {
				fmt.Printf("posted %s to YouTube (private)\n", filepath.Base(path))
			} else {
				displayTitle := title
				if displayTitle == "" {
					sidecar := strings.TrimSuffix(path, filepath.Ext(path)) + ".title.txt"
					if data, readErr := os.ReadFile(sidecar); readErr == nil {
						displayTitle = strings.TrimSpace(string(data))
					}
				}
				if displayTitle == "" {
					displayTitle = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
				}
				fmt.Printf("Queued for YouTube\n  title: %s\n  cadence: %s\n  next: nl-queue\n  after upload: set the thumbnail in YouTube Studio\n", displayTitle, currentYouTubeCadence())
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "video title (defaults to the file name)")
	cmd.Flags().BoolVar(&now, "now", false, "upload immediately instead of joining the shared queue")
	return cmd
}
