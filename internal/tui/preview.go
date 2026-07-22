package tui

import (
	"fmt"
	"hash/fnv"
	"movielily/internal/ffmpeg"
	"movielily/internal/manim"
	"movielily/internal/model"
	"movielily/internal/typst"
	"path/filepath"
)

// ---- preview (async) ------------------------------------------------------

func (e *editor) onSceneChange() {
	e.drawRight()
	if e.kitty {
		kittyDeleteAll(e.out)
	}
	e.requestPreview()
}

func (e *editor) requestPreview() {
	e.gen++
	e.curFirst, e.curLast = "", ""
	if !e.kitty || len(e.items) == 0 {
		return
	}
	it := e.items[e.cursor]
	if it.IsSection() || it.IsAudio() || it.Kind == model.KindUse {
		return
	}
	req := previewReq{gen: e.gen}
	switch {
	case it.Kind == model.KindTitle:
		// Rendered (or fetched from cache) by the preview goroutine.
		req.isImage = true
		req.titleTpl, req.titleText = it.File, it.Note
	case it.Kind == model.KindAnim:
		// Only preview an ALREADY rendered card; a cursor movement must
		// never kick off a heavy manim render in the background.
		clip, ok := manim.Cached(e.p, it.File, it.Note)
		if !ok {
			return
		}
		req.firstSrc, req.firstAt = clip, 0
	case it.Kind == model.KindOverlay:
		abs, err := e.p.ResolveFootage(it.File)
		if err != nil {
			return
		}
		req.isImage = true
		req.firstSrc = abs
	case it.Kind == model.KindImage:
		abs, err := e.p.ResolveFootage(it.File)
		if err != nil {
			return
		}
		req.isImage = true
		req.firstSrc = abs
	case model.IsAudioFile(it.File):
		abs, err := e.p.ResolveFootage(it.File)
		if err != nil {
			return
		}
		req.wave = true
		req.firstSrc, req.waveIn, req.waveOut = abs, it.In, it.Out
	default:
		abs, err := e.p.ResolveFootage(it.File)
		if err != nil {
			return
		}
		// Just the start frame (the clip's IN point) — the centre pane shows
		// one image.
		req.firstSrc, req.firstAt = abs, it.In
	}
	select { // drop a stale pending request
	case <-e.reqCh:
	default:
	}
	e.reqCh <- req
}

func (e *editor) previewLoop() {
	for req := range e.reqCh {
		// Coalesce: only render the most recent request.
	drain:
		for {
			select {
			case nr := <-e.reqCh:
				req = nr
			default:
				break drain
			}
		}
		res := previewRes{gen: req.gen}
		switch {
		case req.titleTpl != "":
			if png, err := typst.Render(e.p, req.titleTpl, req.titleText); err == nil {
				res.first = e.thumb(png, 0, true)
			}
		case req.wave:
			res.first = e.waveThumb(req.firstSrc, req.waveIn, req.waveOut)
		default:
			if req.firstSrc != "" {
				res.first = e.thumb(req.firstSrc, req.firstAt, req.isImage)
			}
			if req.lastSrc != "" {
				res.last = e.thumb(req.lastSrc, req.lastAt, req.isImage)
			}
		}
		e.resCh <- res
	}
}

func (e *editor) thumb(src string, at float64, isImage bool) string {
	key := fmt.Sprintf("%s@%s|%v", src, model.FormatSeconds(at), isImage)
	if p, ok := e.cache[key]; ok {
		return p
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	out := filepath.Join(e.tmpDir, fmt.Sprintf("%08x.png", h.Sum32()))
	if err := ffmpeg.Thumbnail(src, at, isImage, out); err != nil {
		return ""
	}
	e.cache[key] = out
	return out
}

func (e *editor) waveThumb(src string, in, out float64) string {
	key := fmt.Sprintf("wave|%s@%s-%s", src, model.FormatSeconds(in), model.FormatSeconds(out))
	if p, ok := e.cache[key]; ok {
		return p
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	png := filepath.Join(e.tmpDir, fmt.Sprintf("%08x.png", h.Sum32()))
	if err := ffmpeg.Waveform(src, in, out, png); err != nil {
		return ""
	}
	e.cache[key] = png
	return png
}
