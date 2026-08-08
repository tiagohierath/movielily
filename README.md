# milklily

milklily is a notebook-style short-film editor: sort storyboard images in the
browser, time and refine the cut in a terminal UI, watch it instantly without
rendering, and export a YouTube-ready 4:3 file with ffmpeg.

**The one rule:** your source media is never modified, moved, or renamed. mpv,
ffmpeg and the browser board only ever *read* it. Every decision — every cut,
duration, pan, effect, colour grade and note — is plain text; export always
writes a brand-new file. So **everything is reversible** (delete the text),
reproducible (same text, same pixels) and versionable.

Times are always **seconds**: `90`, `90s`, and `1:30` all mean the same thing.

Needs **mpv** and **ffmpeg** on PATH. Title cards additionally use **typst**,
animated cards use **manim**; both are optional, and on a nix machine they are
fetched ephemerally when missing.

---

## Command reference

| command | what it does |
|---|---|
| `milklily init [dir] [--git] [--footage <src>]` | create a GitHub-friendly short-film project |
| `milklily watch <clip>` | play in mpv and log: `m` marker, `i`/`o` in/out, `Enter` select (works on audio files too) |
| `milklily marker add <clip> <t> [note]` · `marker list` | markers by hand |
| `milklily select add <clip> <in> <out> [note]` · `select list` | selects by hand |
| `milklily note add [--clip c] [--time t] <text>` · `note list` | free notes, timestamped if you want |
| `milklily search <term>` | search markers, selects and notes |
| `milklily tag [name]` | list #tags, or everything tagged #name |
| `milklily seq from-selects <seq> [--force]` | seed a sequence from all selects |
| `milklily seq video <seq> <file> <in> <out> [note]` | append a clip (or a slice of a voice recording) |
| `milklily seq image <seq> <img> <dur> [note]` | append a still (`#cover` fills; `#pan_rl` etc. pans) |
| `milklily seq title <seq> <template> <dur> <text>` | append a typst title card |
| `milklily seq anim <seq> <template> <text>` | append a manim animated card (renders now, measures length) |
| `milklily seq overlay <seq> <img|tpl.typ> <at> <dur> [--place br:33] [note]` | image, or a typst template whose text is the note (lower-thirds), on top of the LAST scene |
| `milklily seq use <seq> <other>` | splice another sequence in (chapters edited separately) |
| `milklily silences <audio> [--keep]` | find the spoken stretches of a narration take; --keep turns them into selects |
| `milklily essay <sequence> <narration>` | turn watch-marker timestamps into a simple image-over-narration essay |
| `milklily grade params/list/show/set` | manage colour-grade / film-grain presets (grades/*.grade) |
| `milklily seq audio <seq> <file> [--gain -12] [note]` | music/narration bed under the whole cut |
| `milklily seq show <seq>` · `seq list` | inspect sequences |
| `milklily board <seq>` | browser light table for image storyboards: add local images, sort, time, pan, preview, save EDL |
| `milklily intake <boards|refs> <seq>` | explicitly append unused Pictogrep boards or tagged references to a cut |
| `milklily sketch` | open Pictogrep storyboard mode for the current project |
| `milklily edit [seq]` | the interactive editor (see keys below) |
| `milklily review <seq> [--from N]` | watch the cut in mpv, simulated export, no render |
| `milklily export <seq> <out.mp4> [--draft]` | render the real file (auto-snapshots; --draft = half-res quick look) |
| `milklily storyboard <seq> <out.typ|out.pdf> [--aspect 4:3|16:9]` | printable Typst/PDF storyboard book |
| `milklily chapters <seq>` | YouTube chapters from your sections, ready for the description |
| `milklily frame <clip> <t> <out.png>` | full-resolution frame grab (thumbnails) |
| `milklily youtube [video] [--title T]` | queue the last render (or a given file) for YouTube via your uploader script |
| `milklily snapshot [message]` | commit the instructions to git (creates the repo on first use) |
| `milklily snapshot list` · `snapshot restore <id>` | see versions · roll back (safely: it snapshots first) |
| `milklily doctor [--fix]` | check/repair project folders, `.gitignore`, sequences and git readiness |
| `milklily version` | version info |

Aliases: `m`=marker, `sel`=select, `n`=note, `s`=seq, `snap`=snapshot.

---

## The sequence file

A sequence is the movie: an ordered list of records, one per line, in
`sequences/<name>.txt`. Edit it in the browser board, the TUI, or in vim; all
views stay in sync.

```
section|Abertura                                  organisational folder, no runtime
title|chapter.typ|4|Capítulo 1                    typst card, 4s on screen
video|footage/raw/clip001.mp4|72.3|85.1|the punchline #best
overlay|fxs/overlays/ref.png|2|5|tr:30|the reference
video|audio/dialogue/voz.wav|0|35|narração         voice slice: black canvas + sound
image|storyboards/inbox/photo.jpg|5|opening #cover still; #cover crops to fill
image|images/stills/wide.jpg|2|push past tower #pan_rl #ease_out
anim|card.py|3.8|Fim                              manim card (length measured)
audio|audio/music/song.mp3|-12|music bed           under the whole cut, cut at the end
```

Notes carry `#tags` anywhere; `search` and `tag` find them and the TUI
colours them. Some tags are also switches:

- `#cover` on a visual item: fill the frame (crop) instead of letterboxing;
- `#pan_lr` `#pan_rl` `#pan_tb` `#pan_bt` on an image: pan left/right or
  top/bottom over the shot's duration; use wider/taller images for visible
  movement;
- `#ease_linear` / `#ease_in` / `#ease_out` / `#ease_inout` on a panned image:
  choose the timing curve;
- `#mute` on a clip: silence its own sound (b-roll riding over narration);
- `#-6db` / `#+3db` on a clip: adjust just that clip's level;
- `#clean` on a clip/voice slice: highpass + gentle denoise for rough recordings;
- `#duck` on a bed: sidechain-duck the music under the timeline's voice;
- `#at_image_N` on a bed: begin at the Nth still image, recalculated whenever
  shots are retimed or reordered; `#at_scene_N` does the same for any playable
  scene (image, video, title, or animation).
- `#at_S` `#from_S` `#for_S` on a bed: enter the film at second S, skip S
  seconds into the source, play for S seconds (music per section instead of
  wall to wall). A fixed `#at_S` wins when combined with an image/scene anchor.

Clips whose file has no audio stream at all (some screen captures) export
with silence automatically instead of failing.

## Workflows

### The simplest image essay

```bash
# 1. Listen once. Press m each time the picture should change.
milklily watch audio/dialogue/essay.wav

# 2. For every timestamp, type the image to show (Enter skips a cue).
milklily essay essay audio/dialogue/essay.wav

# 3. Check it, render it, and queue it for YouTube.
milklily edit essay
milklily export essay exports/video/essay.mp4
milklily youtube --title "My video essay"
```

Put images in `images/stills/` first. `essay` keeps the narration continuous
and makes each chosen image full-screen until the next marker.

You can also open an image board while arranging images (for example,
`milklily board images --open`). Its small audio strip plays files from
`audio/`, shows the exact timestamp, and has start, end, play/pause, and
±5-second controls. Clicking an image jumps the narration clock to that
image's current start time.

### Project layout

```bash
milklily init my-film --git
cd my-film
milklily board main --open
```

`init` creates a folder meant to stay readable in any file manager:

```text
scripts/                  script drafts, dialogue, shot notes
storyboards/inbox/        boards imported from the browser light table
storyboards/scenes/       optional manual scene folders
images/stills/            finished stills and cleaned board images
images/backgrounds/       plates and backgrounds
refs/visual/              reference images and mood material
refs/research/            research material
audio/dialogue/           voice, narration and dialogue takes
audio/music/              music beds
audio/sfx/                sound effects
audio/ambience/           room tone and atmosphere
fxs/overlays/             transparent PNGs and compositing elements
fxs/mattes/               mattes and masks
fxs/textures/             grain, paper, dust, light leaks
footage/raw/              raw clips and legacy catch-all media
sequences/                the plain-text cuts
exports/video/            rendered movies
exports/storyboard-books/ printable Typst/PDF storyboard books
```

Every important folder has a small `README.txt`. New projects also get a
GitHub-friendly `README.md`, `.gitignore`, and `sequences/main.txt`; the ignore
rules keep source media and generated exports out of git while keeping the
text instructions trackable. For older projects, run:

```bash
milklily doctor --fix
```

Each film is self-contained: deleting its project directory removes only that
film's instructions and local media. Pictogrep references sent into
`refs/visual/pictogrep/` are symlinks, so the original library stays outside
the film and is never moved or copied.

For collaboration, `milklily snapshot` stages only the portable text surface:
the cut, notes, templates, grades, scripts, configuration, and folder docs.
Raw media, exports, caches, and Pictogrep links remain outside Git. A friend
can clone the project, add their own media to the documented folders, and use
the same sequence files. Run `milklily doctor` before the first push; it warns
if an older project already has media tracked by Git.

### Browser board

```bash
milklily board main --open
```

The browser board is a light table: unused images on the left, the ordered EDL
in the middle, and a fixed preview on the right. Add images from your computer,
drag them into order, set duration with preset buttons or the text box, choose
simple vertical/horizontal pan, press play, then save. It writes the same
`sequences/main.txt` file the TUI reads.

Uploads from the browser land in `storyboards/inbox/`. The board scans
`storyboards/`, `images/`, `refs/`, `fxs/` and legacy `footage/` recursively.

### Pictogrep bridge

Pictogrep is optional visual research and sketching support. From inside a
project, `milklily sketch` opens Pictogrep against `refs/visual/` and writes
new boards to `storyboards/inbox/`. It never edits the sequence by itself:

```bash
milklily sketch
milklily intake boards main
milklily intake refs main --tag cinematic
```

`intake boards` imports only unused boards from the inbox. `intake refs` reads
the references linked by `pictogrep tags send <tag>` into
`refs/visual/pictogrep/<tag>/`. Pictogrep's source/tags/query sidecar becomes
the initial image note where available; the resulting cut remains plain text.

### Printable storyboard book

```bash
milklily storyboard main exports/storyboard-books/main.pdf --aspect 4:3
milklily storyboard main exports/storyboard-books/main-16x9.pdf --aspect 16:9
```

This exports a Typst/PDF book: a contact-sheet grid with each doodle on one
side and notes, numbers, durations, tags and file names on the other. It is for
printing the movie as a book, not rendering video.

### Voice-first (narrated videos)

Record your narration anywhere, pauses and retakes included, then:

```bash
cp ~/gravacao.wav audio/dialogue/
milklily silences audio/dialogue/gravacao.wav --keep
milklily seq from-selects aula
milklily edit aula                 # prune misfires (d), split (s), cards (T)
milklily seq audio aula audio/music/trilha.mp3 --gain -14 "trilha #duck"
milklily export aula exports/video/aula.mp4
```

(`milklily watch gravacao.wav` is the manual alternative: listen and mark
takes with i/o+Enter yourself.)

A voice slice in the timeline shows a black canvas; decorate it with overlays
(your drawings, references) and cards. The TUI previews voice scenes as the
waveform of that exact slice.

### Footage-first

`watch` each clip and mark selects as it plays, `seq from-selects`, then
arrange in the TUI, `review`, `export`.

## watch: logging while playing

mpv opens with an on-screen HUD. `m` drops a marker, `i`/`o` set IN/OUT
(shown on the seekbar, looped via A-B so you can check the trim), `Enter`
saves the select, `q` quits. Markers land in `markers.txt`, selects in
`selects.txt`. Works identically on audio files.

## edit: the TUI

```bash
milklily edit filme        # or just `milklily edit` with a single sequence
```

Three panes. **Left**: the cut, one scene per line, colour-coded with an icon
per kind (▶ clip · ∿ voice · ▦ image · ▣ title card · ✦ animated card · ♪ bed ·
◱ overlay · ⧉ nested). **Centre**: a large single-frame preview of the
selected scene (the clip's start frame, the rendered card, or the voice
waveform, in kitty/Ghostty/WezTerm) with its timing and grade summary beneath.
**Right**: the scene's note.

| key | action |
|---|---|
| `j`/`k`, arrows | move · `J`/`K` reorder · `g`/`G` top/bottom · `[`/`]` prev/next section |
| `s` | split the clip in two at a point picked in mpv |
| `<`/`>` | nudge the clip's in point ±0.5s (`+`/`-` does the out point) |
| `c` | colour-grade panel: live sliders for the scene's grade (see below) |
| `Enter` | open the thing under the cursor in an mpv window, editor stays live: clips replay for redoing in/out (applies when you confirm), images/overlays/cards/animations/beds just open |
| `r` / `R` | watch from here / the whole cut (simulated export in an mpv window, nothing renders) |
| `T` / `A` | insert a title card / animated card below the cursor: pick template (last one prefilled), type text |
| `e` | edit the note (or a card's text, or a section's heading) |
| `t` | edit the number that matters: duration (stills, cards, overlays) or gain (beds) |
| `+`/`-` | nudge without typing: out point ±0.5s, duration ±0.5s, gain ±1dB |
| `space` | mark · `d` cut (marked or current) · `y` yank · `p` paste below cursor |
| `/` | search file names and notes · `n`/`N` next/previous match |
| `u` / `Ctrl-R` | undo / redo |
| `o` | new section · `v` open the file in vim and reload on quit |
| `Tab` | snapshots tab: the git branch graph, scroll with `j`/`k`, `Tab` back |
| `:` | command palette: fuzzy-search every command by name (`wat` finds `watch`/`watch-all`), Tab/Ctrl-n cycles, Enter runs; `bed` and `overlay` there are two-step wizards that insert those records |
| `w` | save · `q`/`Q` quit saving/discarding · `?` help overlay |

`MILKLILY_EDITOR` overrides vim (e.g. `MILKLILY_EDITOR="vim -u NONE"`).

## Title cards (typst)

Templates are `.typ` files in `titles/`; each card names its template and its
text, so one style serves any number of cards. First use creates
`titles/chapter.typ` (a black 4:3 page, centered white text) to copy and
restyle. The contract: the text arrives as `sys.inputs.text`. Rendered PNGs
are cached in `.cache/` by template content + text, so editing the template
re-renders every card that uses it, and reuse costs nothing.

```bash
milklily seq title filme chapter.typ 4 "Capítulo 2"
cp titles/chapter.typ titles/lower.typ   # new style, edit freely
```

## Animated cards (manim)

Same idea, animated: `.py` manim scenes in `anims/`, first use creates
`anims/card.py` (text writes in, holds, fades out). The contract: a Scene
subclass named `Card` that reads `$MILKLILY_TEXT`. milklily renders at the
project's exact frame and fps, measures the animation's length, stores it in
the record, and caches the clip. Renders are slow the first time (the TUI
hands you the terminal so you see manim's progress); cached forever after.
Animated cards are silent by design: the bed plays underneath.

## Overlays

`overlay|file|at|dur|place|note` puts an image on top of the scene directly
above its line: `at` seconds into that scene, for `dur` seconds (`0` = until
the scene ends), clamped to the scene. `place` is a corner plus width percent
(`tl`/`tr`/`bl`/`br`/`c`, e.g. `tr:30` = top-right at 30% of the frame width)
or `full`. PNG transparency is respected. Reorder the scene and its overlay
lines travel with it. Overlays show in both export and review.

In the TUI, run `:overlay` and drag an image file onto the terminal when it
asks for the image. A file outside the project is copied into `images/stills/`
automatically. If your browser drops an image URL instead, paste/drop its
direct `https://…png`/`jpg`/`webp` URL; Milklily downloads a project copy
before adding it, so later renders do not depend on your Downloads folder.
On a Linux Wayland desktop, the quicker route is: copy an image in the
browser, select the narration scene, and press `i`. The PNG is imported into
`images/stills/` and immediately fills that scene. Use `:overlay` only when
you want custom timing or placement.

The file can also be a typst template from `titles/`: then the overlay's
note (tags stripped) is the card's text, rendered on a transparent page.
That's the lower-third/citation workflow: `titles/lower.typ` (created with
the defaults) is a caption block in the bottom-left; use place `full` and
write one line per name/credit:

```bash
milklily seq overlay corte lower.typ 0.5 4 --place full "Fulano, artista"
```

## Colour grading & film grain

Grade a scene as plain text — inline `key=value` in its note
(`sunset saturation=120 warmth=25 grain=20`), or a reusable preset
(`milklily grade set filmic …` + `#grade:filmic` on the note). Or use the
TUI panel: press `c` on a scene for live sliders (`j`/`k` pick, `←`/`→`
adjust, `0` reset one, `r` clear). The panel writes the same tokens back into
the note, so text and TUI are one thing. Parameters: brightness, contrast,
saturation, gamma, warmth, sharpen, grain (luma-only film grain). Applied only
at export, never to source media — fully reversible. Full guide:
[docs/color-grading.md](docs/color-grading.md).

## Nested sequences

`use|other-sequence` splices another sequence in at that point on review and
export, so a long film assembles from chapter sequences edited on their own:
`milklily seq use filme capitulo-1`. Sections inside spliced sequences flow
into `chapters` with correct timestamps.

## Finding the takes: silences

`milklily silences gravacao.wav` lists the spoken stretches of a continuous
recording (everything between pauses of 0.6s+ under -35dB; tune with
`--noise`, `--gap`, `--pad`). `--keep` adds them to selects.txt as numbered
takes, ready for `seq from-selects`; it never changes the recording and is
safe to run again without duplicating the same take.

In `milklily edit`, select a narration audio scene and press `:` then type
`cut silences`. This uses deliberately generous settings: only pauses at least
1.2 seconds that are below -45dB split a take, and 0.4 seconds remains around
each spoken stretch. It creates reviewable selects only — never a destructive
audio edit — so real words are protected even at the cost of leaving short
pauses intact.

## Audio beds

`seq audio` lays a file under the cut from 0:00 by default, mixed below the
timeline's own sound at `--gain` dB (negative sits music under a voice; `0`
suits narration over silent footage). It is cut when the video ends, never
extends the runtime, and several beds stack. Both export and review play
beds. Change the gain with `t` or `+`/`-` in the TUI.

For an image animatic, anchor a cue to the cut instead of calculating seconds:

```bash
milklily seq audio main audio/dialogue/voice.mp3 --gain 0 "narration #at_image_34"
milklily seq audio main audio/music/theme.mp3 --gain -14 "theme #at_scene_37 #duck"
```

The first cue starts at the 34th still image; the second starts at playable
scene 37. Retiming any earlier shot recalculates their start time for both
review and export. Use `#at_52.8` only when the cue must stay at a fixed time.

## review: watch without rendering

```bash
milklily review filme
milklily review filme --from 5     # start at scene 5 (seq show numbering)
```

Instant, full resolution: mpv plays the exact cut through a generated
playlist, with stills held for their duration, cards rendered, voice slices
audible, overlays composited in place, `#mute`/`#NdB` windows applied, beds
mixed at their gain and placement, and the picture letterboxed into the
project frame. In the TUI, `r`/`R` do the same in a separate window. (Only
the export's finishing pass, fades/ducking/loudnorm, is not simulated.)

## export: the real render

```bash
milklily export filme exports/video/filme.mp4
milklily export filme exports/video/rascunho.mp4 --draft
```

One H.264 file, tuned to YouTube's upload recommendations: High profile
4.2, constant frame rate, keyframe every 2s, 2 B-frames, BT.709 flagged,
yuv420p, AAC-LC 48kHz, `+faststart`. Resolution, fps and CRF come from
`milklily.conf`. Export refuses to write into source media folders or over
any source.
In a snapshotted project, every real export automatically commits a snapshot
named after the output file, so any published video maps to its exact cut.

Finishing is automatic: ~15ms audio micro-fades at every join (no clicks or
pops), a fade from and to black on the whole picture, a 1.5s music-bed
fade-out at the end, `#duck` beds compressed under the voice, and a final
loudness normalisation to YouTube's -14 LUFS, so takes recorded on different
days land at the same level.

After exporting, `milklily chapters filme` prints the YouTube chapter list
from your sections, and `milklily frame clip.mp4 1:02 thumb.png` grabs a
full-resolution still for the thumbnail.

## Posting to YouTube

For the complete, copy-paste workflow, see [docs/youtube.md](docs/youtube.md).

`milklily youtube` queues the last render in the shared YouTube pipeline. The
navylily timer posts it privately on the cadence you selected; use
`milklily youtube --now` only for an immediate private upload. It reuses the existing
`navylily-tools/youtube_upload.sh` uploader (override the path with
`MILKLILY_YOUTUBE`); the first run does the Google OAuth flow in a browser.
The `youtube` entry in the TUI command palette (`:`) does the same. Uploads
join the shared navylily queue and state, so the selected cadence applies to
both Milklily renders and other queued videos. Override the destination queue
with `MILKLILY_YOUTUBE_QUEUE` if needed.

### Render → queue → publish

```bash
# Render the finished cut.
milklily export filme exports/video/filme.mp4

# Add it to the shared YouTube queue. The title travels with the render.
milklily youtube --title "Título do vídeo"

# See its position and planned publishing date.
nl-queue

# Control the shared posting cadence whenever you want.
~/projects/navylily-tools/youtube_upload.sh --cadence daily
~/projects/navylily-tools/youtube_upload.sh --cadence weekly
~/projects/navylily-tools/youtube_upload.sh --cadence monthly
```

The scheduled uploader posts one queued video at 18:00 São Paulo time. Weekly
means Sunday; monthly means the first day of the month. Use `--now` only when
you deliberately want to bypass the queue:

```bash
milklily youtube --now --title "Título do vídeo"
```

## Snapshots and versions (git)

```bash
milklily snapshot "primeiro corte"
milklily snapshot list
milklily snapshot restore d77b1c6    # safe: snapshots the current state first
```

Optional. The first `snapshot` turns the project into a git repo whose
`.gitignore` keeps source media folders, exports and caches out, so only the
small text files are versioned. It is a completely normal repo:

```bash
git checkout -b versao-curta      # branch a different cut of the same movie
milklily snapshot "sem a intro"
git checkout main && git merge versao-curta   # line-per-record merges cleanly
git remote add origin … && git push           # collaborate
```

A team shares the repo (instructions) and ships `storyboards/`, `audio/`,
`footage/` and other media folders out of band (drive, rsync); since records
are one per line, two people editing different scenes merge without conflict.
The TUI's `Tab` shows the branch graph.

## Configuration

`milklily.conf` at the project root:

```
name = meu-filme
width = 1440      # 4:3 at 1080p
height = 1080
fps = 30
crf = 18          # libx264 quality, lower is better
```

## On disk

```
milklily.conf      config (above)
README.md           GitHub-facing project map
README.txt          file-manager project map
scripts/            writing, dialogue drafts, shot notes
storyboards/        boards and animatic stills, scanned recursively
images/             cleaned stills, plates and backgrounds
refs/               visual reference and research
audio/              dialogue, narration, music, sfx, ambience
fxs/                overlays, mattes and texture plates
footage/            raw clips and legacy catch-all media
titles/             typst card templates (.typ), versionable
anims/              manim card templates (.py), versionable
grades/             colour-grade presets, versionable
markers.txt         file|seconds|note
selects.txt         file|in|out|note
notes.txt           file|time|text
sequences/*.txt     the cuts (records above)
exports/            rendered movies, storyboard books and frames
.cache/             rendered cards/review files, regenerable, gitignored
```

Everything is plain text with `#` comments and blank lines ignored, so `cat`,
`grep`, `sed`, vim and git all work directly on it. The source media folders
are intentionally ignored by `milklily snapshot`; the sequence text stores
readable paths back to them.

## Ideas parked for later

See `docs/color-grading-idea.md` (still-frame grading with text presets, and
a command palette for the TUI).
