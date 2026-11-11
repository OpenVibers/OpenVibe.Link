# LOOI on the OpenVibe Link

LOOI is the tracked desk robot whose face is a phone. Its base talks Bluetooth Low Energy, so it is a **bridge**
robot: the Link runs on a computer next to it (Windows, macOS or Linux), and a driver plugin drives the base over
Bluetooth. The phone streams its own video, for example from its browser to the owner's OpenVibe.Live channel, so the
Link publishes none.

The Generic LOOI driver is written by the community (LOOI owners), not bundled with the Link. This page is the
contract it implements and how to run it.

## On openvibe.bot

Add a robot with the **LOOI desktop robot** profile (`looi`). Its panel has a drive pad and a head-tilt control, and
it shows battery and link latency. The profile's limits apply before any value reaches the driver: speed at most 0.5,
turn at most 0.6, every motion command lasts at most 300 ms unless it is re-sent, heartbeat 1 s. The owner can lower
them per robot.

Who may drive is Bot's job, not the driver's: the owner, invited operators, an optional public queue with timed
turns, cooldowns and per-role allowed commands, all enforced by Bot's command gate before a command reaches the Link.
The e-stop is latched in Bot and only the owner clears it. Each command tells the driver who sent it (`cmd.operator`,
`{"subject", "role"}`), and when a queue turn ends or its holder leaves Bot halts the robot before the next person
drives (the driver gets `stop`), so commands from the last driver never come after it
([plugins.md](plugins.md#core--plugin)). The owner or an operator driving during someone's turn pauses that turn and
halts the robot first, the same way. Each command also carries `cmd.turn_epoch`: the core already refuses one older
than the newest halt's, and a driver may check it again against its own record. Commands are never redelivered: Bot
sends each once under a new id, the Link passes it on at most once and never after its deadline, and each carries
`cmd.provenance` (the link session, the frame's seq, Bot's send time and deadline). The Python runtime refuses a
replay on a session and a command from an ended turn by itself
([plugins.md](plugins.md#what-a-driver-can-rely-on-commands-are-never-redelivered)).

To show the panel under a Live stream, press **Put the buttons under my OpenVibe.Live stream** on the robot's page on
openvibe.bot: it shows the panel where it is embedded and opens your channel to confirm. The channel page then shows
the robot's panel beside the phone's stream, and viewers signed in on Live press Drive there.

## What the driver receives

The driver is a Link plugin ([plugins.md](plugins.md)): JSON lines over stdin and stdout. For `looi` it receives:

| command | value | meaning |
|---|---|---|
| `drive` | `{"throttle": -1..1, "steer": -1..1}` | tracks: left = throttle + steer, right = throttle − steer (steer > 0 turns right), normalised so neither exceeds 1 |
| `actuator` | `{"name": "head", "value": -1..1}` | head tilt, -1 fully down, 1 fully up |
| `headlight` | `{"on": true}` / `{"on": false}` | the headlights, explicit on or off; not motion, never a toggle |
| `halt` | — | stop everything now; it always passes, even while stopped |

Every `drive` has a `deadline_ms`. The tracks must stop when it passes without a newer command (the panel re-sends a
held control every 150 ms). With the Python runtime (`openvibe_plugin`, [plugins/sdk](../plugins/sdk)) set
`motion_kinds = {"drive"}`, and it enforces the deadline, the 1 s heartbeat, stdin closing, `stop`, `estop` and
`halt` for you.

## Headlights

The owner and the People Operators the owner allows get one labelled Headlights button beside the head slider on the
LOOI panel (the full panel and the channel's Robot tab; never the overlay or the one-row controls, and never an
anonymous viewer or a plain watcher). A press sends Bot `{"kind":"headlight","value":{"on":true|false}}`; Bot's gate
decides the role and stamps the trusted fields, and the browser never sends a subject, role, deadline or provenance.

What the driver receives from OpenVibe Link (the note's exact non-motion envelope; `deadline_ms` and
`expires_at_mono_ns` are filled by the Link at write time):

```json
{
  "op": "command",
  "id": "cmd_…",
  "kind": "headlight",
  "value": {"on": true},
  "deadline_ms": 250,
  "expires_at_mono_ns": 123456789012345,
  "operator": {"subject": "usr_…", "role": "owner"},
  "turn_epoch": 1738065600100,
  "provenance": {
    "session": "sess_…",
    "seq": 7,
    "issued_at_ms": 1738065600000,
    "expires_at_ms": 1738065600300,
    "clock_uncertainty_ms": 12
  }
}
```

- The numbers are the contract: 300 ms original lifetime, a deadline of at most 250 ms, and at least 200 ms of the
  original lifetime left when the Link reads the line (Link nacks `expired` otherwise). It is not a motion kind and
  must never renew a drive/head deadline.
- At most two are accepted per rolling second on the machine, shared with the owner's local control; Link/plugin
  must not retry automatically.
- An accepted command is an `ack` with `data` `{"stage":"headlight_pending","admitted":true}`: accepted, **not
  confirmed**. A busy or rate refusal is an `ack` with `data` `{"stage":"headlight_refused","admitted":false}`: the
  panel says "Busy, try again in a moment." and keeps its displayed state. A `nack` is a rejection and the panel
  says "The robot refused: <reason>".
- Telemetry: `sensors.p7_headlight` {`commanded`: `true`/`false`/`null`, `pending`: boolean,
  `physical_state_verified`: `false`}. `commanded` is what was asked, not what the robot did; the panel labels null
  Unknown, shows Turning on…/Turning off… while pending, and keeps "Not confirmed on the robot" beside the state.
- Access is Bot's, not the driver's: the owner, and People Operators when the owner's allow list gives them
  headlights. Remote light also needs the driver's own preconditions (its Go Live enrollment, ARMED, STOP clear and
  no pending neutral/head work). It grants no motion and nothing while the robot is stopped.

The Node and panel route is exercised in Link's tests with a fake Bot and a fake headlight driver; the real
Bluetooth frames and the physical state are still unverified on a robot. The community information FED2 03=ON /
00=OFF is not verified by this contract. Test on the robot: the physical light changes, STOP/busy leave it as it was,
and a lost Link never leaves it on.

The deadline is the original one on every OS, Windows included (Link 0.30): the Link passes the instant a command
expires on the clock it shares with the driver, so time the line spends in the pipe counts against it. For live motion,
also set `strict_timing = True` on the plugin (or `"strict_timing": true` on it in `config.json`): a command whose
original expiry cannot be checked is then refused, never run on `deadline_ms` alone ([plugins.md](plugins.md), answer 9).

The driver should send telemetry with `battery` (`{"percent": 0..100}` or `{"volts": …}`) about twice a second.

## Stopping

- **The robot must stop on its own when commands stop.** The Link and the runtime stop the driver's motors on a
  passed deadline, a missed heartbeat or a closed stdin, but if the bridge computer sleeps, crashes or loses
  Bluetooth, only the robot can stop itself. Before any motor test, check that the base stops when the Bluetooth link
  drops or commands stop arriving. If it does not, keep every command short-lived so it cannot run on.
- **A stop on the robot itself.** If the driver has its own stop (a button, a hotkey, a sensor it trusts), call
  `self.local_stop("reason")` (or send `{"op": "local_stop", "reason": "…"}`). Every actuator stops at once, the
  Link latches its **local** stop (persisted across restarts), and the panel shows the robot as stopped on the robot.
  The owner's remote clear does not lift it: only `openvibe-link resume` on the bridge computer does, and then the
  owner clears the panel's e-stop. Running `openvibe-link stop` does the same from a terminal.

## Installing the driver

The Link runs a plugin named `looi` as `python -m openvibe_looi`, so the driver is a Python package with that module,
installed into the Python the Link uses.

**Linux or macOS bridge.** `curl -fsSL https://openvibe.link/install | sh -s -- --robot rob_… --code XXXX-XXXX
--driver looi` installs and pairs the Link as a bridge with video off. Then install the driver into the Link's
environment (`sudo /var/lib/openvibe-link/venv/bin/pip install <the driver>`) and restart the Link's service. Until
the driver is installed the panel shows the plugin's fault and the robot never moves.

**Windows bridge.** Bluetooth through the Windows runtime may not work from a service running as LocalSystem, so
run the Link in your own session:

```bat
set OVHOME=%LOCALAPPDATA%\OpenVibe Link
openvibe-link pair XXXX-XXXX --robot rob_… --kind bridge --home "%OVHOME%"
openvibe-link install --user-session --home "%OVHOME%"
```

`config.json` in that directory:

```json
{
  "server": "https://openvibe.bot",
  "device_kind": "bridge",
  "python": "C:\\Users\\you\\AppData\\Local\\Programs\\Python\\Python312\\python.exe",
  "plugins": [{"name": "looi", "strict_timing": true}],
  "video": {"source": "off"}
}
```

`install --user-session` starts the Link when you sign in, in a console window that shows its log (Windows keeps no
service log yet). Closing the window stops the Link and every actuator; nothing restarts it until the next sign-in.
`openvibe-link uninstall --user-session --home DIR` removes that directory's sign-in start (each Link directory has its own, since v0.37.2, so a test node never replaces a working one). On Windows a killed plugin takes the processes it
started with it (a Job Object), as it does on Linux and macOS, so a bridge helper cannot keep the Bluetooth link open
after the Link stops or restarts the plugin.

Try the driver without the robot first: `openvibe-link plugins` asks each plugin to describe itself without touching
hardware, and `openvibe-link run --dry-run` drives only the simulated plugin.
