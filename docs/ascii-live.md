# PRD: ASCII Live (revised)

**Feature:** ASCII Live — watch live video sources as terminal-native ASCII over SSH and browser-terminal.
**Primary host:** AgentBBS (Go).
**Status:** Revised draft (supersedes the standalone "@logicsrc/plugin-ascii-live" PRD).
**Owner:** Profullstack / LogicSRC.

> **Why this revision exists.** The original PRD specified a single Node.js/TypeScript
> package (`@logicsrc/plugin-ascii-live`) hosted inside AgentBBS, with a fresh
> char-ramp ASCII renderer and PairUX support as a future phase. Grounding that
> against the real repos found two structural problems:
>
> 1. **AgentBBS is Go 1.26** (`go.mod`) with a Go plugin interface
>    (`internal/plugin/plugin.go`). It cannot load a TS package. The TS
>    `PluginDefinition` shape (`logicsrc/packages/plugin-core`) is a *different*
>    plugin system — the logicsrc web/Hono/SDK world.
> 2. **The core capability already exists in AgentBBS.** `internal/ascii/ascii.go`
>    renders RGB24 → truecolor half-block (▀) ANSI, and `internal/calls/` already
>    joins a PairUX LiveKit call and renders it as ASCII over SSH (shipped/verified
>    2026-06-11, "128k truecolor cells streamed"). The original PRD's char-ramp
>    renderer is a visual downgrade from this, and its "PairUX = future phase" is
>    largely already done.
>
> This PRD refits the product as **two components** and scopes the MVP to the work
> that is genuinely new.

---

## 1. Architecture: two components

ASCII Live is **not** one package. It is:

| Component | Repo / stack | Responsibility |
|---|---|---|
| **A. `ascii-live` core** | **Go**, in `agentbbs` | Source adapters (YouTube/HLS), shared FFmpeg worker, viewer fan-out, terminal rendering, the `tv@`/`tv-<slug>@` SSH route, and a `cmd/ascii-live` local CLI. Reuses `internal/ascii` and the `internal/calls` patterns. |
| **B. `@logicsrc/plugin-ascii-live`** | **TS**, in `logicsrc` | Browser-terminal viewer (xterm.js + SSE), public stream directory, and the stream metadata/events API. Built as a logicsrc `PluginDefinition` (`routes` + `events` + `tuiPanels`) — the shape it is literally designed for. |

Component A is the MVP. Component B is V1 (the browser viewer) and is the *only*
place the `@logicsrc/plugin-ascii-live` TS package exists.

```
                 ┌─────────────────────── AgentBBS (Go) ───────────────────────┐
  YouTube URL ─▶ │ source adapter ─▶ shared FFmpeg worker ─▶ RGB24 frames       │
  HLS .m3u8   ─▶ │   (yt-dlp -g)        (one per stream)        │               │
  PairUX call ─▶ │ LiveKit tracks ─▶ VP8→IVF→ffmpeg ───────────▶│               │
                 │                                              ▼               │
                 │                            internal/ascii.FrameRGB (▀ truecolor)
                 │                                              │               │
                 │                          fan-out multiplexer (1 worker → N)  │
                 │                            │            │                    │
                 └────────────────────────────┼────────────┼────────────────────┘
                                               ▼            ▼ (frames via SSE)
                                     SSH PTY viewers   @logicsrc/plugin-ascii-live
                                     (tv-<slug>@)      browser xterm.js viewer
```

---

## 2. What already exists (reuse, do not rebuild)

- **`internal/ascii/ascii.go`** — `FrameRGB(buf, w, h)` renders packed RGB24 to
  truecolor half-block ANSI (two pixels per cell via `▀`); `FitEven(cols, rows)`
  clamps geometry for the renderer leaving a status line. **This is the default
  renderer.**
- **`internal/calls/{calls,livekit}.go`** — joins a PairUX LiveKit call from an
  SSH session (`video-<code>@`, `video@`), pipeline VP8 → IVF → ffmpeg → RGB24 →
  ANSI. Codes are minted by PairUX only; SSH never creates calls.
- **`internal/store`** — pure-Go (modernc) sqlite store. ASCII Live metadata uses
  this, not a parallel DB.
- **Plugin contract** — `internal/plugin/plugin.go`
  (`ID/Title/Description/RequiresAuth/New`, `ExitMsg`). Hub menu integration.
- **`~/src/intr0s`** — real branded intro/logo `.mp4` assets for intro/BRB cards.

### Hard-won pipeline lessons (must carry forward)
- Subscriber **must send PLI** or the SFU never forwards video.
- `lksdk` IVF replay mispaces — use `ReaderTrackWithFrameDuration`.
- Publish needs `VideoWidth/Height` or dynacast pauses the track.
- ffmpeg pipe needs `-probesize 32`.
- Go pinned **1.26** via `mise.toml` (lksdk requires it).

---

## 3. What is genuinely new (the MVP)

1. **URL source adapter** — `yt-dlp -f 'best[height<=480]/best' -g <url>` to resolve
   a YouTube Live URL to an HLS URL (or accept a direct `.m3u8`), then ffmpeg
   `-vf "fps=N,scale=W:-2:flags=lanczos,format=rgb24" -pix_fmt rgb24 -f rawvideo`
   → RGB24 frames. (The existing path ingests LiveKit *tracks*; this adds a *URL*
   source.) Always convert to `rgb24` — `yuv420p` will fail.
2. **Shared-worker fan-out** — **one** ffmpeg worker per distinct stream, **N**
   PTY viewers attached. The current `calls.Handle` is standalone (one decode per
   session, leaving ends the session). The multiplexer — viewer join/leave,
   backpressure, reap-after-last-viewer-unless-pinned — is the headline value-add.
3. **SSRF / source hardening** — user-supplied URLs are a new attack surface the
   `video@` route never had (PairUX mints all codes there). This is an **MVP
   acceptance criterion**, not a later nicety. Block: private/link-local/loopback
   IPs, cloud metadata ranges (169.254.169.254 etc.), `file://`, and any protocol
   other than `http(s)` and the resolved HLS. Re-validate after DNS resolution and
   after any redirect.

---

## 4. Interaction model — username routing, not slash commands

AgentBBS routes by **SSH username** (`bbs@`, `pod@`, `agent@`, `video-<code>@`) +
Bubble Tea hub menus. There is **no in-room slash-command chat layer**; chat is the
separate `agent@` surface. So the original `/ascii-live open <url>` + "room chat
alongside" model does not fit. ASCII Live adopts username routing:

```
ssh tv@host              # browse the public stream directory (hub menu)
ssh tv-<slug>@host       # attach directly to a running stream
```

- **Browsing/attaching** is open to guests for public streams.
- **Opening a new stream from a URL** is an authenticated action (hub menu for
  members/agents, or the CLI/web) — never an anonymous SSH command, because that is
  the SSRF + abuse surface. First viewer to open a URL becomes the worker owner.
- Chat, if shown, is a separate **lipgloss text region** beneath the frame (mirror
  the existing status-line layout). Never composite chat into pixels.

The local CLI mirrors the surfaces for dev/testing:

```bash
ascii-live watch "https://youtube.com/live/<id>"   # local terminal preview
ascii-live watch "pairux:room_abc123"              # reuse the calls path
```

`ascii-live serve --ssh-port` is a **local dev convenience only** — production
reuses the AgentBBS `wish` server. Do **not** stand up a second SSH server.

---

## 5. Renderer

- **Default: truecolor half-block** via `internal/ascii.FrameRGB` (existing). This
  is the house style (matches doom-ascii) and looks far better than char-ramp.
- **Themes are color-degradation fallbacks**, not the primary path:
  - `truecolor` (default), `ansi` (256/16-color approximation),
    `mono`/`green`/`amber` (char-ramp ` .:-=+*#%@` for terminals without
    truecolor). The char ramp is the *fallback*, never the default.
- **Adaptive geometry** via `FitEven(cols, rows)`: clamp to even pixel height,
  leave one status line, `width = min(config.width, terminal.columns)`.

---

## 6. Free vs paid

Reuse the existing CoinPay / pods plumbing — **no new billing system**.

- **Free:** public YouTube/HLS URLs, public `tv-<slug>@` directory, low default FPS,
  header/watermark, session + concurrency limits.
- **Creator/Pro/Studio:** private streams, PairUX-as-source under access tokens,
  no watermark, higher FPS, presets, archive/replay, API/webhooks. Gate via the
  same plan capability check used elsewhere in AgentBBS.

---

## 7. Data model

Integrate with `internal/store` (pure-Go sqlite already in `agentbbs`). Do not
create a parallel DB. Minimal tables:

```sql
CREATE TABLE ascii_live_streams (
  id TEXT PRIMARY KEY,
  slug TEXT UNIQUE,
  owner_user_id TEXT,
  source_type TEXT NOT NULL,      -- youtube | hls | pairux
  source_url TEXT NOT NULL,
  title TEXT,
  status TEXT NOT NULL,           -- starting | live | stopped | error
  fps INTEGER NOT NULL DEFAULT 10,
  width INTEGER NOT NULL DEFAULT 120,
  theme TEXT NOT NULL DEFAULT 'truecolor',
  pinned INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  started_at DATETIME, stopped_at DATETIME,
  error_message TEXT
);

CREATE TABLE ascii_live_viewers (
  id TEXT PRIMARY KEY,
  stream_id TEXT NOT NULL REFERENCES ascii_live_streams(id),
  user_id TEXT,
  connection_type TEXT NOT NULL,  -- ssh | web
  joined_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  left_at DATETIME
);
```

(Presets table is optional, deferred to when saved presets ship.)

---

## 8. Config (Go)

Mirror the existing `AGENTBBS_*` / `LIVEKIT_*` env convention. Equivalent of the
original TS config as a Go struct + env knobs:

```
ASCIILIVE_ENABLED              (bool)
ASCIILIVE_DEFAULT_FPS          (int, default 10)
ASCIILIVE_DEFAULT_WIDTH        (int, default 120)
ASCIILIVE_DEFAULT_THEME        (truecolor|ansi|mono|green|amber)
ASCIILIVE_MAX_DURATION_MIN     (int)
ASCIILIVE_MAX_CONCURRENT       (int)   # streams
ASCIILIVE_MAX_VIEWERS          (int)   # per stream
ASCIILIVE_MAX_FPS / _MAX_WIDTH (int)
ASCIILIVE_SHOW_HEADER          (bool)
ASCIILIVE_WATERMARK            (string, optional)
ASCIILIVE_ALLOW_HLS            (bool)  # direct .m3u8 input
```

---

## 9. Events (component B only)

The frame event model (`ascii_live.frame` carrying the full frame string) is for
**SSE/web delivery only** (component B). **SSH frames write straight to the PTY**
as `internal/calls` already does — do not route SSH frames through an event bus.

Bus events worth emitting for the web/directory + agents: `stream_started`,
`stream_stopped`, `error`, `viewer_joined`, `viewer_left` (metadata only, no
per-frame payload on the control bus).

---

## 10. Phases (revised)

| Phase | Scope | Notes vs original PRD |
|---|---|---|
| **0** | Go `cmd/ascii-live watch <url>` — yt-dlp resolve + ffmpeg rgb24 + `internal/ascii` render, FPS/width flags, clean Ctrl+C. | Was "local TS CLI"; now Go, reusing the renderer. |
| **1 (MVP)** | `tv@`/`tv-<slug>@` SSH route; authenticated open-from-URL; **shared worker fan-out** (1 ffmpeg → N viewers, reap after last); SSRF guard; truecolor render; theme/fps/width; status + clean errors; process cleanup. | Pulls original "Phase 3 multiplexing" into the MVP; drops the chat-room + char-ramp + TS-package assumptions. |
| **2** | Browser viewer = `@logicsrc/plugin-ascii-live` (TS, in logicsrc): xterm.js + SSE, shared stream state with SSH, public directory. | This is the *only* TS package. |
| **3** | PairUX-as-source under the `tv` directory with access tokens. | Mostly already built (`internal/calls`, raw tracks). Reframe as "expose existing call render in the directory + tokens," **not** a composed-stream ingest. |
| **4** | Paid/private sources, plan limits, usage tracking via existing CoinPay/pods. | |
| **5** | intr0s intro/BRB/outro cards; archive/replay; captions/transcripts. | |
| **6 (optional)** | ASCII-styled RTMP **publishing** — separate pipeline, not the viewer path. | Explicitly out of scope for everything above. |

---

## 11. MVP acceptance criteria

MVP (Phase 1) is complete when:

- A member can open a public YouTube Live or HLS URL (authenticated path).
- The stream renders as **truecolor half-block** ASCII over SSH via `tv-<slug>@`.
- **≥2 viewers** attach to the **same** stream with **exactly one** ffmpeg worker.
- The worker is **reaped after the last viewer leaves** (unless `pinned`).
- Theme, FPS, and width are adjustable within configured limits.
- **SSRF guard** rejects private/loopback/metadata IPs, `file://`, and non-http(s)
  protocols, re-checking after DNS resolution and redirects.
- Errors are shown cleanly (no raw ffmpeg/yt-dlp dumps to normal users); admins/
  debug mode can see detail.
- Process + temp cleanup is reliable on disconnect, close, and crash.

---

## 12. Decisions (resolving the original §29 open questions)

1. **SSH ownership** — AgentBBS owns SSH; reuse `wish`. No second server.
2. **PairUX shape** — raw LiveKit tracks (already shipped). No composed-stream ingest.
3. **Chat placement** — separate lipgloss text region below the frame; never in pixels.
4. **Audio** — ignored for MVP (already the case); captions are a later transcript feature.
5. **Free sources** — public-only; directory is a V1 web feature, not MVP.
6. **Self-host** — yes, day one (repo is OSS; Go CLI gives it for free).
7. **Standalone CLI** — yes, as a Go `cmd/ascii-live` in `agentbbs`, sharing one
   frame package across `cmd/` and the SSH route. Not a separate TS tool.

---

## 13. Net

The "video → ASCII → SSH" core is already proven in this codebase. ASCII Live is
therefore a *small* feature: a Go source-adapter + shared-worker + `tv@` route
reusing `internal/ascii`/`internal/calls`, plus a separate TS
`@logicsrc/plugin-ascii-live` strictly for the browser viewer. Build it in that
order; keep RTMP publishing out of the viewer path entirely.
