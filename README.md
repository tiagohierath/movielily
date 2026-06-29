# movielily

A minimal, notebook-style video editor — a small command-line companion to
**mpv** and **ffmpeg** for making short videos fast.

The workflow is **watch → log → select → assemble → export**, not
*import → timeline → effects → render*. It feels closer to a notebook and a
screening room than to Premiere or Resolve.

**One invariant, above all:**

```
source footage  +  instructions  =  export
```

movielily **never modifies, moves or rewrites your footage**. mpv only plays it,
ffmpeg only reads it; every edit lives in plain-text "instructions" you can
read, `grep` and hand-edit. Export always produces a *new* file (and refuses to
write anywhere inside `footage/`).

## Quick start

```sh
nix develop                 # go + mpv + ffmpeg dev shell (or: direnv allow)
just build                  # -> ./bin/movielily

cd ~/my-film
movielily init
cp ~/clips/*.mp4 footage/

movielily watch clip001.mp4         # m=marker  i/o=in/out  Enter=select
movielily search reaction
movielily seq from-selects roughcut
movielily seq image roughcut title.png 4 "opening title #intro"
movielily seq show roughcut
movielily review roughcut           # play instantly in mpv (no render)
movielily export roughcut out.mp4   # single 4:3 H264 file, ready for YouTube
```

## A project on disk

```
movielily.conf     # export target: 4:3, fps, quality (crf)
footage/           # your .mp4 / .jpg / .png  (read-only, never touched)
markers.txt        # file|seconds|note
selects.txt        # file|in|out|note
notes.txt          # file|time|text
sequences/         # video|file|in|out|note   and   image|file|duration|note
```

Tags are just `#hashtags` inside any note — `movielily tag` lists them,
`movielily tag funny` shows everything tagged `#funny`.

## Commands

| | |
|---|---|
| `init [dir]` | create a project |
| `watch <clip>` | play in mpv; `m` marker, `i`/`o` in/out, `Enter` save select |
| `marker add/list` · `select add/list` · `note add/list` | log by hand |
| `search <term>` | search markers, selects and notes |
| `tag [name]` | list tags, or show everything tagged `#name` |
| `seq video/image/show/list/from-selects` | assemble sequences |
| `review <seq>` | watch a sequence instantly via mpv EDL (no render) |
| `export <seq> <out.mp4>` | render with ffmpeg |

Times are always stored as **seconds** (never frame numbers); you can *type*
`90`, `90s` or `1:30`.

## Scope (v1)

MP4/H.264 footage, JPG/PNG images, 4:3, SDR. No timeline, transitions, effects,
filters, colour, database or GPU rendering — by design. If a feature doesn't
help you *watch, note, find, select or assemble*, it doesn't belong here.

Built with Go, [cobra](https://github.com/spf13/cobra), mpv and ffmpeg.
