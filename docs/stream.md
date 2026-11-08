# The desk stream (Agent Streams S1, Node's part)

An agent's desk can stream on OpenVibe.Live. The Node captures the desk, encodes it with the screen recipe, publishes
it over WHIP with a publish ticket, sends a snapshot while nobody watches, blanks the picture behind the privacy
curtain, and measures the glass-to-glass latency with a QR probe. The design is
`plan-state-2026-10-22/AGENT-STREAMS-S1-DESIGN-2026-11-06.md`; this document is the Node side of it.

## What runs where

| Piece | Where |
|---|---|
| The stream, its ticket, the state poll, snapshots, renewal | the core (`internal/stream`, `internal/node/stream.go`) |
| Capture selection, the browser desk's page capture, the curtain detectors, the hotkey | the computer plugin (`desk.stream`, `plugins/computer/openvibe_computer/stream.py` and `curtain.py`) |
| Encoding | ffmpeg (`libx264`), started by the core |
| Publishing | WHIP with pion (`internal/video`), video + a silent Opus track |

The core mints the stream and its `wpt_…` ticket on Live with this machine's Network node token (audience
`openvibe.live`, capability `live.agent-stream.publish`). **The plugin never sees the WHIP URL or the ticket**, and
neither does a log line, an error, `openvibe-link status` or the command's ack. The ticket is a
`credentials.Secret`.

## The command

`desk.stream` is a computer kind routed to the computer plugin, with the core doing the orchestration:

```json
{"action": "start", "title": "fix the failing tests",
 "task": {"source": "codes", "ref": "job_123", "title": "fix the tests", "url": "https://openvibe.codes/…"},
 "visibility": "private", "allow_sites": ["https://github.com"], "desk": "desk-a"}
{"action": "stop"}
{"action": "curtain", "up": true, "reason": "manual"}
{"action": "status"}
```

`start` answers `{streaming, id, page_url, visibility, expires_at, capture}`. `desk` may be `main` (the machine's own
screen), `private`, or `desk-<name>`; a browser desk is captured from its own page. `visibility` defaults to
`private`. `stream.live_url` in the Node config moves the API off production.

## The encoder recipe

`internal/stream/encode.go` builds the ffmpeg command line from the design:

* H.264 **main**, `-preset ultrafast`, `-tune zerolatency,stillimage`, `-bf 0`;
* a keyframe every 2 s of wall clock (`-force_key_frames expr:gte(t,n_forced*2)`, `-g 60` at 30 fps);
* variable rate: `mpdecimate` drops unchanged frames and `-fps_mode vfr` keeps the capture's timestamps (about 5 fps on
  a still screen, up to 30 when it moves);
* 1080p30 (or the desk's size), with a 720p rung: after two WHIP retries the core restarts the capture at 720p
  (`EncodeOptions.LowBandwidth`);
* CBR capped at 4 Mb/s (`-b:v 4000k -maxrate 4000k -bufsize 4000k -x264-params nal-hrd=cbr`).

Capture is `x11grab` or (ffmpeg 7.1+) `pipewire` on Linux, `ddagrab` on Windows, `avfoundation` on macOS, and
`-f image2pipe` for a browser desk's page (the plugin writes JPEGs into a 0600 FIFO at about 5 fps, the floor of the
VFR range). A browser desk has no display to grab.

A silent Opus track is always negotiated (a 20 ms silence frame every 20 ms), so S4's voice needs no renegotiation.

## Live's state and snapshots

`GET /api/streams/agent/:id/state` is polled every 10 s with the ticket. `encode: false` stops the capture and pauses
video on the WHIP session (the audio keeps it alive); a snapshot is `PUT` every 5 s, JPEG or WebP, at most 200 KB,
taken through the plugin's own `screen` command. `encode: true` restarts the capture. The ticket is renewed at **3 h**
(sooner if it would expire first), and a `DELETE /api/streams/agent/:id/ticket` is the kill switch.

## The curtain

The plugin's detectors answer `None` (clear), a reason (raise), or raise (fail closed). Every one of these raises it:

* `password_field` — the focused AT-SPI element is a password field (UI Automation on Windows, the AX API on macOS;
  a missing accessibility stack is *unsure*, which raises it);
* `secret_file` — the focused window's title or class looks secret, or a secret-named file was read through `fs` or
  named in an `exec` (`/.env`, `*credentials*`, `id_rsa`, `*.pem`, `*.key`, a password manager);
* `env_print` — an `exec` ran `env`/`printenv` or printed three or more environment assignments;
* `manual` — the hotkey (Ctrl+Alt+P on X11, `SIGUSR1` for a service), Curtain now, or the `curtain` command;
* `site` — a browser desk navigated outside its `allow_sites` (checked on the agent's own actions and on every
  main-frame navigation the browser reports);
* `detector` — any detector failed or returned something it could not decide.

The curtain starts **up** and only comes down after every detector has been clear for 2 s, or when the owner lowers
it. While it is up the core feeds the publisher a pre-encoded "Private screen" frame instead of the capture, mutes the
audio track, and reports `POST /api/streams/agent/:id/curtain {up, reason}`; Live pauses every viewer's consumer as
well. Viewers never see the reason. `desk_stream_detectors` in the plugin config can disable a detector a deployment
genuinely cannot run (a deliberate choice, not a silent failure).

**A lost plugin is a curtain, not a gap.** The plugin sends its curtain state every second. If the computer plugin
exits, crashes, is not ready (the supervisor's state), or goes silent for two seconds while a stream is up, the core
raises the curtain itself at once (reason `detector`), blanks the video and mutes the audio; a "curtain down" command
is refused while the plugin is lost. It comes down only after the plugin is back **and** has reported clear for two
seconds, so a restarted plugin's first "clear" cannot uncover the desk.

## The latency probe and the viewer

```
# Live hands the desk a WHIP URL and a wpt_ ticket (create + state routes).
OPENVIBE_STREAM_TICKET=wpt_… openvibe-link stream --probe \
    --whip-url https://whip.openvibe.live/whip/agent/ags_… --seconds 60

# The same ticket's view side, on a WHEP endpoint:
OPENVIBE_VIEW_TICKET=wvt_… scripts/stream-viewer.sh --whep https://openvibe.live/whep/agent/ags_… --seconds 60
```

The probe draws a QR of `time.Now().UnixMilli()` in the corner of every frame with the built-in PCM encoder (no ffmpeg,
no display) and publishes it with the same WHIP path and silent audio. The viewer joins over WHEP, decodes the QR from
each frame with OpenCV, and reports p50/p95/max; the S1 pass line is **p95 under 1000 ms over 60 s**. `--self-test`
checks the statistics and the decoder without a network. `scripts/stream-viewer.sh` installs `aiohttp` with the other
dependencies, and the viewer reads OpenCV's decoded string (the points are the second return value of
`detectAndDecode`).

**Production, 2026-11-06:** a private agent stream on openvibe.live, published by the v0.41.0 linux/amd64 binary's
`stream --probe` from Xvfb `:99` and watched through the WHEP endpoint for 60 s, measured **p50 148.5 ms, p95 168.5 ms,
max 186 ms** (597 QR frames). The S1 pass line (p95 under 1000 ms) passed. Raising and lowering the curtain during the
check made the embed show "Private screen" and clear it again; the stream was ended and its publish ticket revoked
afterwards.

## Tests

Go (`go test ./internal/stream ./internal/video`): the recipe's arguments, each capture kind, the publisher's silent
audio and pause, the state poll turning encoding off into snapshots, renewal at 3 h, the curtain blanking every
captured frame, the QR probe round-trip, and that a ticket never appears in JSON or an error.

Python (`plugins/computer/tests/test_stream.py`): every curtain trigger, a detector exception and an unsure detector
raising it, the two-second clear hold, the owner lowering it, the capture each platform chooses, a browser desk's
JPEG frames reaching the FIFO, and the plugin's start/status/stop/refuse path.

## Platform notes and follow-ups

* Linux desktops use `x11grab`; PipeWire needs ffmpeg 7.1+ and `OPENVIBE_PIPEWIRE_NODE` set to the screen-cast node.
  The target ffmpeg here is 6.1, so `x11grab` is the default.
* A browser desk streams its page at 5 fps (an ffmpeg-friendly floor); a real DevTools screencast at up to 30 fps is the
  next step.
* The AT-SPI detector sees the accessibility stack of the Node's session; on a private Xvfb desk the desk's own
  `at-spi2-registryd` must be running for it to see the desk's apps.
* Simulcast (S2) and the owner's page (Live's part) are the next pieces.
