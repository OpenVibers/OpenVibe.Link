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
| `POST /panel/api/capabilities/:id` | `{ "on": false }` | Turns a capability on or off. Turning one on is the consent step. |
| `POST /panel/api/shares` | `{ "port": 3000 }` | Shares a local port through Connect, behind the person's sign-in. |
| `DELETE /panel/api/shares/:port` | — | Ends that share. |
| `POST /panel/api/files` | multipart `file`… | Sends the files to the person's other devices. |
| `POST /panel/api/first-run` | `{ "uses": ["codes", "browser"], "skip": false }` | The first-run answer. Uses are `codes`, `browser`, `models`, `earn` and `vibe`. The daemon turns on what each says (`earn` stays off until it is confirmed on Exchange) and stops asking; `skip: true` only stops asking. |

## `GET /panel/api/state`

Every key is optional. A section with nothing in it is left out; the window never shows sample data.

```json
{
  "machine": { "name": "Workshop desktop", "os": "linux", "online": true, "paused": false, "needs_you": 1 },
  "running": [{ "id": "r_1", "kind": "agent", "title": "Vibe is comparing three hosting plans", "detail": "Browser", "started_at": "2026-11-07T10:02:00Z" }],
  "capabilities": [{ "id": "browser", "name": "A browser for your agents", "who": "Your agents, after you approve each task", "on": true }],
  "shares": [{ "port": 3000, "url": "https://dev--alex.openvibe.website", "expires_at": "2026-11-07T22:00:00Z" }],
  "devices": [{ "name": "Workshop desktop", "kind": "desktop", "online": true, "here": true }],
  "gpu": { "name": "RTX 3090", "vram_gb": 24, "models": [{ "name": "Qwen2.5 14B", "tps": 41, "loaded": true }] },
  "earnings_today": { "coins": 120 },
  "activity": [{ "at": "2026-11-07T10:02:00Z", "text": "Vibe opened a browser to compare hosting plans" }]
}
```

- `running[].kind` is `agent`, `desk`, `share` or `model`.
- `machine.needs_you` counts what waits for the person: it puts a dot on the ring.
- `first_run: true` shows the "What's this computer for?" question at the top, until it is answered or skipped.
- The ring shows:
  - green when online;
  - blue and filling while things run;
  - a slash when paused;
  - grey while not connected.

`testdata/state.json` is a full example. The preview and the tests serve it as the answer.
