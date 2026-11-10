# Link's window (the quick panel and the full window)

Designed 2026-11-07 (domains plan §13.1). `index.html`, `panel.css` and `panel.js` are the whole UI: no build, no
framework, no network fetches beyond the daemon itself. Narrow (the tray's popover, a phone) it stacks; from 720 px it
is two columns and adds your devices and the activity log beside the controls.

## What the daemon serves

The daemon serves this folder at `http://127.0.0.1:<port>/panel/` and opens it with a per-session token in the
fragment: `/panel/#t=<token>`. The page sends that token as `X-Link-Panel` on every request. The daemon refuses:
- anything without the token;
- any request whose `Origin` is not its own loopback origin;
- any bind other than loopback.

| Call | Body | What it does |
|---|---|---|
| `GET /panel/api/state` | — | Everything the window shows (below). Polled every 3 s while the window is visible. |
| `POST /panel/api/pause` | `{ "paused": true }` | The local kill switch: it stops everything at once and always wins over any remote request. |
| `POST /panel/api/running/:id/stop` | — | Stops one running thing. |
| `POST /panel/api/codes/tasks/:id/stop` | — | Stops the local Codes task the window lists. A task that is not running on this computer is refused (`409`). |
| `POST /panel/api/capabilities/:id` | `{ "on": false }` | Turns a capability on or off. Turning one on is the consent step. |
| `POST /panel/api/shares` | `{ "port": 3000 }` | Shares a local port through Connect, behind the person's sign-in. |
| `DELETE /panel/api/shares/:port` | — | Ends that share. |
| `GET /panel/api/send/targets` | — | The person's other paired machines with receive keys. |
| `GET /panel/api/send/inbox` | — | Pending encrypted items, shown with Accept and Decline. |
| `GET /panel/api/send/key` | — | This machine's Locker private key, returned only to the token-protected loopback window. |
| `POST /panel/api/files` | `{ "target_node": "lnk_…", "envelope": "base64" }` | Relays Locker ciphertext to one of the person's other machines. |
| `POST /panel/api/files/:id/accept` | — | Returns ciphertext for local Locker opening after the person chooses Accept. |
| `POST /panel/api/files/:id/decline` | — | Removes the incoming item. |
| `POST /panel/api/files/:id/complete` | — | Removes the accepted item after download starts. |
| `POST /panel/api/first-run` | `{ "uses": ["codes", "browser"], "skip": false }` | The first-run answer. Uses are `codes`, `browser`, `models`, `earn` and `vibe`. The daemon turns on what each says (`earn` stays off until it is confirmed on Exchange) and stops asking; `skip: true` only stops asking. |
| `POST /panel/api/clipboard` | `{ "on": true }` | The per-machine clipboard-sharing switch, off by default; text only, sealed like every other item. |
| `POST /panel/api/send/auto-accept` | `{ "on": true }` | Save incoming items from the person's own machines without asking, per machine. Off by default. |

## `GET /panel/api/state`

Every key is optional. A section with nothing in it is left out; the window never shows sample data.

```json
{
  "machine": { "id": "dev_01M6Q1Z8V4T7R2N5K3B9C6D0EF", "name": "Workshop desktop", "os": "linux", "online": true, "paused": false, "needs_you": 1 },
  "running": [{ "id": "r_1", "kind": "agent", "title": "Vibe is comparing three hosting plans", "detail": "Browser", "started_at": "2026-11-07T10:02:00Z" }],
  "capabilities": [{ "id": "browser", "name": "A browser for your agents", "who": "Your agents, after you approve each task", "on": true }],
  "shares": [{ "port": 3000, "url": "https://dev--alex.openvibe.website", "expires_at": "2026-11-07T22:00:00Z" }],
  "devices": [{ "name": "Workshop desktop", "kind": "desktop", "online": true, "here": true }],
  "gpu": { "name": "RTX 3090", "vram_gb": 24, "models": [{ "name": "Qwen2.5 14B", "tps": 41, "loaded": true }] },
  "earnings_today": { "coins": 120 },
  "codes": {
    "agents": [{ "id": "claude-code", "name": "Claude Code", "state": "ready", "version": "2.1.3" }, { "id": "codex", "name": "Codex", "state": "signin", "hint": "codex login" }],
    "tasks": [{ "id": "tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF", "title": "Make every page work at 320 px", "folder": "~/code/openvibe-site", "agent": "Claude Code", "state": "running", "started_at": "2026-11-07T09:52:00Z", "url": "https://openvibe.codes/tasks/tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF" }]
  },
  "activity": [{ "at": "2026-11-07T10:02:00Z", "text": "Vibe opened a browser to compare hosting plans" }],
  "clipboard": false,
  "auto_accept": false
}
```

- `machine.id` is the machine's own id after pairing, so "New task on this computer" can preselect it.
- `running[].kind` is `agent`, `desk`, `share` or `model`.
- `codes.agents[].state` is `ready`, `signin` (then `hint` is the CLI's own sign-in command) or `missing`.
- `codes.tasks[]` are the Codes jobs this worker ran today: `state` is `running`, `waiting`, `done`, `failed` or
  `stopped`; `folder` shows the home directory as `~`; `url` is only ever `https://openvibe.codes/tasks/<id>`.
- `machine.needs_you` counts what waits for the person: it puts a dot on the ring.
- `first_run: true` shows the "What's this computer for?" question at the top, until it is answered or skipped.
- The ring shows:
  - green when online;
  - blue and filling while things run;
  - a slash when paused;
  - grey while not connected.

`testdata/state.json` is a full example. The preview and the tests serve it as the answer.
