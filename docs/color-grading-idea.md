# Color grading (idea, saved for later)

Not built yet. The feasible version, saved from a conversation on 2026-07-22:

Yes, color grading is very doable if you redefine what "color grading" means.

What's hard is building a DaVinci Resolve-style grading suite:

- scopes
- nodes
- masks
- tracking
- secondaries
- curves
- qualifiers
- GPU processing

That's years of work.

The feasible version is closer to:

```
Exposure   +0.3
Contrast   +0.2
Saturation -0.1
Warmth     +0.1
Bloom      +0.05
Grain      +0.03
```

Preview on a still frame. Press Apply. Render with ffmpeg.

## Like a photo editor

```
Clip: interview.mp4

Frame: 00:01:23

Exposure  [====|===]
Contrast  [===|====]
Saturation[==|=====]
```

Every adjustment:

1. Extract frame
2. Run ffmpeg filter chain
3. Update preview image

No video playback involved. Just a PNG.

Most grading decisions are made on stills anyway. Professional colorists use
moving footage eventually, but they spend a surprising amount of time staring
at single frames.

## Grades as text (fits movielily perfectly)

```
grade "documentary"

brightness=0.05
contrast=1.1
saturation=0.9
grain=0.03
```

Then:

```
clip interview.mp4
grade documentary
```

Grades become reusable presets.

## Don't expose ffmpeg filters directly

```
Grade
├── Exposure
├── Contrast
├── Saturation
├── Temperature
├── Tint
├── Grain
├── Bloom
└── Sharpen
```

Internally it generates the filter graph. The user shouldn't need to know
ffmpeg syntax.

## Shot matching (the really interesting part)

A.mp4 and B.mp4 look different. Pick a frame from A, then a frame from B. The
tool estimates:

```
Exposure +0.4
Warmth -0.2
Saturation +0.1
```

and applies it. Probably more useful than a hundred grading controls.

## Priorities, if built

1. Still-frame grading preview
2. Saved grade presets
3. Copy grade from clip A to clip B
4. Grain
5. Bloom

Only after that: curves, scopes, LUTs.

For an essay/documentary workflow, 80% of the visual improvement comes from a
handful of adjustments and consistency between shots, not Hollywood-level
grading tools. Terminal-first "pick frame, tweak sliders, save preset" gets
surprisingly far without becoming a color-grading application.

---

# Command palette (idea, same bucket)

Instead of aliases:

```
:
```

Then fuzzy search:

```
thumb
```

shows:

```
generate-thumbnails
regenerate-thumbnails
clear-thumbnails
```

## Nouns over verbs

From software design:

Bad:

```
:gen-thumb
:rndr
:prv
```

Good:

```
thumbnail
render
preview
```

You type them rarely enough that saving 3 characters doesn't matter.
