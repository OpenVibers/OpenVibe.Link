# Installing OpenVibe Link

OpenVibe Link is one program, `openvibe-link`, plus Python plugins for robots. It connects **out** to OpenVibe (no open
ports, works behind any home router), and a local kill switch stops everything it drives.


## Install on your OS

Every release is built by CI on the tag and carries the same program for each system: Windows x64 and ARM64, macOS
Apple silicon and Intel, Linux x64 and ARM64 (.deb, .rpm, tarball and the `curl | sh` script), and the Android APK.
Links for every file are on [openvibe.link](https://openvibe.link/#install); the release page has the same files and
`SHA256SUMS`.

### Windows (x64 and ARM64)

1. Download `openvibe-link-windows-amd64.exe` (x64) or `openvibe-link-windows-arm64.exe` (ARM64) from
   [openvibe.link](https://openvibe.link/#install) or the latest release.
2. Windows may show SmartScreen the first time: **More info** → **Run anyway**.
3. In an **Administrator** PowerShell, from the folder you saved it in:

   ```powershell
   .\openvibe-link pair <CODE>
   .\openvibe-link install
   ```

   The code comes from [openvibe.link/pair](https://openvibe.link/pair), which signs you in and opens the machines hub
   (**Pair a machine**); [openvibe.link/machines](https://openvibe.link/machines) shows the same code.
4. `openvibe-link status` shows the link; the machine appears in your account's machines.


### macOS (Apple silicon and Intel)

The one command installs the right build and the launchd service:

```sh
curl -fsSL https://openvibe.link/install | sh
```

Or download the program: `openvibe-link-darwin-arm64` (Apple silicon) or `openvibe-link-darwin-amd64` (Intel), or the
tarball `openvibe-link_<version>_darwin_arm64.tar.gz` / `..._darwin_amd64.tar.gz` with `install.sh` and the plugins
beside it. Gatekeeper: right-click the file and choose **Open** the first time. Then:

```sh
sudo ./openvibe-link pair <CODE>
sudo ./openvibe-link install
```

The same command works on a Raspberry Pi or another Linux robot and also finds the kit and its driver.

### Linux (x64 and ARM64: .deb, .rpm, tarball, script)

The one command, on any distribution, finds the CPU and installs the systemd service:

```sh
curl -fsSL https://openvibe.link/install | sh
```

Or use the package for your distribution:

```sh
sudo apt install ./openvibe-link_<version>_amd64.deb     # Debian, Ubuntu (x64; _arm64.deb on ARM64)
sudo dnf install ./openvibe-link-<version>-1.x86_64.rpm  # Fedora, RHEL (aarch64 on ARM64)
```

The packages put `openvibe-link` in `/usr/local/bin`; they do not start or
pair anything by themselves. Then:

```sh
openvibe-link pair <CODE>
sudo openvibe-link install
```

The tarballs (`openvibe-link_<version>_linux_amd64.tar.gz` and `..._linux_arm64.tar.gz`) carry the program, the
plugin bundle and `install.sh`; unpack one and run `sudo ./install.sh --local .`.

### Android (the existing APK)

Download `openvibe-link-<tag>.apk` on the phone, allow installing from that source when Android asks, and open
**OpenVibe Link**. Pair with a code from [openvibe.link/pair](https://openvibe.link/pair) or
[openvibe.bot/link/phone](https://openvibe.bot/link/phone). This is the direct
APK, not Google Play; it has approvals, Activity, pairing and **Send to OpenVibe**, and no phone automation. Details:
[android.md](android.md).

### iPhone and iPad (what works today)

There is no native iOS app yet. The home-screen web app works now: open [openvibe.my](https://openvibe.my/) in Safari,
sign in, then **Share** → **Add to Home Screen**. It gives you **Approvals**, **Activity**, your **machines** and
pairing from the phone.

A native iOS app, push notifications and a background Link connection wait on an Apple Developer account and the App
Store review that follows it; nothing here pretends they already work.

## The one command (Raspberry Pi and other Linux robots)

On the robot's computer, with nothing set up on openvibe.bot first:

```sh
curl -fsSL https://openvibe.link/install | sh
```

The installer is available at <https://openvibe.link/install>.

On a Raspberry Pi it turns on I2C and SPI (now and after a reboot, through `raspi-config`), installs what is missing of
`i2c-tools`, the camera tools (`rpicam-apps`), `python3-venv`, `python3-lgpio` and `qrencode`, and looks at the I2C bus:
a PCA9685 at `0x5f` is the Adeept ADR036 4WD (driver `adeept`), a rosbridge on port 9090 a ROS 2 robot (`ros2`). It
installs that driver, then prints a code and a QR code:

```
  Approve this robot on openvibe.bot (signed in as you):
    open  https://openvibe.bot/link
    code  KQ7M-2XRD
```

Open the link (or scan the QR with your phone), sign in, and approve it: openvibe.bot shows what the machine found and
lets you set its name, its model (the Adeept mecanum chassis looks the same on the bus: pick it there), who may drive,
and whether its camera goes on your OpenVibe.Live channel. The installer then pairs, sets up the driver for the model
you chose if it differs, and checks the link, the driver and the camera:

```
  link     connected
  driver   adeept_adr036: running
  camera   live from rpicam
openvibe-link: drive it: https://openvibe.bot/panel/rob_…
```

The code lasts 15 minutes; a denied or expired one adds nothing, and running the command again gives a new one. An
already-paired machine keeps its pairing. `--driver` still overrides what was found, and `--no-claim` installs without
asking for a code. To drive the robot from your Live chat, use **Use it in my OpenVibe.Live chat** on the approval page.

## From a blank microSD card (no screen, no keyboard)

[openvibe.bot/setup/sd-card](https://openvibe.bot/setup/sd-card) makes a Raspberry Pi card's first-boot files: you
write Raspberry Pi OS Lite (64-bit) with Raspberry Pi Imager, copy the page's `user-data` and `network-config` onto the
card's `bootfs` drive, and switch the robot on. On first boot the Pi joins your Wi-Fi, turns on SSH, and runs

```sh
curl -fsSL https://openvibe.link/install | sh -s -- --setup osk_…
```

with the card's setup key (`OPENVIBE_SETUP_KEY` works too). openvibe.bot approves that claim at once for the person who
made the files, with the name, model, who may drive and Live they chose there, so no code is shown and nobody needs to
be at the robot. A key adds one robot and lasts 7 days; a retry before the robot has paired pairs the same robot again.
Its output is in `journalctl -u openvibe-setup` on the Pi.

## Pair from openvibe.link

[openvibe.link/pair](https://openvibe.link/pair) signs you in and opens **Your machines** at openvibe.my: every machine
you paired with whether it is online, what it is allowed to do, a one-time code and the command to pair another, and
rename or revoke. The installer asks for that code, so the whole one-minute start is: install, get the code there,
paste it. Nothing about pairing lives only on openvibe.bot any more; the old page still works.

## Quick start with a code from openvibe.bot (Linux, Raspberry Pi, macOS)

1. On openvibe.bot, add a robot. You get a code like `ABCD-1234` (valid 10 minutes) and the installer command with
   the robot's id filled in.
2. On the device, run that command, adding `--driver` for your robot:

   ```sh
   curl -fsSL https://openvibe.link/install | sh -s -- --robot rob_01J8Z4M2Q0R7T9YV3K6N8P1W2X --code ABCD-1234 --driver adeept
   ```

   The shape is `--robot rob_… --code CODE [--driver adeept|adeept-mecanum|cozmo|looi|ros2|ros2-holonomic|none]`. `--robot` is the robot's
   `rob_…` id and `--code` the pairing code (both from openvibe.bot; the code may also be the first argument).
   `--driver` names the plugin: `adeept` (Adeept 4WD Smart Car, ordinary wheels), `adeept-mecanum`, `cozmo`, `ros2` (a
   ROS 2 robot through rosbridge, [plugins/ros2/README.md](../plugins/ros2/README.md)), `ros2-holonomic`, or `none` (the default: the dry-run plugin with a test-pattern camera, for trying the Link on a laptop). `--name NAME`
   names the device on openvibe.bot (default: the hostname).
3. Confirm the new device on openvibe.bot.

**Your own models:** add `--ai` to any of these commands. It adds the `ai` plugin, which finds Ollama, LM Studio or
llama.cpp's server on the machine, and the installer says which one answers. `--ai-endpoint URL` names another port.
[ai.md](ai.md) has the rest.

**A machine that runs your jobs, not a robot:** openvibe.link/machines prints the Network form with `--worker` (and no
`--driver`). It turns the job worker on with a dedicated `openvibe-job` user and the OpenVibe.Codes job runner, so a
coding task you start on openvibe.codes/run runs on this machine. Add `--plan-logins` (Linux) to use the plans you
already signed in for: the installer names the coding tools' own login files that are ready (Claude Code, Codex,
OpenCode and Command Code) as the worker's plan logins, sets the Link service's HOME to your home, and prints the login
command for each tool whose login is missing or not signed in (a login whose CLI is not on the runner's PATH is named
with a line to install it system-wide). It never reads, copies or prints a login file's content.
[worker.md](worker.md#a-machine-that-runs-your-coding-tasks---worker)
has the details.

**Two pairing paths.** Which one the command uses is up to openvibe.bot; the installer takes both.

- **Bot pairing** (the form above): `--robot rob_… --code CODE`. The Link redeems the code at
  `POST <bot>/api/v1/pair` and stores the Bot device credential it answers with.
- **Network pairing**: `--network <URL> --pairing pair_… --code CODE`, e.g.

  ```sh
  curl -fsSL https://openvibe.link/install | sh -s -- --network https://openvibe.network --pairing pair_01J8Z4M2Q0R7T9YV3K6N8P1W2Y --code ABCD-1234 --driver adeept
  ```

  The Link redeems the code on OpenVibe.Network (`POST <network>/api/v1/link-pairing`) and stores the machine's node
  principal (`lnk_…`) and node credential. With it the Link buys short-lived node tokens (`POST <network>/oauth/token`,
  audience `openvibe.bot`) and binds itself on Bot (`POST <bot>/api/v1/devices/bind`), which answers the device id, the
  robot and the WHIP publish key. A bind that fails at pairing is retried when the Link starts. `--pairing CODE` with
  no `--code` also works (the code is then the value of `--pairing`).

The installer detects the OS and CPU (linux amd64/arm64/armv7, macOS amd64/arm64), downloads the binary and the plugin
bundle and checks them against `SHA256SUMS`, creates a Python virtual environment for the plugins, writes
`config.json`, disables the Adeept kit's stock server if it finds it, creates the `openvibe-link` service account
(Linux; with the `gpio`, `i2c`, `spi`, `video`, `audio` groups), installs and starts the service, and pairs. Running it
again upgrades and keeps the credential. Options: `--no-service`, `--version vX.Y.Z`, `--local DIR` (install from a
directory holding the release files). `OPENVIBE_LINK_BASE` overrides the download location, `OPENVIBE_SERVER` the
server.

The installer downloads the binary, the plugin bundle and `SHA256SUMS` from `releases/latest/download`, which GitHub
resolves only to the latest full release, so a host whose release is still a pre-release needs `OPENVIBE_LINK_BASE` or
`--version TAG`.

Requirements: `python3` with `venv` (`sudo apt install python3 python3-venv`), `curl` or `wget`. For the Adeept camera,
`rpicam-apps` (preinstalled on Raspberry Pi OS). For Cozmo's voice, `espeak-ng`. `ffmpeg` is optional (better video
from plugins that send JPEG frames).

## First run (guided and measured)

After the installer, run the Link with no command:

```sh
sudo openvibe-link
```

It walks through the first run once: it asks for the code from openvibe.link/machines if the machine is not paired yet
(pasting the whole install command there works too, for a Network pairing), checks that the machine reaches
OpenVibe.Bot and OpenVibe.Network, runs one harmless first job that reads the machine's OS, CPUs, memory and free
disk, and prints what to do next. The job opens nothing and runs as the service account; it only reads those counts.

The Link reports one telemetry event per step (`installed`, `paired`, `self_check` with ok or a reason, `first_job`)
over the same device link it already uses. An event carries the Link version, the OS and architecture, and for the
first job those counts: never a path, a username, an IP or a secret. Bot stores the step times, so an admin can see how
many installs reach each step and how long they take (`/admin/first-run`), and the machine's page shows what the first
job found.

`openvibe-link first-run` is the same command when a script wants to say it. `--code CODE`, `--network URL
--pairing pair_…` and `--home DIR` work as flags instead of the prompt.

## By hand

```sh
sudo install -m 755 openvibe-link-linux-arm64 /usr/local/bin/openvibe-link
sudo ln -s openvibe-link /usr/local/bin/openvibe-link
sudo openvibe-link pair ABCD-1234 --robot rob_…   # stores /etc/openvibe-link/credential.json (mode 600)
# or, for a Network pairing:
sudo openvibe-link pair --network https://openvibe.network --pairing pair_… --code ABCD-1234
sudo openvibe-link install               # systemd / launchd / Windows service, starts it
openvibe-link status
```

Without root, everything goes to your user config directory (`~/.config/openvibe-link`), or to any directory with
`--home DIR` / `OPENVIBE_LINK_HOME`:

```sh
openvibe-link --home ~/node pair ABCD-1234
openvibe-link --home ~/node run --dry-run      # foreground; Ctrl-C stops it
```

**Windows**: download `openvibe-link-windows-amd64.exe`, then in an Administrator prompt `openvibe-link pair <CODE>`
and `openvibe-link install` (the service runs as LocalSystem; files go to `%ProgramData%\OpenVibe Link`). Robot plugins
need Python; set `"python"` in `config.json`.

A driver that needs your signed-in session (Bluetooth through the Windows runtime, as a LOOI bridge does) runs the
Link there instead, with no administrator: `openvibe-link pair <CODE> --home "%LOCALAPPDATA%\OpenVibe Link"`, then
`openvibe-link install --user-session --home "%LOCALAPPDATA%\OpenVibe Link"`. It starts when you sign in, in a
console window that shows its log; closing the window stops the Link and every actuator. `openvibe-link uninstall
--user-session` removes it. See [hardware-looi.md](hardware-looi.md).

## Commands

| command                                   | what it does                                                               |
|-------------------------------------------|----------------------------------------------------------------------------|
| `openvibe-link pair <CODE> [--force]`     | redeem a pairing code (or `--code CODE`); `--robot rob_…` the robot it is for, `--name` the device name (default: hostname); `--server`, `--kind onboard\|bridge` override config. With `--network URL [--pairing pair_…]` the code is redeemed on OpenVibe.Network and the device bound on Bot |
| `openvibe-link run [--dry-run]`           | run in the foreground (what the service runs)                              |
| `openvibe-link install [--user NAME]`     | install and start the service                                              |
| `openvibe-link uninstall`                 | stop and remove the service (config and credential are kept)               |
| `openvibe-link install --user-session --home DIR` | Windows: start at sign-in in your own session instead of as a service |
| `openvibe-link uninstall --user-session --home DIR` | Windows: remove that directory's sign-in start (one per directory) |
| `openvibe-link status [--json]`           | link, stop latch, limits, plugins, video, battery, running jobs            |
| `openvibe-link stop`                      | **kill switch**: stop every actuator now and hold it stopped              |
| `openvibe-link resume`                    | release the local stop (and a server e-stop)                               |
| `openvibe-link quit`                      | end the running Link cleanly: every actuator stops, **nothing is latched**, and it waits until the Link has exited |
| `openvibe-link plugins [--json]`          | plugins and their capabilities                                             |

`stop` works even when the Link does not answer: it latches `latch.json` in the state directory, which a running Link
obeys within a quarter second and a starting Link obeys before anything moves. The latch survives restarts.

### Starting and ending it with the rest of your setup

`stop` is a kill switch, not an exit: it holds every actuator stopped, across restarts, until `resume`. To end the Link
from a script (a stream launcher that starts and stops it with OBS, say), use `quit`. Every actuator stops, the Link
exits, and nothing is latched, so the next `openvibe-link run` drives as before. `quit` waits until the process has
gone and succeeds when no Link is running, so a launcher can call it without checking first.

`quit` ends a Link started from a terminal, by a launcher, or at sign-in (`install --user-session`). A Link installed as
a system service refuses it, because systemd, launchd or Windows would start it again. Stop the service there instead
(`sudo systemctl stop openvibe-link`, or Services on Windows), or remove it with `openvibe-link uninstall`. A launcher
that owns the Link's life should run `openvibe-link run --home DIR` itself, with no service and no sign-in start.

## Files

| Linux                                 | macOS / Windows                                       | contents                          |
|---------------------------------------|-------------------------------------------------------|-----------------------------------|
| `/etc/openvibe-link/config.json`      | `…/OpenVibe Link/config.json`                         | configuration                     |
| `/etc/openvibe-link/credential.json`  | `…/OpenVibe Link/credential.json`                     | device or node credential (mode 600) |
| `/var/lib/openvibe-link/latch.json`   | `…/OpenVibe Link/latch.json`                          | e-stop / kill switch latch        |
| `/var/lib/openvibe-link/node.sock`    | `…/OpenVibe Link/node.sock`                           | local control socket (mode 600)   |
| `/var/lib/openvibe-link/venv`         | `…/OpenVibe Link/venv`                                | plugin Python environment         |

macOS: `/Library/Application Support/OpenVibe Link`. Windows: `%ProgramData%\OpenVibe Link`.

## Configuration

```json
{
  "server": "https://openvibe.bot",
  "device_kind": "onboard",
  "plugins": [{"name": "adeept_adr036", "config": {"backend": "real", "wheels": "ordinary"}}],
  "video": {"source": "auto"},
  "limits": {"max_speed": 0.6, "max_turn": 0.5, "max_command_ms": 500},
  "log_level": "info"
}
```

- `device_kind`: `onboard` or `bridge`.
- `plugins`: see [plugins.md](plugins.md#configuration).
- `video.source`: `auto` (the plugin's camera: its `h264_command` if the program exists, else its JPEG frames, else the
  test pattern if it asks for one), `test`, `command` (with `video.command`: a program writing H.264 Annex B to stdout,
  e.g. `["ffmpeg", "-f", "v4l2", "-i", "/dev/video0", "-c:v", "libx264", "-preset", "ultrafast", "-tune",
  "zerolatency", "-profile:v", "baseline", "-g", "30", "-f", "h264", "-"]`), `plugin` (with `video.plugin`), `off`.
  `fps`, `width`, `height`, `ffmpeg` tune the rest. `whip_url`: where the camera publishes, with the publish key from
  pairing. It overrides the `whip_url` OpenVibe.Bot returns at pairing; with neither (Bot has no ingest configured)
  video stays off and the log says so.
- `limits`: local caps on top of the owner's limits from openvibe.bot; the stricter value wins.
- `python`: interpreter for bundled plugins (default `<state>/venv/bin/python`, then `python3`).

Unknown keys are rejected, so a typo does not silently change behaviour.

## Robots

### Adeept 4WD Smart Car Kit for Raspberry Pi (ADR036)

Raspberry Pi OS Bookworm (64-bit recommended). Enable I²C and SPI: `sudo raspi-config` → Interface Options, or
`dtparam=i2c_arm=on` and `dtparam=spi=on` in `/boot/firmware/config.txt`, then reboot. Install with
`--driver adeept` (or `adeept-mecanum`).

The kit's own software (`Adeept_Robot.service`, `WebServer.py` on `0.0.0.0:8888` with the fixed login
`admin:123456`, MJPEG on `:5000`) must not run: anyone on the network could drive the car. The installer
(`install/install.sh`) now does this automatically: it stops and disables `Adeept_Robot.service` and comments out the
kit's autostart lines in the invoking user's crontab, root's crontab and `/etc/rc.local`, prefixing each with the
marker `#openvibe-link-disabled:` and keeping the original text, so undoing it is removing the marker and a second
run changes nothing. From a root shell, it checks `pi`'s crontab when that account exists. Verify with
`systemctl status Adeept_Robot.service`, `crontab -l | grep openvibe-link-disabled` (and
`sudo crontab -l -u pi | grep openvibe-link-disabled` for a root-shell install) and
`grep openvibe-link-disabled /etc/rc.local`. The Link replaces it.

If a direction is wrong on your car, fix it in `config.json` → `plugins[0].config`: `motors.<wheel>.direction`,
`pan.invert`, `tilt.invert`, `line.invert`. See [the plugin README](../plugins/adeept_adr036/README.md).

### Cozmo (bridge)

Cozmo makes its own Wi-Fi network without internet. Use a Raspberry Pi or laptop with two network interfaces: one
joined to Cozmo's network (`Cozmo_…`; raise and lower the lift to see the password on its face), the other to the
internet (Ethernet, or a second Wi-Fi adapter). Install with `--driver cozmo`; the device kind is `bridge`. See
[the plugin README](../plugins/cozmo/README.md).

## Troubleshooting

- `openvibe-link status` shows the last link error. "the server refused the device credential" means it was revoked:
  `sudo openvibe-link pair --force <NEW-CODE>`.
- A Network-paired machine whose node credential OpenVibe.Network refuses (revoked or removed) stops with "OpenVibe.Network
  refused this machine's node credential" and exit status 78, which the systemd unit does not restart. Get a new
  installer command on openvibe.bot and pair again with `--force`.
- Logs: `journalctl -u openvibe-link -f` (Linux), `/usr/local/var/log/openvibe-link.err.log` (macOS). On Windows the
  service's log is not kept yet; stop the service and run `openvibe-link run` in a terminal to watch it.
- A plugin in `faulted` state says why in `status` (missing library, not a Raspberry Pi, robot not reachable, firmware).
- `openvibe-link plugins` works without the service running: it asks each plugin to describe itself without touching
  hardware.

## Uninstall

```sh
sudo openvibe-link uninstall
sudo rm -rf /usr/local/bin/openvibe-link /etc/openvibe-link /var/lib/openvibe-link
sudo userdel openvibe-link
```

Then remove the device on openvibe.bot, which revokes its credential.
