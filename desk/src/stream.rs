//! `ov-desk stream`: the screen as live H.264 (Annex B) on stdout, for the Link's screen stream (`screen_stream`). The
//! Link's core reads it as a video.CommandSource and publishes it over WHIP, as it does rpicam-vid or ffmpeg.
//!
//! Before this the screen stream was 1 to 5 JPEG pictures a second, each made by forking a screenshot tool, re-encoded
//! by ffmpeg (or the core's uncompressed fallback). Here the screen is read through MIT-SHM and encoded in-process by
//! OpenH264 in its screen-content mode (made for desktops: text stays sharp, still areas cost almost nothing), at 15 a
//! second by default, with a keyframe every 2 seconds and one at once on SIGUSR1 (the core's RequestKeyframe: a PLI or a
//! new viewer).
//!
//!   ov-desk stream [-display :0] [-fps 15] [-max-width 1600] [-kbps 2500] [-seconds 0]
//!
//! It ends when stdout closes (the core stopped reading), on SIGTERM, or after -seconds.
use crate::display::Display;
use openh264::encoder::{BitRate, Encoder, EncoderConfig, FrameRate, IntraFramePeriod, RateControlMode, UsageType};
use openh264::formats::YUVSlices;
use openh264::{OpenH264API, Timestamp};
use std::io::Write;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

/// Set by SIGUSR1: the Link's core asks for a keyframe (a PLI, a new viewer) and the next frame is an IDR.
static KEYFRAME: AtomicBool = AtomicBool::new(false);
extern "C" fn on_keyframe_signal(_: libc::c_int) { KEYFRAME.store(true, Ordering::SeqCst); }

pub struct Options { pub display: String, pub fps: u32, pub max_width: usize, pub kbps: u32, pub seconds: f64 }

impl Options {
    pub fn parse(args: &[String]) -> Options {
        let get = |n: &str| args.iter().position(|x| x == n).and_then(|i| args.get(i + 1)).cloned();
        let num = |n: &str, d: f64| get(n).and_then(|v| v.parse::<f64>().ok()).unwrap_or(d);
        let display = get("-display").or_else(|| std::env::var("DISPLAY").ok().filter(|d| !d.is_empty())).unwrap_or_else(|| ":0".into());
        Options {
            display,
            fps: num("-fps", 15.0).clamp(1.0, 30.0) as u32,
            max_width: num("-max-width", 1600.0).clamp(320.0, 3840.0) as usize,
            kbps: num("-kbps", 2500.0).clamp(200.0, 20000.0) as u32,
            seconds: num("-seconds", 0.0).max(0.0),
        }
    }
}

/// H.264 wants even sizes for its 4:2:0 picture: the last row or column is dropped when it is odd.
fn even(v: usize) -> usize { v & !1 }

pub fn run(o: &Options) -> Result<(), String> {
    let mut d = Display::open(&o.display)?;
    let (capture, _) = d.paths();
    let config = EncoderConfig::new()
        .usage_type(UsageType::ScreenContentRealTime)
        .rate_control_mode(RateControlMode::Bitrate)
        .bitrate(BitRate::from_bps(o.kbps * 1000))
        .max_frame_rate(FrameRate::from_hz(o.fps as f32))
        .intra_frame_period(IntraFramePeriod::from_num_frames(o.fps * 2))
        .skip_frames(true)
        .scene_change_detect(true);
    let mut enc = Encoder::with_api_config(OpenH264API::from_source(), config).map_err(|e| format!("encoder: {e}"))?;
    let (mut py, mut pu, mut pv) = (Vec::new(), Vec::new(), Vec::new());
    let mut packed: Vec<u8> = Vec::new();
    let mut au: Vec<u8> = Vec::with_capacity(256 * 1024);
    unsafe { libc::signal(libc::SIGUSR1, on_keyframe_signal as *const () as libc::sighandler_t) };
    let mut out = std::io::stdout().lock();
    let start = Instant::now();
    let end = if o.seconds > 0.0 { Some(start + Duration::from_secs_f64(o.seconds)) } else { None };
    let tick = Duration::from_secs_f64(1.0 / o.fps as f64);
    let mut next = start;
    let mut said = false;
    loop {
        if end.map(|e| Instant::now() >= e).unwrap_or(false) { return Ok(()); }
        let (pix, w, h) = d.frame_bgrx(o.max_width)?;
        let (ew, eh) = (even(w), even(h));
        // The encoder reads tightly packed rows of the even size.
        let src: &[u8] = if ew == w && eh == h { pix } else {
            packed.clear();
            for y in 0..eh { packed.extend_from_slice(&pix[y * w * 4..y * w * 4 + ew * 4]); }
            &packed
        };
        crate::yuv::bgrx_to_i420(src, ew, eh, &mut py, &mut pu, &mut pv);
        let planes = YUVSlices::new((&py, &pu, &pv), (ew, eh), (ew, ew / 2, ew / 2));
        if !said {
            eprintln!("ov-desk stream: {ew}×{eh} at {} a second, {} kbit/s, capture {capture}, H.264 screen content", o.fps, o.kbps);
            said = true;
        }
        if KEYFRAME.swap(false, Ordering::SeqCst) { enc.force_intra_frame(); }
        // Real timestamps: OpenH264's rate control and its keyframe requests go by them (encode() sends 0 every time).
        let bits = enc.encode_at(&planes, Timestamp::from_millis(start.elapsed().as_millis() as u64)).map_err(|e| format!("encode: {e}"))?;
        // One whole access unit, written whole (the crate's own write takes a short write as an error). The core closing
        // its end is how a stream normally stops.
        au.clear();
        bits.write_vec(&mut au);
        if !au.is_empty() && (out.write_all(&au).is_err() || out.flush().is_err()) { return Ok(()); }
        next += tick;
        let now = Instant::now();
        if next > now { std::thread::sleep(next - now); } else { next = now; }
    }
}
