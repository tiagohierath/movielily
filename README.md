# movielily

movielily is a notebook-style video editor for the terminal: watch footage,
log the good moments, assemble a plain-text cut, watch it instantly without
rendering, and export a YouTube-ready 4:3 file with ffmpeg.

**The one rule:** your footage is never modified, moved, or renamed. mpv and
ffmpeg only ever *read* it. Every decision is a line of text; export always
writes a brand-new file.

Times are always **seconds**: `90`, `90s`, and `1:30` all mean the same thing.

Needs **mpv** and **ffmpeg** on PATH. Title cards additionally use **typst**,
animated cards use **manim**; both are optional, and on a nix machine they are
fetched ephemerally when missing.

---

## Command reference

| command | what it does |
|---|---|
| `movielily init [dir] [--footage <src>]` | create a project (optionally copy media in) |
| `movielily watch <clip>` | play in mpv and log: `m` marker, `i`/`o` in/out, `Enter` select (works on audio files too) |
| `movielily marker add <clip> <t> [note]` · `marker list` | markers by hand |
| `movielily select add <clip> <in> <out> [note]` · `select list` | selects by hand |
| `movielily note add [--clip c] [--time t] <text>` · `note list` | free notes, timestamped if you want |
| `movielily search <term>` | search markers, selects and notes |
| `movielily tag [name]` | list #tags, or everything tagged #name |
| `movielily seq from-selects <seq> [--force]` | seed a sequence from all selects |
| `movielily seq video <seq> <file> <in> <out> [note]` | append a clip (or a slice of a voice recording) |
| `movielily seq image <seq> <img> <dur> [note]` | append a still (`#cover` in the note = fill the frame) |
| `movielily seq title <seq> <template> <dur> <text>` | append a typst title card |
| `movielily seq anim <seq> <template> <text>` | append a manim animated card (renders now, measures length) |
| `movielily seq overlay <seq> <img|tpl.typ> <at> <dur> [--place br:33] [note]` | image, or a typst template whose text is the note (lower-thirds), on top of the LAST scene |
| `movielily seq use <seq> <other>` | splice another sequence in (chapters edited separately) |
| `movielily silences <audio> [--keep]` | find the spoken stretches of a narration take; --keep turns them into selects |
| `movielily seq audio <seq> <file> [--gain -12] [note]` | music/narration bed under the whole cut |
| `movielily seq show <seq>` · `seq list` | inspect sequences |
| `movielily edit [seq]` | the interactive editor (see keys below) |
| `movielily review <seq> [--from N]` | watch the cut in mpv, simulated export, no render |
| `movielily export <seq> <out.mp4> [--draft]` | render the real file (auto-snapshots; --draft = half-res quick look) |
| `movielily chapters <seq>` | YouTube chapters from your sections, ready for the description |
| `movielily frame <clip> <t> <out.png>` | full-resolution frame grab (thumbnails) |
| `movielily youtube [video] [--title T]` | post the last render (or a given file) to YouTube, private, via your uploader script |
| `movielily snapshot [message]` | commit the instructions to git (creates the repo on first use) |
| `movielily snapshot list` · `snapshot restore <id>` | see versions · roll back (safely: it snapshots first) |
| `movielily version` | version info |

Aliases: `m`=marker, `sel`=select, `n`=note, `s`=seq, `snap`=snapshot.

---

## The sequence file

A sequence is the movie: an ordered list of records, one per line, in
`sequences/<name>.txt`. Edit it in the TUI or in vim, both stay in sync.

```
section|Abertura                                  organisational folder, no runtime
title|chapter.typ|4|Capítulo 1                    typst card, 4s on screen
video|clip001.mp4|72.3|85.1|the punchline #best   clip trimmed to in..out
overlay|ref.png|2|5|tr:30|the reference           rides the scene ABOVE it
video|voz.wav|0|35|narração                       voice slice: black canvas + sound
image|photo.jpg|5|opening #cover                  still; #cover crops to fill
anim|card.py|3.8|Fim                              manim card (length measured)
audio|song.mp3|-12|music bed                      under the whole cut, cut at the end
```

Notes carry `#tags` anywhere; `search` and `tag` find them and the TUI
colours them. Three tags are also switches:

- `#cover` on a visual item: fill the frame (crop) instead of letterboxing;
- `#mute` on a clip: silence its own sound (b-roll riding over narration);
- `#-6db` / `#+3db` on a clip: adjust just that clip's level;
- `#clean` on a clip/voice slice: highpass + gentle denoise for rough recordings;
- `#duck` on a bed: sidechain-duck the music under the timeline's voice;
- `#at_S` `#from_S` `#for_S` on a bed: enter the film at second S, skip S
  seconds into the source, play for S seconds (music per section instead of
  wall to wall).

Clips whose file has no audio stream at all (some screen captures) export
with silence automatically instead of failing.

## Workflows

### Voice-first (narrated videos)

Record your narration anywhere, pauses and retakes included, then:

```bash
cp ~/gravacao.wav footage/
movielily silences gravacao.wav --keep   # the spoken stretches become selects
movielily seq from-selects aula
movielily edit aula                 # prune misfires (d), split (s), cards (T)
movielily seq audio aula musica.mp3 --gain -14 "trilha #duck"
movielily export aula aula.mp4
```

(`movielily watch gravacao.wav` is the manual alternative: listen and mark
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
movielily edit filme        # or just `movielily edit` with a single sequence
```

Left: the cut, one scene per line, colour-coded with an icon per kind
(▶ clip · ∿ voice · ▦ image · ▣ title card · ✦ animated card · ♪ bed ·
◱ overlay). Right: previews (first/last frame, the rendered card, or the
voice waveform, in kitty/Ghostty/WezTerm) plus details, including where the
scene starts in the finished movie.

| key | action |
|---|---|
| `j`/`k`, arrows | move · `J`/`K` reorder · `g`/`G` top/bottom · `[`/`]` prev/next section |
| `s` | split the clip in two at a point picked in mpv |
| `<`/`>` | nudge the clip's in point ±0.5s (`+`/`-` does the out point) |
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

`MOVIELILY_EDITOR` overrides vim (e.g. `MOVIELILY_EDITOR="vim -u NONE"`).

## Title cards (typst)

Templates are `.typ` files in `titles/`; each card names its template and its
text, so one style serves any number of cards. First use creates
`titles/chapter.typ` (a black 4:3 page, centered white text) to copy and
restyle. The contract: the text arrives as `sys.inputs.text`. Rendered PNGs
are cached in `.cache/` by template content + text, so editing the template
re-renders every card that uses it, and reuse costs nothing.

```bash
movielily seq title filme chapter.typ 4 "Capítulo 2"
cp titles/chapter.typ titles/lower.typ   # new style, edit freely
```

## Animated cards (manim)

Same idea, animated: `.py` manim scenes in `anims/`, first use creates
`anims/card.py` (text writes in, holds, fades out). The contract: a Scene
subclass named `Card` that reads `$MOVIELILY_TEXT`. movielily renders at the
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

The file can also be a typst template from `titles/`: then the overlay's
note (tags stripped) is the card's text, rendered on a transparent page.
That's the lower-third/citation workflow: `titles/lower.typ` (created with
the defaults) is a caption block in the bottom-left; use place `full` and
write one line per name/credit:

```bash
movielily seq overlay corte lower.typ 0.5 4 --place full "Fulano, artista"
```

## Nested sequences

`use|other-sequence` splices another sequence in at that point on review and
export, so a long film assembles from chapter sequences edited on their own:
`movielily seq use filme capitulo-1`. Sections inside spliced sequences flow
into `chapters` with correct timestamps.

## Finding the takes: silences

`movielily silences gravacao.wav` lists the spoken stretches of a continuous
recording (everything between pauses of 0.6s+ under -35dB; tune with
`--noise`, `--gap`, `--pad`). `--keep` appends them to selects.txt as
numbered takes, ready for `seq from-selects`.

## Audio beds

`seq audio` lays a file under the whole cut from 0:00, mixed below the
timeline's own sound at `--gain` dB (negative sits music under a voice; `0`
suits narration over silent footage). It is cut when the video ends, never
extends the runtime, and several beds stack. Both export and review play
beds. Change the gain with `t` or `+`/`-` in the TUI.

## review: watch without rendering

```bash
movielily review filme
movielily review filme --from 5     # start at scene 5 (seq show numbering)
```

Instant, full resolution: mpv plays the exact cut through a generated
playlist, with stills held for their duration, cards rendered, voice slices
audible, overlays composited in place, `#mute`/`#NdB` windows applied, beds
mixed at their gain and placement, and the picture letterboxed into the
project frame. In the TUI, `r`/`R` do the same in a separate window. (Only
the export's finishing pass, fades/ducking/loudnorm, is not simulated.)

## export: the real render

```bash
movielily export filme filme.mp4
movielily export filme rascunho.mp4 --draft   # half-res, fast: a quick look
```

One H.264 file, tuned to YouTube's upload recommendations: High profile
4.2, constant frame rate, keyframe every 2s, 2 B-frames, BT.709 flagged,
yuv420p, AAC-LC 48kHz, `+faststart`. Resolution, fps and CRF come from
`movielily.conf`. Export refuses to write into `footage/` or over any source.
In a snapshotted project, every real export automatically commits a snapshot
named after the output file, so any published video maps to its exact cut.

Finishing is automatic: ~15ms audio micro-fades at every join (no clicks or
pops), a fade from and to black on the whole picture, a 1.5s music-bed
fade-out at the end, `#duck` beds compressed under the voice, and a final
loudness normalisation to YouTube's -14 LUFS, so takes recorded on different
days land at the same level.

After exporting, `movielily chapters filme` prints the YouTube chapter list
from your sections, and `movielily frame clip.mp4 1:02 thumb.png` grabs a
full-resolution still for the thumbnail.

## Posting to YouTube

`movielily youtube` uploads the last render as a PRIVATE video (set title and
thumbnail in YouTube Studio, publish when ready). It reuses the existing
`navylily-tools/youtube_upload.sh` uploader (override the path with
`MOVIELILY_YOUTUBE`); the first run does the Google OAuth flow in a browser.
The `youtube` entry in the TUI command palette (`:`) does the same. Uploads
track their own state under the project's `.cache/`, separate from the
navylily daily timer.

## Snapshots and versions (git)

```bash
movielily snapshot "primeiro corte"
movielily snapshot list
movielily snapshot restore d77b1c6    # safe: snapshots the current state first
```

Optional. The first `snapshot` turns the project into a git repo whose
`.gitignore` keeps footage, exports and caches out, so only the small text
files are versioned. It is a completely normal repo:

```bash
git checkout -b versao-curta      # branch a different cut of the same movie
movielily snapshot "sem a intro"
git checkout main && git merge versao-curta   # line-per-record merges cleanly
git remote add origin … && git push           # collaborate
```

A team shares the repo (instructions) and ships `footage/` out of band
(drive, rsync); since records are one per line, two people editing different
scenes merge without conflict. The TUI's `Tab` shows the branch graph.

## Configuration

`movielily.conf` at the project root:

```
name = meu-filme
width = 1440      # 4:3 at 1080p
height = 1080
fps = 30
crf = 18          # libx264 quality, lower is better
```

## On disk

```
movielily.conf      config (above)
footage/            your media, read-only: mp4 · jpg/png · wav/mp3/m4a/flac/ogg
titles/             typst card templates (.typ)
anims/              manim card templates (.py)
markers.txt         file|seconds|note
selects.txt         file|in|out|note
notes.txt           file|time|text
sequences/*.txt     the cuts (records above)
.cache/             rendered cards, regenerable, gitignored
```

Everything is plain text with `#` comments and blank lines ignored, so `cat`,
`grep`, `sed`, vim and git all work directly on it.

## Ideas parked for later

See `docs/color-grading-idea.md` (still-frame grading with text presets, and
a command palette for the TUI).
