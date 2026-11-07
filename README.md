# OpenVibe Node

One service for your computers, servers, Raspberry Pis, phones and robots: pair it once with OpenVibe and products
like **OpenVibe.Bot** (robots: drive, cameras, sensors, with safety) and **OpenVibe.Actor** (computer control for AI
agents, at the level you set on the machine) can use the device. It connects out, needs no open ports, and keeps a local kill switch.

On a robot's Raspberry Pi (or other Linux computer), the one command:

```sh
curl -fsSL https://node.openvibe.network/install | sh
```

The same installer is also at <https://openvibe.bot/install>, which keeps working; `node.openvibe.network` is the
Node's own address, where it also has a page that says what it is and how to install it.

It turns on I2C and SPI, installs the camera tools, finds the kit (an Adeept ADR036 4WD, a ROS 2 robot), shows a code
to approve at openvibe.bot/link, and pairs: the robot is made, drivable on its panel and, if you chose it, live on your
OpenVibe.Live channel ([docs/install.md](docs/install.md#the-one-command-raspberry-pi-and-other-linux-robots)).

With a robot already added on openvibe.bot:

```sh
curl -fsSL https://node.openvibe.network/install | sh -s -- --robot rob_… --code ABCD-1234 --driver adeept
```

openvibe.bot shows this command with the robot's id and a fresh code. `--driver` is `adeept`, `adeept-mecanum`, `cozmo`, `ros2`,
`looi` (the community's Generic LOOI driver, [docs/hardware-looi.md](docs/hardware-looi.md)) or `none` (dry run; the old `--robot adeept` form still works but is deprecated). A robot paired through
OpenVibe.Network gets `--network <URL> --pairing pair_… --code ABCD-1234` instead of `--robot`. Details, manual
install, Windows and configuration: [docs/install.md](docs/install.md).

**Put a port on the web** with [OpenVibe Connect](https://connect.openvibe.website): `openvibe-node share 8790`
serves the share this computer was picked for at `https://<name>--<you>.openvibe.website` behind OpenVibe sign-in (or
`openvibe-node share --key ovc_… --port 8790` with a key), and `openvibe-node share add` keeps it up with the service;
`openvibe-node status` lists each share ([docs/connect.md](docs/connect.md)).

**Sell a local model** on [OpenVibe.Exchange](https://openvibe.exchange):
`openvibe-node exchange serve --model qwen3:32b --public-url https://gpu.example.com/v1`. It serves the model from the
local OpenAI-compatible server (Ollama by default at `127.0.0.1:11434`), registers the public https address through the
Exchange's endpoint-seller flow with your `OPENVIBE_DEVELOPER_KEY`, and removes the offer when you stop it with Ctrl-C.
The public URL must be reachable by the Exchange and is judged by its egress guard; keep it behind your own tunnel or
reverse proxy. The local listener has no buyer authentication (the Exchange's endpoint flow sends no buyer identity), so
bind it to loopback, which is the default.

```sh
openvibe-node            # guided first run: pair, check the link, report this machine's first job
openvibe-node status     # link, plugins, video, battery, shares, stop latch
openvibe-node stop       # kill switch: stop every motor now and keep it stopped
openvibe-node resume
openvibe-node quit       # end the node cleanly (nothing latched), e.g. from a stream launcher
openvibe-node approvals  # the computer plugin at level ask: what waits; approve <id> / deny <id>
openvibe-node tray       # the same in your notification area, with Approve/Deny notifications (Linux, Windows)
openvibe-node memory scan  # what your coding tools remember: read-only, local, secrets removed
openvibe-node memory upload  # send what they remember to your own memory store, after you say yes
openvibe-node memory sync  # write your profile and rules back into the tools, one marked block per file, opt-in per tool
openvibe-node skills sync  # write the skills and rules you chose at openvibe.codes/skills into each tool, with a dry run and undo
openvibe-node exchange serve --model qwen3:32b --public-url https://gpu.example.com/v1  # sell a local model
```

## What is in here

- **Core** (Go, one static binary per platform): pairing, one outbound WebSocket to `wss://openvibe.bot/device`
  (credential in the `Authorization` header only), heartbeats and a deadman, the safety rules (deadlines, owner limits
  clamped before any driver sees a value, a persisted e-stop, the local kill switch), the plugin supervisor, WHIP video
  publishing with pion, and the system service (systemd, launchd, Windows).
- **Plugins** (separate processes, JSON lines over stdin/stdout): [`dryrun`](plugins/dryrun), the Adeept 4WD Smart Car
  Kit [`adeept_adr036`](plugins/adeept_adr036/README.md), [`cozmo`](plugins/cozmo/README.md) through PyCozmo, and
  [`relay`](plugins/relay) for existing Live hardware clients, and [`ros2`](plugins/ros2/README.md) for ROS 2 robots
  (`cmd_vel`, battery, scan/range, odometry and camera, as a node with rclpy or through rosbridge). LOOI's driver is the community's
  ([docs/hardware-looi.md](docs/hardware-looi.md) is its contract). Each plugin stops its own actuators when stdin closes,
  at a command's deadline and after 1 s without a heartbeat, so the core dying never leaves motors running.
- **ESP32 library** ([`esp32/`](esp32/README.md), `OpenVibeNode`): the device side of the protocol for ESP32 boards —
  pairing, the control link, drive/actuator commands, telemetry, heartbeat, deadman and e-stop — but no jobs or video.

**Community robots:** a profile people submit on openvibe.bot (`community.<name>`) is reviewed by an OpenVibe admin
and signed by Bot (Ed25519 over its canonical JSON). `openvibe-node profile verify community.<name>` checks that
signature against the key Bot publishes (`--key-id` pins one) and names the plugin the robots run; pairing with a robot
on a community profile runs the same check and warns when it fails. Install a community plugin only when it passes
([docs/plugins.md](docs/plugins.md#community-plugins)).

**Your computer for your agents:** `--computer observe|ask|trusted|full` adds the `computer` plugin (terminal, files,
screen, mouse and keyboard, and a browser of its own for OpenVibe.Actor), running as your own account, at the level you set on the machine; `ask` waits for
your answer in the tray, a notification's Approve button or `openvibe-node approve <id>` ([docs/computer.md](docs/computer.md)).
With it on, you can also watch the machine's screen live on openvibe.bot. Only you see it, it runs for up to 30
minutes, and `openvibe-node stop` ends it at once ([the live screen](docs/computer.md#the-live-screen-screen_stream-node-v0120)).

**Your own models for your OpenVibe work:** the `ai` plugin lets OpenVibe use a model you already run at home (Ollama,
LM Studio or llama.cpp's server) for your own work, at no cost and with no open port, once you turn on "Let my AI use
this computer's models" on openvibe.bot/machines ([docs/ai.md](docs/ai.md)). The installer sets it up with `--ai`.

**A machine that runs your jobs:** add it on openvibe.bot/machines, which prints the same installer with `--worker`: the
job worker runs each job as an unprivileged user in a sandbox, and the OpenVibe.Codes job runner lets you start a coding
task on openvibe.codes/run from any browser and have this machine do it ([docs/worker.md](docs/worker.md)). Add
`--plan-logins` to use the coding plans you already signed in for here: it names those login files for your jobs, sets
the service's HOME to your home, and says how to sign in a tool that is not ready, without ever reading or printing a
login's content.

**What the machine does for the network:** the owner sets the machine's roles on openvibe.network/machines
(`codes.worker`, `desks.host`, `ai.host`, `bot.robot`). The Node reads them at its check-in, keeps them, and applies
the ones this machine was installed for: the job worker starts if the local configuration has it, and the machine says
plainly which role it could not honour. The local configuration always wins: a role never installs anything and never
turns on a feature you turned off here.

**Link's own window:** the tray's **Open Link** (or `openvibe-node panel`) opens a small page on the machine itself
at `127.0.0.1` that shows what runs here, what this computer allows, your shares, the coding-agent CLIs and the Codes
tasks this machine ran today, and what happened, and lets you pause everything, stop one thing, turn a capability off,
share a port, or send files (not in this version). It is served only on loopback, with a per-run token in the address
fragment that every API call must send; see [docs/panel.md](docs/panel.md) for the security rules.

Docs: [protocol](docs/protocol.md) (the wire format with OpenVibe.Bot), [plugins](docs/plugins.md) (the driver
protocol, the bundled drivers, and where Actor's computer-control plugin attaches), [install](docs/install.md),
[panel](docs/panel.md) (Link's own window and its security rules),
[memory](docs/memory.md) (what the coding tools on a machine remember, how the Node reads it and how it is uploaded to
your own memory store), [skills](docs/skills.md) (the skills hub: which folders each tool reads, the marked block, the
dry run and the undo).
Decisions: [ADR-043](docs/ADR-043-bot-devices-and-control.md).

## Development

```sh
go vet ./... && go test -race ./...                    # the end-to-end tests run the Python dry-run plugin
pip install pytest pillow numpy
for p in sdk dryrun adeept_adr036 cozmo relay ros2 computer; do python3 -m pytest -q plugins/$p/tests; done
scripts/dist.sh v0.0.0-dev                             # every binary, the plugin bundle and SHA256SUMS in dist/
```

`internal/fakebot` is a fake OpenVibe.Bot (pairing, device WebSocket, WHIP) used by the tests. Licence: AGPL-3.0
([LICENSE](LICENSE)). Security reports: [SECURITY.md](SECURITY.md).
