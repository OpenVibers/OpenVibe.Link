//! ov-desk: the Rust desk helper of a lent desktop. It speaks the protocol of Link's Go helper (`openvibe-link
//! desk-display`, internal/deskdisplay) exactly, so the computer plugin can run either:
//!
//!   ov-desk desk-display WIDTH HEIGHT APPS_JSON     (the plugin's pipe on fds 3 and 4)
//!   ov-desk bench [-display :97] [-seconds 60] [-fps 30] [-width 0] [-moves 2000]
//!
//! Why Rust (2026-11-17, the owner: "use rust … for the most ultimate maximized optimized efficient agent workers"): on
//! the same loop and the same libjpeg-turbo it measured less CPU a frame and less memory than the Go helper (the race
//! in docs/computer.md), and its platform bindings are the way to native capture on Windows and macOS.
mod display;
mod keys;
mod serve;

use std::os::fd::FromRawFd;
use std::time::{Duration, Instant};

const USAGE: &str = "usage: ov-desk desk-display WIDTH HEIGHT APPS_JSON\n       ov-desk bench [-display :0] [-seconds 10] [-fps 30] [-width 0] [-moves 2000]\n";

fn arg(args: &[String], name: &str) -> Option<String> {
    args.iter().position(|x| x == name).and_then(|i| args.get(i + 1)).cloned()
}

fn pct(v: &[f64], p: f64) -> f64 {
    if v.is_empty() { return 0.0; }
    let mut s = v.to_vec();
    s.sort_by(|a, b| a.partial_cmp(b).unwrap_or(std::cmp::Ordering::Equal));
    s[(p * (s.len() - 1) as f64) as usize]
}

fn rusage() -> (f64, i64) {
    let mut ru: libc::rusage = unsafe { std::mem::zeroed() };
    unsafe { libc::getrusage(libc::RUSAGE_SELF, &mut ru) };
    ((ru.ru_utime.tv_sec + ru.ru_stime.tv_sec) as f64 * 1000.0 + (ru.ru_utime.tv_usec + ru.ru_stime.tv_usec) as f64 / 1000.0, ru.ru_maxrss)
}

/// The desk loop timed: shots at a pace (or flat out), then pointer moves; one JSON line.
fn bench(args: &[String]) -> i32 {
    let disp = arg(args, "-display").unwrap_or_else(|| ":0".into());
    let num = |n: &str, d: f64| arg(args, n).and_then(|v| v.parse::<f64>().ok()).unwrap_or(d);
    let (secs, fps, width, moves) = (num("-seconds", 10.0), num("-fps", 30.0), num("-width", 0.0) as usize, num("-moves", 2000.0) as usize);
    let mut d = match display::Display::open(&disp) { Ok(d) => d, Err(e) => { eprintln!("{e}"); return 1; } };
    for _ in 0..10 { if let Err(e) = d.shot(width, 0) { eprintln!("{e}"); return 1; } }
    let (mut frames, mut bytes) = (Vec::new(), 0usize);
    let (cpu0, _) = rusage();
    let start = Instant::now();
    let end = start + Duration::from_secs_f64(secs);
    let mut next = start;
    while Instant::now() < end {
        let t0 = Instant::now();
        let (pic, _, _) = match d.shot(width, 0) { Ok(p) => p, Err(e) => { eprintln!("{e}"); return 1; } };
        frames.push(t0.elapsed().as_secs_f64() * 1000.0);
        bytes += pic.len();
        if fps > 0.0 {
            next += Duration::from_secs_f64(1.0 / fps);
            let now = Instant::now();
            if next > now { std::thread::sleep(next - now); }
        }
    }
    let wall = start.elapsed().as_secs_f64();
    let (cpu1, rss) = rusage();
    let mut mv = Vec::new();
    for i in 0..moves {
        let t0 = Instant::now();
        if d.move_to(((i * 37) % d.width) as i64, ((i * 53) % d.height) as i64).is_err() { eprintln!("move failed"); return 1; }
        mv.push(t0.elapsed().as_secs_f64() * 1000.0);
    }
    let (capture, encoder) = d.paths();
    let n = frames.len() as f64;
    println!("{}", serde_json::json!({
        "lang": "rust", "capture": capture, "encoder": encoder, "width": width, "fps": fps, "frames": frames.len(),
        "frames_per_s": n / wall, "frame_ms_p50": pct(&frames, 0.5), "frame_ms_p90": pct(&frames, 0.9), "frame_ms_p99": pct(&frames, 0.99),
        "frame_ms_max": pct(&frames, 1.0), "cpu_ms_per_frame": (cpu1 - cpu0) / n, "cpu_pct_of_core": (cpu1 - cpu0) / (wall * 1000.0) * 100.0,
        "max_rss_kb": rss, "jpeg_kb_avg": bytes as f64 / n / 1024.0, "move_ms_p50": pct(&mv, 0.5), "move_ms_p99": pct(&mv, 0.99),
    }));
    0
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let code = match args.get(1).map(String::as_str) {
        Some("desk-display") => {
            if args.len() != 5 { eprint!("{USAGE}"); std::process::exit(2); }
            let (w, h) = (args[2].parse::<usize>().unwrap_or(0), args[3].parse::<usize>().unwrap_or(0));
            if !(320..=7680).contains(&w) || !(240..=4320).contains(&h) { eprint!("{USAGE}"); std::process::exit(2); }
            let apps = match serve::parse_apps(&args[4]) { Ok(a) => a, Err(e) => { eprintln!("desk-display: {e}"); std::process::exit(2); } };
            // fds 3 and 4 are the plugin's pipe.
            let ok = unsafe { libc::fcntl(3, libc::F_GETFD) } >= 0 && unsafe { libc::fcntl(4, libc::F_GETFD) } >= 0;
            if !ok { eprintln!("desk-display: fds 3 and 4 are the plugin's pipe"); std::process::exit(2); }
            let (input, output) = unsafe { (std::fs::File::from_raw_fd(3), std::fs::File::from_raw_fd(4)) };
            match serve::serve(w, h, apps, input, output) { Ok(()) => 0, Err(e) => { eprintln!("desk-display: {e}"); 1 } }
        }
        Some("bench") => bench(&args[2..]),
        Some("--version") | Some("version") => { println!("ov-desk {}", env!("CARGO_PKG_VERSION")); 0 }
        _ => { eprint!("{USAGE}"); 2 }
    };
    std::process::exit(code);
}
