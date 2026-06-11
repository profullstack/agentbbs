# Video Calls Addendum (PairUX over SSH)

Join a PairUX video call from a terminal: the platform subscribes to the
call's LiveKit room and renders participant video as **truecolor ASCII** —
each character cell is two pixels via the upper-half block (▀), the same
technique doom-ascii uses.

## SSH routes

| Command | What happens |
|---|---|
| `ssh video-<code>@profullstack.com` | join call `<code>` directly |
| `ssh video@profullstack.com` | prompted for a code |

**Codes are minted by PairUX.** The SSH surface never creates calls — to
start one you must already have a code (create the call in PairUX first).

## Pipeline

```
PairUX / LiveKit room
   └─ VP8 RTP track  ── PLI keyframe requests ──┐
        └─ ivfwriter remux → ffmpeg (decode + scale) → RGB24 frames
             └─ half-block ANSI (internal/ascii) → bubbletea → SSH PTY
```

- Subscriber-only: the terminal viewer publishes nothing.
- A PLI is sent on subscribe and every 2s — without it the SFU never starts
  forwarding video to a fresh subscriber, and the periodic refresh bounds
  packet-loss artifacts.
- Audio is not rendered (it's a terminal); v2 could downlink Opus → local
  audio out for desktop SSH clients.
- Frame size locks to the terminal geometry at join (`scale=` in ffmpeg);
  ~10–15 fps at typical terminal sizes costs a few hundred KB/s of SSH
  bandwidth.

## Configuration

Shares PairUX's env shape:

| Var | Meaning |
|---|---|
| `AGENTBBS_LIVEKIT_URL` (or `LIVEKIT_URL` / `NEXT_PUBLIC_LIVEKIT_URL`) | LiveKit ws URL |
| `AGENTBBS_LIVEKIT_KEY` (or `LIVEKIT_API_KEY`) | API key |
| `AGENTBBS_LIVEKIT_SECRET` (or `LIVEKIT_API_SECRET`) | API secret |
| `AGENTBBS_VIDEO_DEBUG` | dump the received IVF stream to this path |

Unconfigured hosts refuse with a clear message.

## Dev testing

```bash
docker run -d -p 7880:7880 -p 7881:7881 -p 7882:7882/udp \
  livekit/livekit-server --dev --bind 0.0.0.0   # devkey/secret
ffmpeg -f lavfi -i testsrc=size=320x240:rate=15 -t 900 \
  -c:v libvpx -b:v 400k -g 15 -y test.ivf
go run ./cmd/lkpublish -room demo1 -fps 15 -file test.ivf
ssh -p 2222 video-demo1@localhost
```

Note: `lkpublish` passes an explicit frame duration — lksdk's IVF replay
pacing can't be trusted from the file timebase alone (we measured 1fps from
a 15fps file without it). Real PairUX publishers are browsers, which pace
correctly on their own.
