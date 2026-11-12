# AGENTS.md — OpenVibe.Link

## Project Overview

OpenVibe.Link ("OpenVibe Node") connects a person's computers, servers, Raspberry Pis, phones and robots to the
OpenVibe network. It pairs once, keeps **one outbound WebSocket** to OpenVibe.Bot (credential in the `Authorization`
header only), opens **no ports**, and lets OpenVibe.Bot (robots) and OpenVibe.Actor (computer control for agents) use
the device at the level the owner sets. It is a **client**, not a service: no database, no server, no deploy of its own.

- **Kind:** client · **Version:** 0.59.0 · **License:** AGPL-3.0 · **Runtime:** Go 1.24 (one static binary per
  platform), Python 3 plugins, an ESP32 Arduino library, an Android app (gomobile + Kotlin/Compose)
- **Read first:** [README.md](README.md), [STATUS.json](STATUS.json), [docs/protocol.md](docs/protocol.md),
  [docs/plugins.md](docs/plugins.md), [docs/computer.md](docs/computer.md), [docs/panel.md](docs/panel.md),
  [docs/worker.md](docs/worker.md), [docs/access-and-agents.md](docs/access-and-agents.md),
  [ADR-043](docs/ADR-043-bot-devices-and-control.md).

**Network context:** pairing and the device credential are OpenVibe.Bot's (or Network's pairing flow); Network owns the
account, roles, signed access grants (`access request/approve/revoke`) and the machines hub; Actor sends computer
commands through Bot; Codes' job runner sends jobs to the worker; Connect carries port shares; Exchange lists a local
model; Locker/Send move sealed files and text between the person's machines; Live receives WHIP video; Git serves the
clone/git-connect relay. Link never becomes an authority for any of them.

## Commands

```bash
go vet ./... && go test -race ./...                    # the e2e tests run the Python dry-run plugin
pip install pytest pillow numpy                        # plugin test deps
for p in sdk dryrun adeept_adr036 cozmo relay ros2 computer; do python3 -m pytest -q plugins/$p/tests; done
scripts/dist.sh v0.0.0-dev                             # every binary, plugin bundle and SHA256SUMS in dist/
scripts/package.sh                                     # distro packages
go build ./cmd/openvibe-link                           # one binary
openvibe-link            # guided first run: pair, check the link, report the first job
openvibe-link status | stop | resume | quit | approvals | tray | panel
openvibe-link memory scan|upload|sync | skills sync | exchange serve --model <id> --public-url <https>
openvibe-link share <port> | share add | party host <party-id>
openvibe-link access request|list|approve|deny|revoke ...  |  openvibe-link mcp [--print-config]
openvibe-link clone '<link>' | git connect '<link>'
openvibe-link profile verify community.<name> [--key-id <id>]
```

Install: `curl -fsSL https://openvibe.link/install | sh` (and `--robot/--driver`, `--network/--pairing/--code`,
`--computer`, `--ai`, `--worker`, `--plan-logins`). CI builds Windows x64/ARM64, macOS arm64/Intel, Linux x64/ARM64
(`.deb`, `.rpm`, tarball, install script) and the Android APK; releases carry SHA256SUMS only (no code signing yet).

## Architecture at a Glance

- **Core Go binary** ([cmd/openvibe-link/](cmd/openvibe-link/main.go), [internal/](internal/)): pairing, the outbound
  WebSocket, heartbeats/deadman, the safety rules (deadlines, owner limits clamped before any driver sees a value, a
  persisted e-stop, the local kill switch), the plugin supervisor, WHIP video (pion), the system service
  (systemd/launchd/Windows).
- **Plugins** ([plugins/](plugins/), separate processes, JSON lines over stdin/stdout): `dryrun`,
  `adeept_adr036` (Adeept 4WD), `cozmo` (PyCozmo), `relay` (existing Live hardware clients), `ros2` (rclpy or
  rosbridge: cmd_vel, battery, scan/range, odometry, camera), `computer` (terminal/files/screen/input/browser), plus the
  community LOOI driver contract. A plugin stops its own actuators when stdin closes, at a command deadline and after
  1 s without a heartbeat — the core dying never leaves motors running.
- **ESP32 library** ([esp32/](esp32/README.md), `OpenVibeLink`): pairing, control link, drive/actuator commands,
  telemetry, heartbeat, deadman and e-stop; no jobs or video.
- **Computer plugin** ([docs/computer.md](docs/computer.md), [internal/agent/](internal/agent/)): `--computer
  observe|ask|trusted|full`; runs as the person's account; approvals in the tray / notification / `openvibe-link
  approve`; browser over DevTools `--remote-debugging-pipe` (no port); exact-origin task checks; nonce-fenced page
  text; a live screen on openvibe.bot for its owner only (≤30 min); `openvibe-link stop` ends it at once.
- **Worker** ([docs/worker.md](docs/worker.md), [internal/agent/jobs.go](internal/agent/jobs.go)): `--worker` runs
  each job as an unprivileged user in a sandbox; Codes' runner can start a task from any browser; `--plan-logins` names
  existing login files without reading/printing them.
- **Network roles:** `codes.worker`, `desks.host`, `ai.host`, `bot.robot` are read at check-in and kept, but the local
  configuration always wins — a role never installs anything or turns on a feature turned off here.
- **Panel** ([docs/panel.md](docs/panel.md), [internal/panel/](internal/panel/)): `127.0.0.1`-only, a fresh random port
  and 32-byte token per run (`#t=` fragment), every `/panel/api/*` needs `X-Link-Panel`, Origin must be the panel's own,
  no CORS, only three files served. A restart invalidates the old address/token.
- **Sharing/relay:** `share`/`share add` (Connect), `party host`, `clone`/`git connect` (Git, outbound), Locker-sealed
  Send between machines, the access-grant relay (six scopes), `mcp` (stdio + local control socket) for agent CLIs,
  memory/skills sync (dry run, one marked block per file, opt-in per tool).
- **Android** ([android/](android/), [docs/android.md](docs/android.md)): v0 pairing/connection in Kotlin/Compose;
  tokens + pairing AES-GCM-sealed with a non-exportable Keystore key; approvals (fingerprint for money, passwords,
  deletions and a new recipient), read-only Dock activity, **Send to OpenVibe**; no AccessibilityService and no screen
  capture.

## Safety, Egress and Security

- **The kill switch is the promise:** `openvibe-link stop` latches (persisted e-stop); `resume` clears it. Every
  actuator path checks deadlines and clamped owner limits before a driver sees a value; the deadman/heartbeat stops
  motion when the control link is lost.
- **No open ports:** the core connects out; the panel is loopback-only with a per-run token; the browser is driven over
  a pipe; `git connect`, clone, shares and the worker all connect out.
- **Browsing egress** ([docs/link-egress.md](docs/link-egress.md)): public web destinations on 80/443 only, DNS
  resolved and pinned on the device, private/local/metadata/IPv6-translation addresses refused; per-owner limits,
  opt-out and a monthly cap; Network checks every stream; no cookie or ordinary share key grants it; a missing Network
  **never** means direct fallback; on expiry/revocation/opt-out/battery/metered it closes within 5 s.
- **Access grants** (`openvibe-link access`): the target verifies Network's signed grant (signature, audience, sender,
  scope, expiry, live revocation) and its own level; the owner may narrow but never widen; a missing/expired/revoked
  grant fails closed. Relayed ops go through Bot's device relay; Link Send's LAN hand-over checks the same grant.
- **Community plugins/profiles:** a `community.<name>` profile is reviewed and signed by Bot (Ed25519 over canonical
  JSON); `profile verify` checks it against Bot's published key (pin with `--key-id`), and pairing with a robot on a
  community profile runs the same check and warns. Install a community plugin only when it passes.
- **Secrets:** the device credential appears only in the `Authorization` header; Android seals it with the Keystore;
  never log a token, a login file's content or a sealed envelope. The panel's token is in the fragment and must stay
  there.
- **Local configuration wins** over a network role; a role is a description, not a remote installer.

## Deployment and Data

None of its own: people install it on their machines/robots (install script or release binary), it pairs through
OpenVibe.Bot/Network and runs as a system service. There is no database; local state lives in the Link's state
directory (config, e-stop latch, panel.json, usage/reservations, memory/skills settings). Production is a distribution
matter, not a service deploy.

## Key Pitfalls

- **Never add an inbound listener** (other than the loopback panel) or a shared secret/key. Pairing is a one-time code
  and the credential goes in the `Authorization` header only.
- **Never let a network role or a remote command turn on a local feature**; the local config is the upper bound.
  Approvals at `ask` must be answerable (tray/notification/CLI) and deny by default.
- **A plugin that keeps moving when its stdin closes is a safety bug.** Every driver must stop on stdin close, deadline
  and 1 s without a heartbeat; keep the persisted e-stop checked on every path.
- **Computer/browser:** exact-origin checks for task origins, no direct DNS/QUIC/non-proxied WebRTC UDP in the shared
  browser, nonce-fenced page text, and no port. Do not "simplify" the DevTools pipe into a TCP debug port.
- **Egress failure is closed:** a Network/Bot outage must never fall back to direct browsing or a relay that bypasses
  the owner's cap; the saved offline choice ("use OpenVibe's address or wait") applies only when the Link connection
  cannot be made.
- **Panel:** loopback only, token on every API call, Origin checked, no CORS, no directory listing; a restart must
  rotate the port and token.
- **Worker:** run jobs as an unprivileged user in the sandbox; never read or print a login file's content; a job that
  a person did not install a role for must not run.
- **Community profiles:** verify the Ed25519 signature and plugin name before running a community driver; a failed
  check is a warning/refusal, not a shrug.
- **Android v0** has no automation: no AccessibilityService, no screen capture. Do not add one without the owner's
  decision and a security review.
- **Releases:** CI builds every platform; the release has SHA256SUMS only (no signing/notarization, no auto-update).
  Do not claim a store/signed build.
- `settings`/memory/skills sync must stay opt-in per tool, dry-run first, one marked block per file, with an undo; never
  overwrite a tool's own content.

## Testing

`go vet ./... && go test -race ./...` covers the core, pairing, safety, protocol, worker, panel, access grants, Git
relay, memory/skills and the e2e flow against [internal/fakebot](internal/fakebot) and
[internal/fakenetwork](internal/fakenetwork). Plugin tests are Python (`pytest` per plugin; the e2e Go tests run the
dry-run plugin). `scripts/dist.sh` proves every binary/plugin bundle builds. Add a test with every safety, grant or
egress change; a skipped test is not a pass.

## Documentation

- [README.md](README.md) — install, what is in here, development.
- [docs/protocol.md](docs/protocol.md) — the wire format with Bot; [docs/plugins.md](docs/plugins.md) — driver protocol,
  bundled drivers, community plugins; [docs/install.md](docs/install.md); [docs/computer.md](docs/computer.md);
  [docs/panel.md](docs/panel.md); [docs/worker.md](docs/worker.md); [docs/access-and-agents.md](docs/access-and-agents.md);
  [docs/connect.md](docs/connect.md); [docs/party-desktop.md](docs/party-desktop.md); [docs/ai.md](docs/ai.md);
  [docs/link-egress.md](docs/link-egress.md); [docs/stream.md](docs/stream.md); [docs/voice.md](docs/voice.md);
  [docs/memory.md](docs/memory.md); [docs/skills.md](docs/skills.md); [docs/android.md](docs/android.md);
  [docs/hardware-adeept.md](docs/hardware-adeept.md), [docs/hardware-cozmo.md](docs/hardware-cozmo.md),
  [docs/hardware-looi.md](docs/hardware-looi.md); [ADR-043](docs/ADR-043-bot-devices-and-control.md).
- [SECURITY.md](SECURITY.md), [STATUS.json](STATUS.json), [esp32/README.md](esp32/README.md),
  [plugins/](plugins/) READMEs.
