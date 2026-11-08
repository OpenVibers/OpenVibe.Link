# Link's own window

`openvibe-node` serves a small page on the machine itself: what runs here, what this computer allows, the shares it
keeps, and what happened. The design and the JSON it reads are in
[`internal/panel/ui/README.md`](../internal/panel/ui/README.md); this page is how it is served and what protects it.

## Opening it

- The tray's **Open Link** (the first item) opens it. The first time the tray runs, it opens by itself.
- `openvibe-link panel` prints its address for a terminal without a tray.

The address is `http://127.0.0.1:<port>/panel/#t=<token>`: the port is picked free at each start and the token is new
for every run (32 random bytes). The token is in the fragment, so the browser never sends it in a request or in a
`Referer`.

## What protects it

- **Loopback only.** It binds `127.0.0.1`; it never binds `0.0.0.0` or another interface. There is no TLS: the traffic
  does not leave the machine.
- **A token on every API call.** `/panel/api/*` needs `X-Link-Panel: <token>`; without it, or with another value, the
  answer is `403`. Static files (`/panel/`, `panel.css`, `panel.js`) need no token: they are public code.
- **Its own origin only.** When a request carries an `Origin`, it must be the panel's own loopback origin. A page on
  another site cannot call the API, even if it guesses the port.
- **No CORS.** The daemon writes no `Access-Control-*` header at all, so no other origin can read an answer.
- **No listing.** Only the three files are served; the folder's `README.md` and `testdata/` are not embedded in the
  binary and answer `404`.

A restart makes a new port and a new token: an old link stops working.

## The API

| Call | What it does | Today |
|---|---|---|
| `GET /panel/api/state` | Everything the page shows | Machine (with its paired id), running (waiting approvals, jobs, connected shares, desks), capabilities, shares, Codes (the agent CLIs here and the worker's tasks today), activity |
| `POST /panel/api/pause` `{ "paused": true }` | The local kill switch, the same latch as `openvibe-link stop` / the tray | Real |
| `POST /panel/api/running/:id/stop` | Stops one running thing | Waiting approvals (deny), jobs (cancel), shares (end) |
| `POST /panel/api/codes/tasks/:id/stop` | Stops a local Codes task | Real: cancels the Run job in the worker, the same way Codes' own stop does; `409` when it is not running here |
| `POST /panel/api/capabilities/:id` `{ "on": false }` | Turns a capability off here | Real; persisted in `<state>/panel.json` |
| `POST /panel/api/shares` `{ "port": 3000 }` | Shares a port through Connect | Real for a share Connect already picked for this computer |
| `DELETE /panel/api/shares/:port` | Ends that share | Real |
| `POST /panel/api/first-run` `{ "uses": ["codes", "browser"] }` | The first-run answer: records it in `<state>/panel.json`, turns on a browser (`browser`) or the AI plugin (`models`), and stops asking. `codes` and `earn` are recorded only (Codes has no module switch yet, and Exchange confirms earning); `vibe` turns nothing on; `skip: true` only stops asking | Real; never turns on Terminal or Desktop |
| `POST /panel/api/files` | Sends files to your other devices | `501` with a reason: not in the daemon yet |

`state` omits what the Node cannot fill truthfully: `devices` (the paired account's machine list is not read here),
`gpu` (no GPU detection), and `earnings_today`. `activity` is the Node's own record of what it saw (an approval
waiting or answered, a pause, a share, a capability), newest first, at most 30.

`codes` is the desktop's Codes module: `agents` are the coding CLIs on this machine, read the way OpenVibe.Codes reads
them (installed on PATH, signed in by the CLI's own login file, the version the binary reports); `tasks` are the jobs
the Codes worker ran here today, kept in `<state>/codes-tasks.json` so an ended task still shows after a restart. A
folder under the home directory is written as `~/…`, and a task's link is only ever
`https://openvibe.codes/tasks/<id>`.

## Capabilities

Each card is a permission switch the daemon already has, from the `computer` plugin's `describe` (`terminal`, `files`,
`screen`, `input`, `browser`, `desk`) and the `ai` plugin (`ai`). A switch is **on** when the plugin offers it, the
computer is not at level `observe`, and it was not turned off here.

Turning one **off** adds a local restriction the Node enforces itself: a command of that kind is refused with
`not_allowed` before it reaches the plugin. Turning it **on** removes only that restriction; it never grants more than
the plugin already offers, and it cannot raise the computer's level.

**Desktop (`desk`) and Terminal (`terminal`) are gated** on the 11-05 review fixes: they may be turned off here but
never on (`409` with the reason). At level `observe` nothing can be turned on from the panel: the level is set on the
machine.
