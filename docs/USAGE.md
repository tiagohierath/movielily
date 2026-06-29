# Using movielily

movielily is a notebook-style video editor: you **watch** footage, **log** the
good moments, gather them into **selects**, **assemble** a sequence, **review**
it instantly, and **export** a finished file. Everything you decide lives in
plain-text files you can read, `grep`, and hand-edit.

**The one rule:** your footage is never modified, moved, or renamed. mpv and
ffmpeg only ever *read* it. Every edit is an instruction in a text file; export
always writes a brand-new file.

Times are always **seconds**. You can type `90`, `90s`, or `1:30` — they all
mean the same thing.

---

## The workflow at a glance

```bash
movielily init my-film              # 1. make a project
cd my-film
cp ~/clips/*.mp4 footage/           # 2. put footage in footage/
movielily watch clip001.mp4         # 3. watch & log (m / i / o / Enter)
movielily search reaction           # 4. find what you logged
movielily seq from-selects roughcut # 5. turn selects into a sequence
movielily edit roughcut             # 6. arrange it (TUI; or edit the .txt in vim)
movielily review roughcut           # 7. watch it instantly in mpv (no render)
movielily export roughcut out.mp4   # 8. render the final file
```

---

## 1. Start a project — `init`

```bash
movielily init my-film              # creates my-film/ with footage/ inside
movielily init                      # turn the current folder into a project
movielily init my-film --footage ~/clips   # also copy media in (originals kept)
```

A project is any folder containing `movielily.conf`. Commands work from the
project root or any subfolder. `movielily.conf` holds the export target
(resolution, fps, quality).

## 2. Watch and log — `watch`

```bash
movielily watch clip001.mp4
```

Opens the clip in mpv. While it plays, these keys log to the project:

| key | action |
|---|---|
| `m` | drop a **marker** at the current time |
| `i` | set the **IN** point |
| `o` | set the **OUT** point |
| `Enter` | save a **select** from IN..OUT |
| `q` | quit |

Markers go to `markers.txt`, selects to `selects.txt`.

## 3. Log or review by hand — `marker`, `select`, `note`

You don't have to use mpv; you can log directly:

```bash
movielily marker add clip001.mp4 72.3 "funny reaction #funny"
movielily select add clip001.mp4 72.3 85.1 "great line #best"
movielily note add "remember to colour-grade the intro #todo"
movielily note add clip001.mp4 90 "use this for the cold open"

movielily marker list
movielily select list
movielily note list
```

(`m`, `sel`, `n` are short aliases for `marker`, `select`, `note`.)

## 4. Find things — `search` and `tag`

```bash
movielily search reaction           # search markers, selects and notes
movielily tag                       # list every #tag and how often it appears
movielily tag funny                 # show everything tagged #funny
```

Tags are just `#hashtags` written inside any note — no special syntax.

## 5. Build a sequence — `seq`

A **sequence** is an edit-decision list: an ordered list of clips and images,
one per line, in `sequences/<name>.txt`.

```bash
movielily seq from-selects roughcut          # seed a sequence from all selects
movielily seq video roughcut clip001.mp4 72.3 85.1 "the punchline"
movielily seq image roughcut title.png 4 "opening title #intro"
movielily seq show roughcut                   # list its items + total runtime
movielily seq list                            # list all sequences
```

(`s` is the short alias, e.g. `movielily s show roughcut`.)

## 6. Arrange it — `edit` (the TUI)

```bash
movielily edit roughcut       # or just `movielily edit` if there's only one
```

An interactive terminal editor — a more visible view of the sequence file. If
the sequence doesn't exist yet, it's seeded from your selects.

| key | action |
|---|---|
| `j` / `k` | move down / up |
| `J` / `K` | reorder the scene down / up |
| `g` / `G` | jump to top / bottom |
| `o` | add a **section** ("folder", e.g. *Scene 1*) and type its title |
| `e` | edit the scene's note (add `#tags` here) |
| `v` | open the sequence in **vim**, then reload on quit |
| `space` | mark a scene (mark several, then `d` to delete them all) |
| `d` | delete the marked scenes, or the one under the cursor |
| `u` | undo |
| `w` | save |
| `q` / `Q` | quit (save / discard) |

Sections are organisational only — they group scenes and add nothing to the
export. In a kitty-style terminal (kitty, Ghostty, WezTerm) the right pane shows
the selected scene's first and last frame.

**Seamless with the text file:** the TUI just reads and writes
`sequences/<name>.txt`. Press `v` to jump into vim on that exact file; when you
quit vim the TUI reloads it. You can also edit the file directly in vim with the
TUI closed — next time you open `edit` it picks up your changes. Set
`MOVIELILY_EDITOR` to use a different editor (e.g. `MOVIELILY_EDITOR="vim -u NONE"`).

## 7. Preview — `review`

```bash
movielily review roughcut
```

Plays the whole sequence in mpv using an EDL — **no rendering**, so it's
instant. Still images are skipped in review (export to see them).

## 8. Render — `export`

```bash
movielily export roughcut out.mp4
```

Renders the sequence to a single H.264 file with ffmpeg, using the resolution /
fps / quality from `movielily.conf`. It refuses to write into `footage/` or over
any source file — your footage stays untouched.

---

## What's on disk

```
my-film/
  movielily.conf     # export target: resolution, fps, quality (crf)
  footage/           # your .mp4 / .jpg / .png  (read-only, never touched)
  markers.txt        # file|seconds|note
  selects.txt        # file|in|out|note
  notes.txt          # file|time|text
  sequences/
    roughcut.txt     # one record per line:
                     #   video|file|in|out|note
                     #   image|file|duration|note
                     #   section|title
```

Every file is plain text, one record per line, with `#` comments and blank
lines ignored — so `cat`, `grep`, `sed`, and vim all work on them directly.
