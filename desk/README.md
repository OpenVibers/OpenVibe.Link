# ov-desk: the lent desktop's Rust helper

`ov-desk` runs inside a lent desktop's sandbox (`--desktops`, [docs/computer.md](../docs/computer.md#lending-whole-desktops-too---desktops-link-v0650)).
It starts the desktop's own X display (Xvfb) and its apps, and answers the computer plugin over the sandbox's pipe:
one JSON line each way on fds 3 and 4.

It speaks exactly the protocol of the Go helper (`openvibe-link desk-display`, `internal/deskdisplay`):

| op | what it does |
| --- | --- |
| `shot` | the screen as a JPEG, `max_width` and `max_bytes` honoured |
| `xdotool` | the input, done in-process through XTest: `mousemove`, `click`, `key`, `type`, `getdisplaygeometry`; anything else goes to the real xdotool |
| `logs` | each program's last output and whether it runs |
| `stop` | ends the desktop |

So the plugin can run either helper. It prefers `ov-desk` where the sandbox sees it (under /usr, /bin or /opt), then
the Link's own binary, then the Python helper.

```sh
ov-desk desk-display WIDTH HEIGHT APPS_JSON        # what the plugin runs (fds 3 and 4 are its pipe)
ov-desk stream -display :0 -fps 15                 # the live screen as H.264 on stdout (the core reads it)
ov-desk bench -display :99 -seconds 10 -fps 30     # the desk loop timed on a display: one JSON line
```

`ov-desk stream` (v0.75) is the live screen. It writes H.264 (Annex B) to stdout, which the Link's core publishes over WHIP:
- the screen is read through MIT-SHM and turned into I420 in one integer pass, since the openh264 crate's own
  conversion cost more than the encoding;
- OpenH264 encodes it in its screen-content mode;
- it runs at 15 a second, at most 1600 px wide, with a keyframe every 2 seconds.

## How it makes pictures

- **Capture.** The screen is read through MIT-SHM, where the X server writes into memory it shares with the helper.
  Where that is refused, it is read over the socket.
- **Encoding.** X's own BGRX pixels go to libjpeg-turbo, loaded at run time like the Go helper's purego load. Where
  the library is missing, a pure-Rust encoder runs.
- **Shrinking.** A narrower picture is shrunk by averaging, so text stays legible. It is the Go helper's algorithm,
  line for line.
- **The ready message** says which paths it took: `capture` (`shm` or `socket`), `encoder` (`libjpeg-turbo` or
  `rust`), and `helper: "ov-desk"`.

## Why Rust

The owner asked for Rust on 2026-11-17 ("the most ultimate maximized optimized efficient agent workers"). A
line-for-line race against the Go helper's loop settled it; the results are in docs/computer.md. Where the C library
does the work they tie. Where our own code does the work (the shrink, frames as fast as they go), Rust used about a
quarter to a third less CPU, with a much lower worst case. It used about 10 MB less memory throughout.

The same crate is where native capture and input go next: Windows (Graphics.Capture/DXGI and SendInput), macOS
(ScreenCaptureKit and CGEvent) and Wayland (PipeWire).

## Building

```sh
cargo test --manifest-path desk/Cargo.toml
cargo build --release --manifest-path desk/Cargo.toml
```

`scripts/dist.sh` builds it for Linux amd64 and arm64. The release, the .deb, the .rpm and the Linux tarballs carry it,
and `install/install.sh` installs it beside `openvibe-link`.
