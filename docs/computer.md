# The computer plugin: your machine for your agents

The `computer` plugin gives the machine's terminal, files and screen to its owner's agents (OpenVibe.Actor, plan
T14/T17), through OpenVibe.Bot and the Link's one outbound link. It is **off** unless you turn it on, it runs as your
own account (never root: the one exception is a shared desk host for other people, where the Link runs as root and
every desk sits in its own sandbox, see "Desk hosts for other people"), and **you set what it may do on the machine
itself**: nothing in the cloud can raise that level.

## Turn it on

On Linux, from your own account (the installer uses sudo itself):

```sh
curl -fsSL https://openvibe.link/install | sh -s -- --network <URL> --pairing pair_… --code ABCD-1234 --computer ask
```

`--computer LEVEL` runs the Link's service as **your** account (never root, never the `openvibe-link` system account)
and adds the plugin with your home folder as its only folder. It cannot share a machine with `--worker` yet (the job
worker's service runs as root).

By hand, or on another system, add it to `plugins` in the Link's `config.json` and run the Link as your own account
(`openvibe-link run` in your session, or `openvibe-link install --user <you>`):

```json
{"name": "computer", "config": {"level": "ask", "roots": ["/home/you/projects"]}}
```

## Control levels

| level     | screen, list, stat, read (inside the folders) | run programs, write files (inside the folders), mouse and keyboard | outside the folders |
|-----------|----------------------------------------------|--------------------------------------------------------------------|---------------------|
| `observe` | yes                                          | no (`nack not_allowed`)                                            | no                  |
| `ask`     | yes                                          | after you approve each one on the machine                          | no                  |
| `trusted` | yes                                          | yes                                                                | no                  |
| `full`    | yes                                          | yes                                                                | yes                 |

`observe` is the default. Folders are `roots` (default: the home folder of the account running the Link); a path is
checked after symlinks are resolved, so a link out of a folder is outside it.

**The folders limit the plugin's own actions**: its reads, its writes, its screen and its browser. They do **not**
limit a program. At `trusted` a program runs without asking, and at `ask` one runs when you approve it; either way it
runs as your account, with everything that account can reach, not only the folders. An approved program can read and
write anywhere your account can.

**How a folder is kept.** On Linux the plugin opens every folder once and resolves each name under that handle
(`openat2` with `RESOLVE_BENEATH`, or one component at a time where the kernel is older), so a name that is swapped
for a link after it was checked cannot move the work outside the folder. A link out of the folder is refused, and so is
a file with more than one name (a hard link to a file outside the folder), below `full`. On macOS and Windows the
plugin compares path names only: there a name that changes under it is still a name.

**Approvals** (level `ask`): a request waits on the machine until you answer it, for at most `approval_timeout_s`
(default 120 s) and never longer than the request's own deadline:

```sh
openvibe-link approvals          # what waits: id, kind, what it would do
openvibe-link approve <ID>       # let it run
openvibe-link deny <ID>          # refuse it
```

A request shows what it would do in full: every argument of a program, the program it resolves to, the folder, and
whether stdin is given with its size and digest; a write's path, mode, size and content digest; the whole text to type,
or its length and digest. Control, ANSI and bidi characters are escaped. A notification or dialog that cannot show all
of it offers no **Approve** button: answer that request from `openvibe-link approvals`.

**What an approval does not limit.** Approving a program lets it run as your account, with everything that account can
reach: the folders are the plugin's own limit, not a sandbox around the program. Read what it would do before you
approve it.

**The tray** (Linux and Windows): `openvibe-link tray` puts the Link in your notification area. The installer's
`--computer` starts it when you sign in (`openvibe-link tray --autostart on|off` does it by hand). It runs in your
desktop session, not in the service, and talks to the Link over the same local socket as the commands above:

- the icon is blue when all is well, amber while a request waits, red while a stop is latched, grey when the Link does
  not answer;
- the menu lists what waits (kind and what it would do), each with **Approve** and **Deny**, then **Stop everything**
  (the kill switch), **Resume** and your machines page on openvibe.bot;
- a new request raises a notification with **Approve** and **Deny** buttons (Linux, any desktop with
  org.freedesktop.Notifications; without buttons it names the command to answer with).

KDE, XFCE, Cinnamon and MATE show tray icons; GNOME needs the AppIndicator extension, and its notifications still ask.
While the tray runs, the plugin's own prompts below stay quiet.

**Without the tray**, when the Link runs in your graphical session, the plugin asks itself (config `notify: false` turns
it off): on Linux a notification with Approve and Deny buttons (`notify-send` from libnotify 0.7.10 on; older ones show
the command to answer with); on macOS a dialog with **Deny** as its default button, which gives up when the request
expires. A prompt goes away once the request is answered anywhere else, expires or is stopped.

**An approval is not taken while the agent drives the mouse or keyboard**, nor for 2 seconds after: an approved click
must not be able to press another request's Approve button. `openvibe-link approve` then answers "answer again in a
moment", and the tray and the plugin ask again. Deny always goes through.

At most 8 requests wait at once. The panel on openvibe.bot sees `approval` and `approval_done` events, so it can say
that a request is waiting on the machine.

**Kill switch:** `openvibe-link stop` (and an owner's stop) drops the plugin to `observe`: what runs is killed, what
waits is denied, the agent's browser is closed, and the core also refuses `exec`, file writes, `input` and the browser's
acting actions until `openvibe-link resume`. `halt` kills what runs. An stop on this computer always needs that local
resume: the owner's remote clear (`estop` `latched:false`, or a config without `estop_latched`) releases the robot's remote
latch, never this computer.

## What it does

| kind     | value                                                                                         | answer (`ack.data`) |
|----------|-----------------------------------------------------------------------------------------------|---------------------|
| `exec`   | `argv` (a program and its arguments: never a shell line), `cwd`, `timeout_s`, `stdin` (≤ 64 KB), `desk`, `detach` (Link v0.26.0: see "Programs on a desk") | `exit_code`, `stdout`, `stderr` (each cut at `max_output_bytes`), `truncated`, `timed_out`, `duration_ms`; detached: `started`, `pid`, `desk` |
| `fs`     | `op` `list` / `stat` / `read` (`offset`, `length`) / `write` (`content_b64`, `mode` `create`, `overwrite` or `append`) / `mkdir`, and an absolute `path` | `entries` (≤ 1000), the stat, `content_b64` (≤ `max_read_bytes`), `written`, `created` |
| `probe`  | `target`: `http(s)://…` (one GET, no redirect followed) or `tcp://host:port`; `timeout_s` | `status`, `content_type`, `etag`, `last_modified`, `body` (≤ 64 KB), `ms`; or `open`, `ms` |
| `screen` | `max_width` (default 1280)                                                                    | `format` (`jpeg` with Pillow, else `png` ≤ 300 KB), `width`, `height`, `content_b64` |
| `input`  | `action`: `move`, `click`, `double_click`, `right_click` (`x`, `y`), `scroll` (`dy` clicks, ±20), `type` (`text`, ≤ 2000), `key` (`keys`: one key or chord, `ctrl+s`, `Return`); `width`, `height`: the picture `x`, `y` are measured in | `done`, `action`, and the real `x`, `y` it used |
| `browser` | `action`: `open` (`url`, http or https), `click` (`selector`, or the `text` a button or link shows), `fill` (`selector`, `text` ≤ 2000), `press` (`key`: Enter, Tab, Escape, Backspace, Delete, arrows, PageUp, PageDown, Home, End, Space), `back`, `read`, `screenshot` (`max_width`), `close` | a page: `url`, `title`, `text` (visible text, capped at 20,000 serialized bytes), `truncated`, `links` (up to 60); `screenshot`: `format`, `width`, `height`, `content_b64`; `fill`: `done`, `chars` |

A program runs in its own process group with the Link account's rights, at most 2 at a time, and is killed at
`timeout_s` (the owner's `timeout_s` is the cap). A write with `overwrite` replaces the file atomically. Screens come
from `screencapture` (macOS), `grim` (Wayland) or `import`, `scrot` or `gnome-screenshot` (X11), so the Link must run in
your graphical session for `screen`; without one the plugin simply does not offer it.

### The live screen (`screen_stream`, Link v0.12.0)

You can watch your machine's screen live on openvibe.bot (**Your machines** → **Show screen**), for 30 seconds to
30 minutes at a time:

- **Only you see it.** Bot gives your machine's start command the WHIP address and key of its own private
  OpenRestream stream (no recording, never on Live). No operator or agent supplies them, and agents cannot start it.
- **How it works.** The plugin captures a JPEG frame with the same tools as `screen`, 1 to 5 times a second (2 by
  default), at most 1600 px wide when Pillow is installed. The core encodes the frames (ffmpeg, else its built-in
  encoder) and publishes them on a WebRTC session of their own, separate from a robot's camera.
- **Through ov-desk (v0.75).** On an X11 session where `ov-desk` is installed (Linux; `OPENVIBE_SCREEN_NATIVE=0` turns
  it off), the plugin still applies your level and says the screen is live, but the core runs `ov-desk stream` instead.
  It reads the screen through MIT-SHM and encodes H.264 itself with OpenH264's screen-content mode, at 15 a second,
  at most 1600 px wide, about 2.5 Mbit/s with a keyframe every 2 seconds, and one at once when a viewer asks for it
  (a PLI or a new viewer: the core's RequestKeyframe signals ov-desk, SIGUSR1). No screenshot tool, Pillow or ffmpeg is needed.
  On one workstation (1280×800) a frame cost about 16 ms of CPU, against about 70 ms for the old path's forked `import`
  (51.6 ms) and its ffmpeg encoding, and a frame arrives every 66 ms instead of every 500. Wayland and other systems
  keep the frames path.
- **Published by ov-desk itself (v0.77).** Where ov-desk is v0.77 or newer, it also publishes the stream over WHIP
  (`ov-desk publish`, WebRTC by str0m), so the frames never cross into the Go core. The core only starts it and reads
  how it goes. The key reaches ov-desk in its environment, never in its arguments. A viewer's PLI or FIR makes the next
  frame a keyframe directly. When ov-desk finds no UDP path to the server, or is older than v0.77, the core falls back
  to the pipe and its own publisher, which can relay through TURN. `OPENVIBE_SCREEN_PUBLISH=0` turns this off.
  The owner wants Rust wherever it gains at all, so both paths were raced on the same Xvfb screen and receiver: 30 s
  a run, 3 runs a case, the median, counting the CPU of every sending process:

  | Screen | Path | CPU | Memory | Frames received |
  |---|---|---|---|---|
  | Still (a clock) | pipe + Go (pion) | 8.49 s | 57.9 MB | 444 |
  | | ov-desk publish (rust-crypto) | 8.54 s | 43.3 MB | 449 |
  | | ov-desk publish (aws-lc) | 8.20 s | 43.2 MB | 449 |
  | Moving video, full screen | pipe + Go (pion) | 11.22 s | 62.3 MB | 443 |
  | | ov-desk publish (rust-crypto) | 10.23 s (−9%) | 44.7 MB (−28%) | 449 |
  | | ov-desk publish (aws-lc) | 10.73 s (−4%) | 44.7 MB | 448 |

  On a still screen, capture and encoding (the same Rust code on both paths) are nearly all the work, so CPU ties.
  When the screen moves, Rust's media path wins, and it always uses about a quarter less memory. The pure-Rust crypto
  backend won, and it is the build. The media path alone was raced on real recordings as well: splitting the frames,
  RTP and SRTP took 11–44% less CPU in Rust (webrtc-rs) than in Go (pion).
- **You are told.** The desktop says when it starts ("Your screen is live on OpenVibe"), and `openvibe-link status`
  shows `Screen: live … until …` while it runs.
- **It ends** when its time is up, when you press Stop, when capturing fails five times in a row, and at once on
  `openvibe-link stop` or an stop. While a stop is latched, a new start is refused.
- **Levels.** It runs at every level, like a screenshot. To never offer it, set `allow_screen_stream: false`.
- **Without Pillow** the capture tool must write JPEG itself: `screencapture`, `grim`, `import` and `scrot` do;
  `gnome-screenshot` does not.

**Mouse and keyboard** (`input`) is an acting command like running a program: refused at `observe`, approved one by one
at `ask` (the approval says exactly what it would do: "click at 640, 360 of a 1280×720 picture of the screen",
"type 12 characters: 'hello there'", "press ctrl+s"). Coordinates are in the space of the screen picture the agent last
saw (`width` × `height`), scaled to your real screen. It uses `xdotool` on an X11 session and `cliclick` on macOS
(`brew install cliclick`; no scrolling there); Wayland has no general input tool yet, so it is not offered there. Typed
text and keys are passed to the tool as arguments, never through a shell, and `keys` takes only letters, digits, `+`,
`-` and `_`.

**The browser** (`browser`) is a Chromium-family browser (Chromium, Chrome, Edge or Brave) the agent drives:
- **Its own profile.** It runs in a folder of its own (`browser_profile`, by default `~/.local/share/openvibe-link/browser`; on Windows `%LOCALAPPDATA%\openvibe-link\browser`), so the agent never sees the sessions of your own browser unless you point it at one. Sign it in to a site yourself (`browser_headless: false` shows its window) if you want the agent to work there as you.
- **No port.** The Link starts it on the first command with `--remote-debugging-pipe`: it speaks the DevTools protocol over two file descriptors, so nothing else on the machine can drive it.
- **Levels.** `read`, `screenshot` and `close` run at every level. `open`, `click`, `fill`, `press` and `back` are acting commands: refused at `observe`, approved one by one at `ask`. The approval says exactly what would happen: "open https://… in the browser", "type 5 characters into #q in the browser: 'hello'".
- **Downloads.** Below level `full`, the browser is told to deny every download (a page, a click or a redirect cannot put a file anywhere on the machine). At `full` a desk downloads into its own folder, and only when that folder resolves inside `roots` (it is never used from outside them).
- **Your own network.** Below level `full`, `open` refuses an address on the machine's own network (private, loopback or link-local, checked for every address the name resolves to).
- **The egress guard** (Link v0.25.0). A page the agent opened could still reach your network with its own scripts: a
  `fetch`, an image, a WebSocket or a redirect to your router, a printer or another computer at home. So every
  connection every agent browser makes (on your screen and on every desk) goes through a small proxy the plugin runs on
  `127.0.0.1`.
  - **Checked.** Each address is resolved and checked. A name with any address that is not on the public internet is
    refused below level `full`.
  - **Connected to what was checked.** The connection goes to the exact address that was checked, so a name cannot
    pass the check with one address and connect to another.
  - **Nothing around it.** Loopback goes through the guard too, WebRTC sends no UDP around it, and QUIC is off.
  - `browser_egress: "open"` turns the guard off, for a machine whose agent must reach a local service.
- **Task origins.** A browser command may carry `origins`, a list of exact `http(s)://host[:port]` origins. When present,
  the browser gets a proxy of its own: navigation, page resources, HTTPS tunnels and WebSocket connections to other
  origins are refused, even if `browser_egress` is `open`. The list has at most 32 origins; an empty list permits no
  network address. Shared desk hosts currently refuse an origin-bounded browser command because their sandbox has
  different network plumbing. A task must send the same list on every command; omitting it is legacy unrestricted
  access on a new browser.
- **Page text boundary.** Page text and the lines reported by an action receipt carry a random nonce and the page's
  origin in their markers. The source text is capped before those markers; `truncated` says when content was cut.
  The complete page answer, including links and elements, is capped at 48 KiB of serialized JSON.
- **Elements and receipts** (Link v0.21.0, the Desks plan's step D3). `open` and `read` list the page's interactive
  elements, one compact line each: `e7 button "Sign in"`, `e8 textbox "Email" (email)`, `e9 checkbox "Remember me"
  checked`. That is at most 120 visible links, buttons, fields, boxes and menus, named by their label, text or
  placeholder. Each element keeps its ref (`data-ov-ref`) while it stays on the page, so `click` or `fill` with the
  selector `[data-ov-ref="e7"]` acts on exactly that element. An agent works from the page's own structure first and
  needs a screenshot only when that is not enough.
- **Receipts.** `click`, `press` and `back` answer with a `receipt` of what they changed: `url_changed`, the first lines
  that `appeared` (up to 8) and how many lines are `gone`. An agent knows at once whether its click did anything,
  without another read.
- **Popups and new tabs** (Link v0.24.0). A page the page opens, such as a "Sign in with Google" popup or a link to a
  new tab, comes to the front. The agent reads and acts on it, and a browser desk shows it, with `"page": "opened"` in
  the receipt. When it closes (a sign-in popup closes itself), the page it came from is in front again
  (`"page": "closed"`). Pages are followed at most 5 deep, and only pages opened from the followed ones. The agent's
  clicks count as a person's (user activation), so a site may open its sign-in popup from them.
- **Closing.** It closes after `browser_idle_s` without a command and at the kill switch.
- **Platforms.** Linux and macOS; not offered on Windows yet.

### The private desk (`desk`, Link v0.15.0)

An agent can work on a desk of its own instead of your screen (plan track K: Vibe's agent mode):
- **What it is.** An Xvfb display on your machine with a window manager (metacity, openbox, xfwm4 or fluxbox, whichever
  is installed) and a D-Bus session of its own, so apps opened there open there, not on your screen.
- **Using it.** `desk` `{"action": "start", "width"?, "height"?}` (1280×800 by default), `stop` or `status`. Then
  `screen`, `screen_stream`, `input` and `browser` take `"desk": "private"`: screenshots and the live screen of the desk,
  the mouse and keyboard on the desk (points scale to the desk's own size), and the agent's browser shown on the desk
  in a profile of its own (`browser_profile` + `-desk`). Without `desk` (or with `main`) they act on your own screen,
  as before.
- **Several desks at once** (Link v0.19.0, the Desks plan's step D1). Every `desk` command and every `screen`,
  `screen_stream`, `input` and `browser` command may name a desk: `"desk": "desk-<name>"` (lower-case letters, digits
  and dashes, up to 24 after `desk-`; `private` is the first desk, as before). Each desk is a separate computer for
  its agent:
  - its own X display and D-Bus;
  - its own browser in its own profile (`browser_profile` + `-<name>`) and downloads folder (used at level `full`);
  - its own temp folder (`TMPDIR`), so its clipboard, cookies, files and windows never meet another desk's.

  Two agents can work at once without either seeing or driving the other's screen, and two desks' browsers act in
  parallel. `{"action": "list"}` reports the desks that run. A desk no command used for `desk_idle_s` (15 minutes by
  default) is stopped, with an event `desk_idle_stopped`: a desk an agent left behind when it crashed or restarted
  mid-task ends by itself. `max_desks` caps how many run at once (by default one per
  2 GiB of memory, 1 to 8); one more is refused (`not_ready`), never queued. Stopping a desk ends only its processes,
  and the kill switch stops them all. The desks' own folders are under `desk_dir`
  (`~/.local/share/openvibe-link/desks`).
- **Levels.** Starting and stopping a desk are acting commands (refused at `observe`, approved one by one at `ask`);
  `status` and `list` run at every level. What runs on the desk keeps its own rules: input and the browser still act.
- **Watching and taking over.** Start the live screen with `"desk": "private"` (your desktop is not told, since nothing of
  yours is shown) and drive it the same way.
- **It ends** on `stop`, and at once on `openvibe-link stop` or an stop: every process the desk started is ended by
  handle, never by name.
- **Needs** Linux with `Xvfb` (Debian/Ubuntu: `apt install xvfb`), `xdotool` for input and ImageMagick `import` or `scrot`
  for screenshots. Not offered elsewhere; `allow_desk: false` turns it off.

### Browser desks (`"kind": "browser"`, Link v0.23.0)

Most agent work happens in a browser, and a browser does not need a screen. `{"action": "start", "desk": "desk-<name>",
"kind": "browser"}` starts a **browser desk** (the Desks plan's step D2): a headless browser alone, with no display or
window manager.
- **About half the cost of a whole desk.** A browser desk counts as half a desk against `max_desks`, so a machine with
  `max_desks: 2` runs four browser desks, or one whole desk and two browser desks.
- **The browser's pipe through ov-desk (v0.77).** Where ov-desk v0.77 or newer is installed (under /usr, /bin or /opt, and
  answering `ov-desk browser --check`), the plugin runs the browser as `ov-desk browser -- <browser argv>`.
  - **Every DevTools message passes through unchanged**, so everything the plugin does works as before.
  - **A browser desk's live frames are taken in Rust.** ov-desk asks for the picture, decodes it and writes the file,
    and the picture never passes through Python.
  - **Turning it off:** `OPENVIBE_BROWSER_RELAY=0`.
  - **Why:** the plugin's DevTools client in Python, raced against the same client in Rust on the same Chrome and page
    (median of 5), took 2.6–2.8 times the CPU a call and 1.8 times the memory:

    | Call | Python CPU | Rust CPU |
    |---|---|---|
    | Screenshot | 1,237 µs | 455 µs |
    | Evaluate | 69 µs | 27 µs |
    | Mouse move | 245 µs | 88 µs |

  - **A browser desk's live view, 30 s at 5 frames a second, median of 5:**
    - total CPU 0.353 → 0.204 s (−42%);
    - the plugin's own 0.353 → 0.044 s;
    - the same 150 frames.
  - **Agent screenshots too (v0.78).** An action's screenshot is taken scaled by Chrome and handed back by ov-desk as
    Chrome's own base64. Before, Python decoded the full-size picture, shrank it with Pillow and encoded it again. At
    640 wide, over 3 rounds of 60: 23–25 ms of CPU a screenshot became 0.7–0.9 ms (−96%), and 57–64 ms of waiting
    became 49–50 ms.
  - **What it costs:** the relay is one more process (6.4 MB), and an ordinary call takes one more hop (323 → 366 µs).
    Both shrink as more of the browser moves into it.
- **On a Mac too.** A browser desk needs only Chromium, Chrome, Edge or Brave, so it runs on macOS as well as Linux.
  `describe` lists the kinds a machine runs in `desk.kinds` (`desktop`, `browser`).
- **chrome-headless-shell where it is installed** (v0.69). A browser desk, and the agent's headless browser, run
  Chrome for Testing's headless build when it is there: measured on one machine against full Chrome with
  `--headless=new`, it started in 92 ms against 290, loaded a page in 15 ms against 105, took a screenshot in 49 ms
  against 69, and used 177 MB against 300 MB (PSS) in 5 processes against 10, so more desks fit on a machine. A desk
  host's installer (`--desk-host`, `--desktops`, Linux x86-64) puts it in `/opt/openvibe-link/chrome-headless-shell`, a
  link to a folder per version, and a daily timer (`openvibe-link-headless-shell.timer`) brings each Chrome stable
  release within a day; a desk already running keeps the version it started with. `browser_headless_shell` is `auto`
  (the default), `off`, or a path; the owner's `browser_path` and a browser shown on a display (a whole desk, a lent
  desktop) keep the full browser. `describe` says `browser.headless_shell`.
- **Its page is its screen.** `screen` takes a picture of the page. `input` is the page's own mouse and keyboard:
  clicks, the wheel, keys and chords in xdotool's names (`Return`, `ctrl+a`), and text. A click answers with what it
  changed (a receipt, as in the browser kind). `screen_stream` sends frames of the page, so you can watch a browser desk
  live and take it over like any desk. All of it goes through the browser's pipe: no display, no capture tool, no
  Pillow.
- **Like any desk.** It has its own profile (`browser_profile` + `-<name>`), downloads (at level `full`) and temp folder, and nothing of
  your session (no `DISPLAY`, session bus or X cookie). `browser` drives its page, and its page is exactly the desk's
  size (1280×800 by default).
- `private` is always a whole desk; a whole desk is `"kind": "desktop"`, the default.

### Programs on a desk (`exec` with `desk` and `detach`, Link v0.26.0)

A whole desk is a whole computer, so an agent can work in real applications there, never on your screen.
- **`"desk": "desk-<name>"`** (or `private`) runs the program in that desk's session: its display, session bus and
  temp folder. A program with windows opens on the desk.
- **`"detach": true`** starts a program that keeps running and answers at once with its `pid`: an office suite, a
  terminal, an editor. It is ended when its desk stops: by its agent, by `desk_idle_s`, or by the kill switch. At most
  16 run on one desk.
- **Never on your own session or a browser desk.** A browser desk has no screen for windows.
- **Desk templates** come from this: a *research desk* is a browser desk; an *office desk* is a whole desk with an
  office suite opened on it (`libreoffice --writer report.odt`); a *dev desk* is a whole desk with a terminal and an
  editor. On a desk host, install the applications its desks should have (`apt install libreoffice xterm`).
- `exec` keeps its rules: the folders you allow, the level (approved one by one at `ask`), and `allow_exec`.
  `describe` says `terminal.desks: true`.

### Kept profiles (`"profile": "<name>"`, Link v0.24.0)

Each agent task gets a desk of its own, and with it a fresh browser. A **kept profile** carries the person's sign-ins
from desk to desk: `{"action": "start", "desk": "desk-<name>", "profile": "agent"}`. OpenVibe.Actor starts every
task's desk from the profile `agent`.
- **Sign in once.** The desk's browser starts from a copy of the kept profile: its cookies and site storage. When the
  person signs in on an agent's desk (a "Needs you" step), the next task is already signed in.
- **Saved when the desk stops.** The browser is asked to quit first, so it writes its cookies down. The desk's profile
  is then saved back (`profile_saved` in the answer), and the desk's own copy is removed, so a desk per task leaves
  nothing behind. When two desks with the same profile stop, the last one wins.
- **Session sign-ins too** (Link v0.24.1). Many sites sign people in with a cookie that ends when the browser quits,
  and Chrome throws those away as it closes. Before the desk's browser quits, its session cookies are kept beside the
  profile (`ov-session-cookies.json`, 0600), and the next desk's browser gets them back as soon as it starts.
- **Not kept:** caches, crash reports, locks and the last session's tabs. A profile larger than `desk_profile_max_mb`
  (256 MB) is not saved, and an event `desk_profile_too_big` says so. The kill switch saves nothing.
- **Where.** `browser_profile` + `-kept-<name>`, 0700, on this machine only. Profiles that move between machines are
  the Desks plan's step D7, later.
- The private desk keeps its own profile, as before. `describe` says `desk.profiles: true`.

### Desk hosts (`--desk-host`, Link v0.20.0)

An old laptop, a mini PC or a spare workstation can host desks for your agents: no screen, keyboard or login needed.
```
curl -fsSL https://openvibe.link/install | sh -s -- --network … --pairing pair_… --code … --desk-host
```
- **Its own account.** The Link runs as `openvibe-desk` (home `/var/lib/openvibe-desk`), never as a person. An agent
  there never reaches anyone's own files, browser or sessions. The computer plugin runs at `trusted` for that account's
  folder only (`--computer` sets another level).
- **What desks need.** On apt systems the installer adds Xvfb, openbox, D-Bus, xdotool, ImageMagick, fonts and
  Chromium. Elsewhere it lists them.
- **Sized to the machine.** `max_desks` defaults to one desk per 2 GiB of memory (1 to 8), and the installer says how
  many. Each agent task gets a desk of its own (OpenVibe.Actor with OpenVibe.Bot, Link v0.19.0): its own screen,
  browser profile, downloads and temp folder. A task beyond `max_desks` is told the machine is busy (its desk does not
  start); it never shares another task's desk.
- **No screen of its own.** A desk host offers no screen, mouse or keyboard of its own, only its desks'. The Link gives
  `screen`, `screen_stream` and `input` to the plugin with desks, which acts on the desk named and refuses the host's
  own screen (Link v0.23.0).
- **Then** turn on "Let my agent use this computer" for the machine on openvibe.link/machines. Watch any desk live from
  there, and take it over the same way.

### Desk hosts for other people (`--desk-host --shared`, Link v0.32.0)

A desk host OpenVibe or a community member runs for **other people's** agents (the Desks plan's step D5, second part)
must keep each person's desk away from the machine and from every other desk. Such a host runs browser desks only, and
each desk's browser runs in the job worker's sandbox (docs/worker.md):
```
curl -fsSL https://openvibe.link/install | sudo sh -s -- --network … --pairing pair_… --code … --desk-host --shared
```
- **Each desk sandboxed.** The plugin starts a desk's browser as `openvibe-link desk-sandbox -- <browser> …`: its own
  user, mount, network, PID and IPC namespaces, as the unprivileged `openvibe-job` (`worker.run_as`), in a cgroup of its
  own with `desk_memory_mb` of memory (swap off: running out ends that desk alone), `desk_cpu_millis` of CPU,
  `desk_pids` processes and a `desk_disk_mb` private `/tmp`, where its profile and downloads live. It sees only the
  system's own files read-only, the browser's install directory and the fonts; never the Link's config or credential,
  another desk, or a home. Its network is the public internet only (`desk_egress: public`), enforced by the kernel
  (the worker's veth and nftables rules), never the machine's own network or the host itself. Chrome's own sandbox is
  off inside it (ours replaces it; it cannot nest), and the browser gets nothing of the plugin's environment.
- **Nothing of the machine.** `exec`, `fs`, `probe`, the machine's own screen, mouse, keyboard and browser, whole desks,
  kept profiles and `screen_stream` (the machine's one live stream is its operator's) are refused; `screen`, `input` and
  `browser` act on a browser desk. `describe` reports only `desk`, with `isolation: "sandbox"` and the limits.
- **What it runs as.** The Link runs as root, which the sandbox needs (namespaces, a cgroup per desk, its network), with
  its worker on (no functions: no jobs offered) so it sets up its cgroup before any desk starts; the computer plugin
  runs as root only here, at level `trusted` (nobody is at the machine to approve), and starts nothing unconfined.
- **What it needs.** Linux 5.14+ with cgroup v2 and the Link's service delegated (`Delegate=yes`), a browser installed
  under `/usr` or `/opt` (Google Chrome's .deb on amd64, Debian's Chromium on ARM; a snap cannot run sandboxed),
  nftables, iproute2 and IPv4 forwarding. The installer adds them, sizes `max_desks` to the machine (a desk may use
  1 GiB; a browser desk counts half) and keeps your own sizes on a second run.

### Lending whole desktops too (`--desktops`, Link v0.65.0)

Some agents work a whole computer, not a browser: a terminal, a file manager, any program. A desk host for other people
may lend **desktops** as well, when its owner says so:
```
curl -fsSL https://openvibe.link/install | sudo sh -s -- --network … --pairing pair_… --code … --desk-host --shared --desktops
```
That sets `desk_shared_kinds: ["browser", "desktop"]` and installs Xvfb, openbox, xdotool, ImageMagick and xterm. On
openvibe.link/machines the owner also turns on "Lend desktops to other people's agents"; OpenVibe.Bot leases a desktop
only where both are on.

- **The whole desktop is inside the desk's sandbox.** A desktop desk (`desk start` with `kind: "desktop"`, named
  `desk-<name>`; never `private`, the machine's own) is the same `openvibe-link desk-sandbox` a browser desk runs in,
  with its own limits (`desk_desktop_memory_mb` 2048, `desk_desktop_cpu_millis` 2000, `desk_desktop_disk_mb` 1024,
  `desk_desktop_pids` 1024). Inside it, the system's own Python runs a small helper (`sandbox_desk.py`) that starts the
  display (Xvfb, in the sandbox's own `/tmp` and network namespace), a window manager, a browser and a terminal, all as
  `openvibe-job`. Nothing of the desktop runs on the machine itself; two desktops share no display, process, file or
  network, and the public internet is all any of them reaches (`desk_egress`). Its terminal gets pseudo-terminals of its own
  (`desk-sandbox --ptys`, v0.65.1: a new devpts instance and `/dev/tty`, nothing of the host's terminals) and a real
  shell (the sandbox user's login shell is nologin); each program's output is kept in the desk's `/tmp` (the helper's
  `logs`), so a desktop that shows nothing says why.
- **Screen, mouse and keyboard only.** `screen` and `input` on a desktop go to the helper over the sandbox's pipe (fds 3
  and 4). Where the sandbox sees the Link's own binary (installed under /usr or /opt), the helper is Go
  (`openvibe-link desk-display`, v0.67): it sends a JPEG already at the asked width and size, and does the mouse and
  keyboard through XTest in-process (measured on one machine: a click 3.7 ms against 103 ms for the Python helper
  forking xdotool); a character with no key on the keymap still goes to xdotool. Since v0.68 it reads the screen
  through MIT-SHM (the X server writes the picture into memory it shares with the helper inside the sandbox: 0.44 ms
  for 1280×800, against 3.2 ms over the socket) and encodes X's own BGRX pixels with libjpeg-turbo, loaded at run time
  so the Link stays one binary without cgo (2.6 ms against 24 ms for Go's encoder). A whole 1280×800 picture is about
  3 ms; the Python helper took 74-82 ms. Where libjpeg-turbo is missing (the installer adds `libturbojpeg0`, or
  `libturbojpeg` on Ubuntu 24.04, with `--desktops`) Go's encoder runs, and where MIT-SHM is refused the socket does;
  the helper's ready message says which (`capture`: `shm` or `socket`, `encoder`: `libjpeg-turbo` or `go`). A narrower
  picture than the desk is shrunk by averaging, so text stays legible. Elsewhere the Python helper (a PNG, the same xdotool
  commands) runs.
- **ov-desk, the Rust helper (v0.74).** Where the sandbox sees `ov-desk` (the installer, the .deb, the .rpm and the Linux
  tarballs put it beside `openvibe-link`), the plugin runs it before the Go helper. It speaks the same protocol, line
  for line ([desk/README.md](../desk/README.md)), and its ready message adds `helper: "ov-desk"` (its own encoder, where
  libjpeg-turbo is missing, is `rust`: 10 ms a 1280×800 picture against 21 ms for Go's). It replaced the Go loop after
  a race on one workstation (2026-11-17). The two ran the same loop on a real 1280×800 desktop frame on Xvfb: MIT-SHM,
  the same shrink, the same libjpeg-turbo call. Each case ran twice for 60 s at 30 a second, in alternating order, then
  flat out:

  | Case | Go: frame p50 / p99, CPU a frame | Rust: frame p50 / p99, CPU a frame | CPU | Memory (Go / Rust) |
  | --- | --- | --- | --- | --- |
  | Full 1280×800, 30 a second | 4.9 / 17.7 ms, 5.74 ms | 5.0 / 17.6 ms, 5.75 ms | same | 17 / 7 MB |
  | Shrunk to 1024, 30 a second | 16.3 / 42.5 ms, 17.93 ms | 11.9 / 26.6 ms, 12.42 ms | −31% | 19 / 9 MB |
  | Full, flat out (217 vs 266 a second) | 3.9 / 12.5 ms, 4.06 ms | 3.5 / 5.9 ms, 3.10 ms | −24% | 17 / 7 MB |
  | Shrunk, flat out (62 vs 118 a second) | 14.1 / 38.5 ms, 15.58 ms | 8.1 / 13.5 ms, 7.76 ms | −50% | 19 / 9 MB |

  Where libjpeg-turbo does the work they tie. Where our own code does (the shrink, frames as fast as they go), Rust
  takes a quarter to half off the CPU and about half off the worst case, in 10 MB less memory. A mouse move (an XTest
  round trip) is 0.04 ms either way. The same crate is where native capture goes next: Windows, macOS and Wayland. The plugin trusts
  nothing the helper answers beyond its shape, its size (32 MB) and its time. `browser` on a desktop is refused (its
  browser is a window there), as are `exec`, `fs`, `screen_stream` and everything else of the machine.
- **What `describe` says.** `desk.kinds` lists `desktop` when it is lent and the machine has everything a desktop needs
  (else only `browser`, and starting one says what is missing), with `window_manager` and `desktop_limits`. A desktop
  counts as a whole desk against `max_desks`.

## Config

| key                  | default        | meaning |
|----------------------|----------------|---------|
| `level`              | `observe`      | `observe`, `ask`, `trusted` or `full` |
| `roots`              | your home      | the folders it may use (absolute) |
| `allow_exec`         | `true`         | `false`: never run programs (no `terminal` capability) |
| `timeout_s`          | 30 (≤ 600)     | the longest a program runs |
| `max_output_bytes`   | 65536 (≤ 192 KB) | stdout and stderr are each cut there |
| `max_read_bytes`     | 262144 (≤ 256 KB) | the most one read or write moves |
| `approval_timeout_s` | 120 (≤ 600)    | how long a request waits for you |
| `notify`             | `true`         | the plugin's own prompt when a request waits and no tray shows it (graphical session only) |
| `probe`              | `true`         | `false`: no probes (OpenVibe.Watch's `node` source) |
| `allow_input`        | `true`         | `false`: never use the mouse or keyboard (no `input` capability) |
| `allow_desk`         | `true`         | `false`: never start a private desk (no `desk` capability) |
| `max_desks`          | memory / 2 GiB, 1 to 8 | how many whole desks run at once (1 to 10); a browser desk counts as half |
| `desk_idle_s`        | `900`          | a desk no command used this long is stopped (60 to 86400) |
| `desk_dir`           | `~/.local/share/openvibe-link/desks` (Windows: `%LOCALAPPDATA%\openvibe-link\desks`) | each desk's own temp and downloads folders |
| `desk_profile_max_mb` | `256`         | a kept profile larger than this is not saved (16 to 4096) |
| `desk_isolation`     | `none`         | `sandbox`: a desk host for other people: browser desks only, each in its own sandbox, nothing of the machine offered (Linux, the Link as root with its worker on) |
| `desk_memory_mb`     | `1024`         | with `sandbox`: each desk's memory (256 to 16384; swap off) |
| `desk_cpu_millis`    | `1000`         | with `sandbox`: each desk's CPU in thousandths of a core (100 to 16000) |
| `desk_disk_mb`       | `512`          | with `sandbox`: each desk's private `/tmp`, its profile and downloads (64 to 16384) |
| `desk_pids`          | `512`          | with `sandbox`: each desk's processes and threads (64 to 8192) |
| `desk_egress`        | `public`       | with `sandbox`: `public` (the public internet only) or `none` |
| `desk_shared_kinds`  | `["browser"]`  | with `sandbox` (v0.65.0): what it lends, `browser` and/or `desktop` (whole desktops, each in its own sandbox) |
| `desk_desktop_memory_mb` | `2048`     | with `desktop` lent: each desktop's memory (512 to 32768; swap off) |
| `desk_desktop_cpu_millis` | `2000`    | with `desktop` lent: each desktop's CPU in thousandths of a core (100 to 16000) |
| `desk_desktop_disk_mb` | `1024`       | with `desktop` lent: each desktop's private `/tmp` (128 to 32768) |
| `desk_desktop_pids`  | `1024`         | with `desktop` lent: each desktop's processes and threads (128 to 8192) |
| `allow_browser`      | `true`         | `false`: no browser (no `browser` capability) |
| `allow_screen_stream` | `true`        | `false`: never show the screen live (`screen.stream` is false) |
| `browser_path`       | found on PATH  | the browser to drive (Chromium, Chrome, Edge or Brave) |
| `browser_profile`    | `~/.local/share/openvibe-link/browser` (Windows: `%LOCALAPPDATA%\openvibe-link\browser`) | its profile folder: the agent's own, unless you choose another |
| `browser_egress`     | `public`       | `public`: every agent browser goes through the egress guard (never your own network below level `full`); `open`: no guard |
| `browser_headless`   | `true`         | `false`: show its window |
| `browser_headless_shell` | `auto`     | `auto`: chrome-headless-shell for a headless browser where it is installed; `off`; or its path |
| `browser_idle_s`     | 600            | close it after this long without a command |

`describe` reports `terminal`, `files`, `screen`, `input` and `browser` with the level and folders, so Bot shows what the machine allows;
telemetry carries the sensors `computer_level` (observe while a stop is latched), `computer_waiting`,
`computer_running` and `computer_screen_stream` (`live` or `off`). `screen.stream` says whether the live screen is
offered.
Every request is logged by the Link (`journalctl -u openvibe-link`), with what it would do, approved or denied.
