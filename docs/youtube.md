# YouTube pipeline

Movielily makes the video. Navylily’s uploader keeps a shared queue and posts
one finished video on the cadence you choose.

## The normal workflow

```bash
# 1. Render a finished video.
movielily export filme exports/video/filme.mp4

# 2. Queue the last render for YouTube.
movielily youtube --title "Título do vídeo"

# 3. Check what is waiting and when it will post.
nl-queue
```

`movielily youtube` is safe to run again for the same render: it recognizes
the same video bytes and does not add another copy to the queue.

## Choose how often to post

Run one of these at any time. The choice is shared by Movielily and the rest
of your YouTube queue.

```bash
~/projects/navylily-tools/youtube_upload.sh --cadence daily
~/projects/navylily-tools/youtube_upload.sh --cadence weekly
~/projects/navylily-tools/youtube_upload.sh --cadence monthly
```

The uploader runs at 18:00 São Paulo time:

- `daily` posts every day.
- `weekly` posts on Sunday.
- `monthly` posts on the first day of the month.

## Upload right now

Normally, use the queue. If you specifically want a private upload immediately,
use:

```bash
movielily youtube --now --title "Título do vídeo"
```

This bypasses the shared cadence, so reserve it for deliberate one-off uploads.

## One-time setup

Install the scheduled uploader once:

```bash
cd ~/projects/navylily-tools
./youtube_upload.sh --authorize
./install_timer.sh
```

`movielily youtube` expects the uploader at
`~/projects/navylily-tools/youtube_upload.sh`. If it lives elsewhere, set
`MOVIELILY_YOUTUBE` to that script. Set `MOVIELILY_YOUTUBE_QUEUE` only if you
want a non-default shared queue directory.
