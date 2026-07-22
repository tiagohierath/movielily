# Colour grading & film grain

movielily grades footage the way it does everything else: as **plain text,
applied only at export, never touching the source**. A grade is a set of
friendly `key=value` parameters. That makes it fully reversible (delete the
text), reproducible (same text, same pixels), diffable, and git-versionable.

## The parameters

Each has a neutral value that means "do nothing". You speak in friendly
numbers; the ffmpeg filters are generated, never typed.

| parameter | range | neutral | effect |
|---|---|---|---|
| `brightness` (alias `exposure`) | -100 … 100 | 0 | darker / brighter |
| `contrast` | 0 … 200 | 100 | flat / punchy |
| `temperature` (alias `warmth`) | -100 … 100 | 0 | cool-blue / warm-orange |
| `tint` | -100 … 100 | 0 | green / magenta |
| `highlights` | -100 … 100 | 0 | recover / brighten the bright end |
| `shadows` | -100 … 100 | 0 | deepen / lift the dark end |
| `saturation` | 0 … 200 | 100 | grey / vivid |
| `vibrance` | -100 … 100 | 0 | smart saturation (spares already-saturated colours) |
| `blackpoint` | -100 … 100 | 0 | crush / lift the blacks |
| `whitepoint` | -100 … 100 | 0 | dim / (clips at pure) whites |
| `grain` | 0 … 100 | 0 | clean / heavy film grain (luma only, temporal) |
| `bloom` (alias `glow`) | 0 … 100 | 0 | glow bleeding from the highlights |
| `sharpen` | 0 … 100 | 0 | none / crisp |
| `vignette` | 0 … 100 | 0 | darken toward the corners |
| `fade` (alias `lift`) | 0 … 100 | 0 | lift blacks to grey (matte look) |

Run `movielily grade params` for this list at any time. Common short aliases
work too (`sat`, `temp`, `hi`, `sh`, `vig`, `bp`, `wp`, `noise` = grain).

Under the hood these compile to one ffmpeg chain: `eq` (exposure/contrast/
saturation), `colortemperature`, `colorbalance` (tint), a `curves` built from
the tonal knobs (highlights/shadows/black/white/fade), `vibrance`, `unsharp`,
`vignette`, luma `noise`, and — for bloom — a `split`/`gblur`/`blend` glow
sub-graph. A neutral grade adds no filter at all.

## Two ways to grade a scene — both plain text

**Inline in a scene's note.** Any `key=value` grade tokens in a note are
pulled out and applied to that scene; the rest of the note (and its `#tags`)
is untouched:

```
video|sunset.mp4|10|18|golden hour saturation=120 warmth=25 grain=20
```

**A named preset**, for a look reused across shots. Presets live in
`grades/*.grade` (one `key=value` per line). Tag a scene's note `#grade:name`
to apply it; inline tokens on the same scene override the preset:

```bash
movielily grade set filmic saturation=115 contrast=108 warmth=15 grain=20
# then, on a scene's note:  moody shot #grade:filmic grain=40
```

`grade list` / `grade show <name>` inspect them.

## Two ways to edit — text or TUI

The **TUI grade panel** (`c` on a scene in `movielily edit`, or `:grade`) is a
live slider view of the same parameters: `j`/`k` pick a parameter, `←`/`→`
(or `h`/`l`) adjust it, `0` resets one, `r` clears the whole grade. The panel
shows the exact `key=value` text at the bottom as you go — the panel and the
text are two views of the one grade. Whatever you set is written straight back
into the scene's note, so it round-trips with hand-editing and presets.

## How it renders

At export, each visual scene's grade compiles to one ffmpeg filter chain, run
after the frame is fitted to the 4:3 canvas and before the final pixel format:
tone/colour (`eq`), white balance (`colortemperature`), detail (`unsharp`),
then grain (`noise`, luma-only and temporal so it reads as film grain and
leaves colour intact) last. A neutral grade adds no filter at all, so ungraded
exports are byte-for-byte what they always were.

Footage is only ever read. The grade is instructions; the export is a new
file. Nothing is destructive.

## Roadmap (not built yet)

The ideas parked in `color-grading-idea.md` that go beyond this base:

- **Still-frame preview**: extract a frame, apply the chain, show the PNG, so
  a grade is dialed in on a still before rendering (colourists work on stills).
- **Shot matching**: pick a frame from clip A and one from clip B, estimate
  the exposure/warmth/saturation offset, and write it as a grade — more useful
  for consistency than a hundred manual controls.
- **Curves / LUTs / scopes**: only after the above; deliberately last, they
  push toward being a grading application rather than a fast essay tool.
