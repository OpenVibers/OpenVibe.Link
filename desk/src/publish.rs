//! `ov-desk publish`: the screen captured, encoded and published over WHIP by ov-desk itself (Link v0.77).
//!
//! The owner, 2026-11-17: "we want to use rust if we get any gain at all". The live screen used to leave ov-desk as
//! H.264 on a pipe, and the Link's Go core split it into frames, packetized it into RTP and encrypted it (pion). Raced
//! on the same real recordings, that Go path took 11-44% more CPU a frame than the same work in Rust, and 18-65 MB
//! against 13 MB (docs/computer.md). So here the whole path is one Rust process: capture (MIT-SHM), the colour
//! conversion, OpenH264, and WebRTC by str0m (sans-IO: one thread and one UDP socket, no async runtime). A viewer's PLI
//! or FIR makes the next frame an IDR directly, with no signal in between.
//!
//!   ov-desk publish -whip <url> [-display :0] [-fps 15] [-max-width 1600] [-kbps 2500] [-audio] [-seconds 0]
//!
//! The publish key is the environment's OPENVIBE_WHIP_KEY (never argv, which every user can read). The Link's core
//! drives it over stdin and reads it over stdout, one JSON object a line:
//!   in:  {"pause":true} {"mute":false} {"key":"…"} {"voice":"<base64 Opus, 20 ms>"}
//!   out: {"state":"connecting"} {"state":"live"} {"state":"retrying","error":"…"} {"frames":N}
//! It reconnects with backoff (1 s doubling to 30 s), says goodbye to the WHIP resource (DELETE) when it ends, and ends
//! when stdin closes, on SIGTERM, or after -seconds. Exit 3: it never reached the server over UDP (the core falls back
//! to its own publisher, which can relay through TURN).
use crate::stream::{self, Options as StreamOptions};
use std::io::{BufRead, Write};
use std::net::{SocketAddr, ToSocketAddrs, UdpSocket};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{sync_channel, Receiver, TrySendError};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use str0m::change::{SdpAnswer, SdpPendingOffer};
use str0m::format::Codec;
use str0m::media::{Direction, Frequency, MediaKind, MediaTime, Mid, Pt};
use str0m::net::{Protocol, Receive};
use str0m::{Candidate, Event, IceConnectionState, Input, Output, Rtc};

/// One 20 ms Opus silence frame (the same three bytes the Go publisher sends).
const OPUS_SILENCE: [u8; 3] = [0xf8, 0xff, 0xfe];
/// How long a session may take to connect before it counts as "no route" (exit 3 when it never connected once).
const CONNECT_WITHIN: Duration = Duration::from_secs(15);

pub struct Options {
    pub stream: StreamOptions,
    pub whip: String,
    pub audio: bool,
}

impl Options {
    pub fn parse(args: &[String]) -> Result<Options, String> {
        let get = |n: &str| args.iter().position(|x| x == n).and_then(|i| args.get(i + 1)).cloned();
        let whip = get("-whip").ok_or("-whip <url> is needed")?;
        if !(whip.starts_with("https://") || whip.starts_with("http://127.0.0.1") || whip.starts_with("http://localhost")) {
            return Err("the WHIP endpoint is https (or loopback http, for tests)".into());
        }
        Ok(Options { stream: StreamOptions::parse(args), whip, audio: args.iter().any(|a| a == "-audio") })
    }
}

/// What the core controls, shared with the session loop.
struct Control {
    paused: AtomicBool,
    muted: AtomicBool,
    key: Mutex<String>,
    voice: Mutex<std::collections::VecDeque<Vec<u8>>>,
    stop: AtomicBool,
}

fn say(line: &str) {
    let mut out = std::io::stdout().lock();
    let _ = out.write_all(line.as_bytes());
    let _ = out.write_all(b"\n");
    let _ = out.flush();
}

fn say_state(state: &str, error: Option<&str>) {
    let mut v = serde_json::json!({ "state": state });
    if let Some(e) = error { v["error"] = serde_json::Value::String(e.chars().take(300).collect()); }
    say(&v.to_string());
}

static TERM: AtomicBool = AtomicBool::new(false);
extern "C" fn on_term(_: libc::c_int) { TERM.store(true, Ordering::SeqCst); }

pub fn run(o: Options) -> Result<i32, String> {
    let key = std::env::var("OPENVIBE_WHIP_KEY").unwrap_or_default();
    let ctl = Arc::new(Control { paused: AtomicBool::new(false), muted: AtomicBool::new(false), key: Mutex::new(key), voice: Mutex::new(Default::default()), stop: AtomicBool::new(false) });
    unsafe {
        libc::signal(libc::SIGTERM, on_term as *const () as libc::sighandler_t);
        libc::signal(libc::SIGPIPE, libc::SIG_IGN);
    }

    // The core's commands, one JSON object a line; stdin closing is the end.
    {
        let ctl = ctl.clone();
        std::thread::spawn(move || {
            let stdin = std::io::stdin();
            for line in stdin.lock().lines() {
                let Ok(line) = line else { break };
                let Ok(v) = serde_json::from_str::<serde_json::Value>(&line) else { continue };
                if let Some(b) = v.get("pause").and_then(|x| x.as_bool()) { ctl.paused.store(b, Ordering::SeqCst); }
                if let Some(b) = v.get("mute").and_then(|x| x.as_bool()) { ctl.muted.store(b, Ordering::SeqCst); }
                if let Some(k) = v.get("key").and_then(|x| x.as_str()) { *ctl.key.lock().unwrap() = k.to_string(); }
                if let Some(s) = v.get("voice").and_then(|x| x.as_str()) {
                    use base64::Engine;
                    if let Ok(frame) = base64::engine::general_purpose::STANDARD.decode(s) {
                        let mut q = ctl.voice.lock().unwrap();
                        if q.len() < 50 { q.push_back(frame); }
                    }
                }
            }
            ctl.stop.store(true, Ordering::SeqCst);
        });
    }

    // The screen: captured and encoded on its own thread, a few frames of slack. A frame that does not fit is dropped,
    // and the next one is made an IDR so the picture never decodes from a missing reference.
    let (tx, rx) = sync_channel::<(Vec<u8>, Duration)>(4);
    let frames = Arc::new(AtomicU64::new(0));
    let capture = {
        let so = StreamOptions { display: o.stream.display.clone(), fps: o.stream.fps, max_width: o.stream.max_width, kbps: o.stream.kbps, seconds: o.stream.seconds };
        let ctl = ctl.clone();
        std::thread::spawn(move || {
            let flag = stream::keyframe_flag();
            let r = stream::produce(&so, flag, "publish", |au, at| {
                if ctl.stop.load(Ordering::SeqCst) || TERM.load(Ordering::SeqCst) { return false; }
                match tx.try_send((au.to_vec(), at)) {
                    Ok(()) => true,
                    Err(TrySendError::Full(_)) => { stream::request_keyframe(); true }
                    Err(TrySendError::Disconnected(_)) => false,
                }
            });
            if let Err(e) = r { eprintln!("ov-desk publish: {e}"); }
        })
    };

    let mut backoff = Duration::from_secs(1);
    let mut ever_connected = false;
    let started = Instant::now();
    let end = if o.stream.seconds > 0.0 { Some(started + Duration::from_secs_f64(o.stream.seconds)) } else { None };
    loop {
        if ctl.stop.load(Ordering::SeqCst) || TERM.load(Ordering::SeqCst) || capture.is_finished() || end.map(|e| Instant::now() >= e).unwrap_or(false) {
            return Ok(0);
        }
        say_state("connecting", None);
        let r = session(&o, &ctl, &rx, &frames, &mut ever_connected, end);
        match r {
            Ok(()) => return Ok(0),
            Err(e) => {
                if !ever_connected && e.starts_with("no route") { say_state("failed", Some(&e)); return Ok(3); }
                say_state("retrying", Some(&e));
                let wait_until = Instant::now() + backoff;
                while Instant::now() < wait_until {
                    if ctl.stop.load(Ordering::SeqCst) || TERM.load(Ordering::SeqCst) { return Ok(0); }
                    // Frames made while away are dropped; the session after asks for a keyframe first.
                    while rx.try_recv().is_ok() {}
                    std::thread::sleep(Duration::from_millis(50));
                }
                if backoff < Duration::from_secs(30) { backoff *= 2; }
            }
        }
    }
}

/// The local address the WHIP server is reached from: the source of a connected UDP socket toward it (nothing is sent).
fn local_ip_toward(host: &str, port: u16) -> Result<std::net::IpAddr, String> {
    let to: SocketAddr = (host, port).to_socket_addrs().map_err(|e| format!("no route to {host}: {e}"))?.next().ok_or("no route: no address")?;
    let probe = UdpSocket::bind(if to.is_ipv4() { "0.0.0.0:0" } else { "[::]:0" }).map_err(|e| e.to_string())?;
    probe.connect(to).map_err(|e| format!("no route to {host}: {e}"))?;
    Ok(probe.local_addr().map_err(|e| e.to_string())?.ip())
}

fn host_port(url: &str) -> Result<(String, u16), String> {
    let rest = url.split("://").nth(1).ok_or("not a URL")?;
    let authority = rest.split('/').next().unwrap_or("");
    let https = url.starts_with("https://");
    if let Some((h, p)) = authority.rsplit_once(':') {
        if let Ok(p) = p.parse::<u16>() { return Ok((h.trim_matches(|c| c == '[' || c == ']').to_string(), p)); }
    }
    Ok((authority.to_string(), if https { 443 } else { 80 }))
}

fn post_offer(whip: &str, key: &str, offer: &str) -> Result<(String, Option<String>), String> {
    let mut req = ureq::post(whip).set("Content-Type", "application/sdp").timeout(Duration::from_secs(15));
    if !key.is_empty() { req = req.set("Authorization", &format!("Bearer {key}")); }
    match req.send_string(offer) {
        Ok(resp) => {
            let loc = resp.header("Location").map(|s| s.to_string());
            let body = resp.into_string().map_err(|e| e.to_string())?;
            Ok((body, loc))
        }
        Err(ureq::Error::Status(code, resp)) => {
            let body = resp.into_string().unwrap_or_default();
            Err(format!("the WHIP endpoint answered {code}: {}", body.chars().take(200).collect::<String>().replace(key, "***")))
        }
        Err(e) => Err(format!("the WHIP endpoint did not answer: {e}")),
    }
}

/// The resource the server named, only on the WHIP endpoint's own origin (the key never goes elsewhere).
fn resource_url(whip: &str, loc: &str) -> Option<String> {
    let origin = |u: &str| -> Option<String> { let mut p = u.splitn(4, '/'); Some(format!("{}//{}", p.next()?, { p.next(); p.next()? })) };
    if loc.starts_with('/') { return Some(format!("{}{}", origin(whip)?, loc)); }
    if origin(loc)? == origin(whip)? { Some(loc.to_string()) } else { None }
}

fn session(o: &Options, ctl: &Control, rx: &Receiver<(Vec<u8>, Duration)>, frames: &AtomicU64, ever: &mut bool, end: Option<Instant>) -> Result<(), String> {
    let (host, port) = host_port(&o.whip)?;
    let ip = local_ip_toward(&host, port)?;
    let socket = UdpSocket::bind(SocketAddr::new(ip, 0)).map_err(|e| format!("no route: cannot bind {ip}: {e}"))?;
    let local = socket.local_addr().map_err(|e| e.to_string())?;

    let mut rtc = Rtc::builder().clear_codecs().enable_h264(true).enable_opus(o.audio, false).build(Instant::now());
    rtc.add_local_candidate(Candidate::host(local, "udp").map_err(|e| e.to_string())?);
    let mut change = rtc.sdp_api();
    let vmid = change.add_media(MediaKind::Video, Direction::SendOnly, None, None, None);
    let amid = if o.audio { Some(change.add_media(MediaKind::Audio, Direction::SendOnly, None, None, None)) } else { None };
    let (offer, pending): (_, SdpPendingOffer) = change.apply().ok_or("no offer")?;
    let key = ctl.key.lock().unwrap().clone();
    let (answer, location) = post_offer(&o.whip, &key, &offer.to_sdp_string())?;
    let resource = location.as_deref().and_then(|l| resource_url(&o.whip, l));
    let answer = SdpAnswer::from_sdp_string(&answer).map_err(|e| format!("the answer is not SDP: {e}"))?;
    rtc.sdp_api().accept_answer(pending, answer).map_err(|e| format!("the answer was refused: {e}"))?;

    let r = drive(o, ctl, rx, frames, ever, end, &mut rtc, &socket, local, vmid, amid);
    // Goodbye to the resource, whatever ended the session.
    if let Some(url) = resource {
        let mut del = ureq::delete(&url).timeout(Duration::from_secs(5));
        if !key.is_empty() { del = del.set("Authorization", &format!("Bearer {key}")); }
        let _ = del.call();
    }
    r
}

/// The negotiated H.264 payload type: packetization-mode 1, Constrained Baseline or Baseline (what OpenH264 makes).
fn h264_pt(rtc: &mut Rtc, mid: Mid) -> Option<Pt> {
    let w = rtc.writer(mid)?;
    let mut best: Option<(u8, Pt)> = None;
    for p in w.payload_params() {
        if p.spec().codec != Codec::H264 { continue; }
        let f = &p.spec().format;
        if f.packetization_mode != Some(1) { continue; }
        let profile = f.profile_level_id.map(|v| (v >> 16) as u8).unwrap_or(0x42);
        let score = if profile == 0x42 { 2 } else { 1 };
        if best.map(|b| score > b.0).unwrap_or(true) { best = Some((score, p.pt())); }
    }
    best.map(|b| b.1)
}

fn opus_pt(rtc: &mut Rtc, mid: Mid) -> Option<Pt> {
    let w = rtc.writer(mid)?;
    let pt = w.payload_params().find(|p| p.spec().codec == Codec::Opus).map(|p| p.pt());
    pt
}

#[allow(clippy::too_many_arguments)]
fn drive(o: &Options, ctl: &Control, rx: &Receiver<(Vec<u8>, Duration)>, frames: &AtomicU64, ever: &mut bool, end: Option<Instant>,
         rtc: &mut Rtc, socket: &UdpSocket, local: SocketAddr, vmid: Mid, amid: Option<Mid>) -> Result<(), String> {
    let mut buf = vec![0u8; 2000];
    let began = Instant::now();
    let mut connected = false;
    let mut need_key = true;
    let mut vpt: Option<Pt> = None;
    let mut apt: Option<Pt> = None;
    let mut next_audio = Instant::now();
    let mut audio_ts: u64 = 0;
    let mut last_report = Instant::now();
    loop {
        if ctl.stop.load(Ordering::SeqCst) || TERM.load(Ordering::SeqCst) || end.map(|e| Instant::now() >= e).unwrap_or(false) {
            return Ok(());
        }
        // Everything str0m has to say, until it asks for time.
        let timeout = loop {
            match rtc.poll_output().map_err(|e| e.to_string())? {
                Output::Timeout(t) => break t,
                Output::Transmit(t) => { let _ = socket.send_to(&t.contents, t.destination); }
                Output::Event(e) => match e {
                    Event::Connected => {
                        connected = true;
                        *ever = true;
                        vpt = h264_pt(rtc, vmid);
                        apt = amid.and_then(|m| opus_pt(rtc, m));
                        if vpt.is_none() { return Err("the server took no H.264 this encoder can send".into()); }
                        stream::request_keyframe();
                        need_key = true;
                        // Sound starts now: no burst of the frames "missed" while connecting.
                        next_audio = Instant::now();
                        say_state("live", None);
                    }
                    Event::KeyframeRequest(_) => stream::request_keyframe(),
                    Event::IceConnectionStateChange(IceConnectionState::Disconnected) => return Err("the connection dropped".into()),
                    _ => {}
                },
            }
        };
        if !connected && began.elapsed() > CONNECT_WITHIN {
            return Err(format!("no route: no media path to the server within {} s", CONNECT_WITHIN.as_secs()));
        }
        // Wait for a packet, at most until str0m's next timer or the next frame is likely due (5 ms).
        let now = Instant::now();
        let wait = timeout.saturating_duration_since(now).clamp(Duration::from_micros(500), Duration::from_millis(5));
        let _ = socket.set_read_timeout(Some(wait));
        match socket.recv_from(&mut buf) {
            Ok((n, source)) => {
                if let Ok(contents) = buf[..n].try_into() {
                    rtc.handle_input(Input::Receive(Instant::now(), Receive { proto: Protocol::Udp, source, destination: local, contents }))
                        .map_err(|e| e.to_string())?;
                }
            }
            Err(e) if matches!(e.kind(), std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut) => {}
            Err(e) => return Err(format!("the socket failed: {e}")),
        }
        rtc.handle_input(Input::Timeout(Instant::now())).map_err(|e| e.to_string())?;
        if !connected { while rx.try_recv().is_ok() {} continue; }

        // The frames the encoder made since.
        let paused = ctl.paused.load(Ordering::SeqCst);
        while let Ok((au, at)) = rx.try_recv() {
            if paused { need_key = true; continue; }
            if need_key {
                if !is_key(&au) { stream::request_keyframe(); continue; }
                need_key = false;
            }
            let (Some(pt), Some(w)) = (vpt, rtc.writer(vmid)) else { continue };
            let rtp = MediaTime::new(at.as_micros() as u64 * 9 / 100, Frequency::NINETY_KHZ);
            if w.write(pt, Instant::now(), rtp, au).is_ok() { frames.fetch_add(1, Ordering::Relaxed); }
        }
        // Sound: every 20 ms the agent's voice or silence, nothing while muted.
        if let (Some(m), Some(pt)) = (amid, apt) {
            let now = Instant::now();
            while next_audio <= now {
                next_audio += Duration::from_millis(20);
                audio_ts += 960;
                if ctl.muted.load(Ordering::SeqCst) { continue; }
                let frame = ctl.voice.lock().unwrap().pop_front().unwrap_or_else(|| OPUS_SILENCE.to_vec());
                if let Some(w) = rtc.writer(m) { let _ = w.write(pt, now, MediaTime::new(audio_ts, Frequency::FORTY_EIGHT_KHZ), frame); }
            }
        }
        if last_report.elapsed() >= Duration::from_secs(2) {
            last_report = Instant::now();
            say(&format!("{{\"frames\":{}}}", frames.load(Ordering::Relaxed)));
        }
        let _ = o;
    }
}

/// Whether an access unit holds an IDR slice (NAL type 5).
fn is_key(au: &[u8]) -> bool {
    let mut i = 0;
    while i + 3 < au.len() {
        if au[i] == 0 && au[i + 1] == 0 && au[i + 2] == 1 {
            if au[i + 3] & 0x1f == 5 { return true; }
            i += 3;
        } else { i += 1; }
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn the_key_goes_only_to_the_endpoints_origin() {
        assert_eq!(resource_url("https://live.example/whip/x", "/whip/x/res1").as_deref(), Some("https://live.example/whip/x/res1"));
        assert_eq!(resource_url("https://live.example/whip/x", "https://live.example/r/2").as_deref(), Some("https://live.example/r/2"));
        assert_eq!(resource_url("https://live.example/whip/x", "https://evil.example/r/2"), None);
    }
    #[test]
    fn host_and_port() {
        assert_eq!(host_port("https://live.example/whip").unwrap(), ("live.example".to_string(), 443));
        assert_eq!(host_port("http://127.0.0.1:8090/whip").unwrap(), ("127.0.0.1".to_string(), 8090));
    }
    #[test]
    fn idr_is_seen() {
        assert!(is_key(&[0, 0, 0, 1, 0x67, 1, 0, 0, 1, 0x65, 0x88]));
        assert!(!is_key(&[0, 0, 0, 1, 0x41, 0x9a]));
    }
}
