//! `ov-desk browser -- <chrome argv…>`: the computer plugin's browser behind a Rust relay (Link v0.77).
//!
//! The owner, 2026-11-17: Rust wherever it gains at all. The plugin's CDP client is Python; raced against the same
//! client in Rust on the same Chrome, Python spent 2.6–2.8× the CPU a call (a 130 KB screenshot: 1,237 µs against 455)
//! and 1.8× the memory. The hottest path is a browser desk's live view, a screenshot five times a second that Python
//! JSON-decoded, base64-decoded and wrote out. So ov-desk owns Chrome's pipe:
//!
//! - every message passes through unchanged both ways (the plugin's own CDP calls, ids and sessions as they were), so
//!   everything the plugin does keeps working;
//! - `OV.frame {path, max_width, quality}` (with the page's sessionId) is answered here: the layout metrics and the
//!   screenshot are asked from Chrome, the JPEG is decoded from base64 and written whole to path (then moved into
//!   place), and the plugin only gets `{bytes, width}` back;
//! - `ov-desk browser --check` prints `ok`: the plugin uses the relay only then (an older ov-desk has no browser).
//!
//! fds 3 and 4 are the plugin's side (it writes to 3, reads 4), one JSON message per NUL, as Chrome's own
//! --remote-debugging-pipe; Chrome gets its own pair. It ends when either side closes.
use serde_json::{json, Value};
use std::collections::HashMap;
use std::io::{Read, Write};
use std::os::fd::{FromRawFd, RawFd};
use std::os::unix::process::CommandExt;
use std::process::{Command, Stdio};
use std::sync::mpsc::{channel, Sender};
use std::sync::{Arc, Mutex};
use std::time::Duration;

/// Ids of the calls ov-desk makes itself: far above any the plugin uses, so the answers can be told apart.
const OWN_IDS: u64 = 1 << 30;   // CDP ids are 32-bit in Chrome: 2^30 and up, below 2^31

type Waiting = Arc<Mutex<HashMap<u64, Sender<Value>>>>;

/// Reads NUL-ended messages from r and hands each (without its NUL) to f; ends at EOF or when f says false.
fn messages(mut r: impl Read, mut f: impl FnMut(&[u8]) -> bool) {
    let mut buf: Vec<u8> = Vec::with_capacity(1 << 20);
    let mut chunk = vec![0u8; 1 << 16];
    let mut scanned = 0usize;
    loop {
        let n = match r.read(&mut chunk) { Ok(0) | Err(_) => return, Ok(n) => n };
        buf.extend_from_slice(&chunk[..n]);
        let mut start = 0usize;
        while let Some(off) = buf[scanned..].iter().position(|&b| b == 0) {
            let end = scanned + off;
            if !f(&buf[start..end]) { return; }
            start = end + 1;
            scanned = start;
        }
        if start > 0 { buf.drain(..start); }
        scanned = buf.len();
    }
}

/// The id at the head of a message without parsing it whole: Chrome writes `{"id":N,…` for an answer.
fn head_id(m: &[u8]) -> Option<u64> {
    let rest = m.strip_prefix(b"{\"id\":")?;
    let digits = rest.iter().take_while(|b| b.is_ascii_digit()).count();
    std::str::from_utf8(&rest[..digits]).ok()?.parse().ok()
}

struct Relay {
    to_chrome: Mutex<std::fs::File>,
    to_plugin: Mutex<std::fs::File>,
    waiting: Waiting,
    next: Mutex<u64>,
}

impl Relay {
    fn send_chrome(&self, bytes: &[u8]) -> std::io::Result<()> {
        let mut w = self.to_chrome.lock().unwrap();
        w.write_all(bytes)?;
        w.write_all(b"\0")
    }
    fn send_plugin(&self, v: &Value) {
        let mut bytes = serde_json::to_vec(v).unwrap_or_default();
        bytes.push(0);
        let _ = self.to_plugin.lock().unwrap().write_all(&bytes);
    }
    /// One call to Chrome on ov-desk's own id → its result, or the error message.
    fn call(&self, method: &str, params: Value, session: Option<&str>) -> Result<Value, String> {
        let id = { let mut n = self.next.lock().unwrap(); *n = if *n + 1 >= (1 << 31) - 1 { OWN_IDS + 1 } else { *n + 1 }; *n };
        let (tx, rx) = channel();
        self.waiting.lock().unwrap().insert(id, tx);
        let mut msg = json!({ "id": id, "method": method, "params": params });
        if let Some(s) = session { msg["sessionId"] = json!(s); }
        let bytes = serde_json::to_vec(&msg).map_err(|e| e.to_string())?;
        self.send_chrome(&bytes).map_err(|e| format!("the browser closed: {e}"))?;
        let r = rx.recv_timeout(Duration::from_secs(15)).map_err(|_| format!("the browser did not answer {method} in time"))?;
        if let Some(e) = r.get("error") { return Err(format!("{method}: {}", e.get("message").and_then(|m| m.as_str()).unwrap_or("refused"))); }
        Ok(r.get("result").cloned().unwrap_or(Value::Null))
    }

    /// OV.frame: one JPEG picture of the page, at most max_width wide, written whole to path.
    fn frame(&self, params: &Value, session: Option<&str>) -> Result<Value, String> {
        let path = params.get("path").and_then(|p| p.as_str()).ok_or("path is needed")?;
        if !path.starts_with('/') { return Err("path must be absolute".into()); }
        let max_width = params.get("max_width").and_then(|v| v.as_f64()).unwrap_or(1280.0).max(64.0);
        let quality = params.get("quality").and_then(|v| v.as_u64()).unwrap_or(70).clamp(10, 95);
        let m = self.call("Page.getLayoutMetrics", json!({}), session)?;
        let view = m.get("cssVisualViewport").or_else(|| m.get("visualViewport")).cloned().unwrap_or(Value::Null);
        let width = view.get("clientWidth").and_then(|v| v.as_f64()).unwrap_or(1280.0);
        let height = view.get("clientHeight").and_then(|v| v.as_f64()).unwrap_or(900.0);
        let mut shot = json!({ "format": "jpeg", "quality": quality });
        if width > max_width {
            shot["clip"] = json!({ "x": 0, "y": 0, "width": width, "height": height, "scale": max_width / width });
        }
        let r = self.call("Page.captureScreenshot", shot, session)?;
        let b64 = r.get("data").and_then(|d| d.as_str()).ok_or("no picture")?;
        use base64::Engine;
        let bytes = base64::engine::general_purpose::STANDARD.decode(b64).map_err(|e| e.to_string())?;
        let part = format!("{path}.part");
        std::fs::write(&part, &bytes).map_err(|e| e.to_string())?;
        std::fs::rename(&part, path).map_err(|e| e.to_string())?;
        Ok(json!({ "bytes": bytes.len(), "width": width.min(max_width) }))
    }
}

pub fn run(args: &[String]) -> i32 {
    if args.first().map(|a| a == "--check").unwrap_or(false) { println!("ok"); return 0; }
    let Some(i) = args.iter().position(|a| a == "--") else { eprintln!("ov-desk browser -- <chrome argv…>"); return 2 };
    let argv = &args[i + 1..];
    if argv.is_empty() { eprintln!("ov-desk browser: no browser to run"); return 2; }
    // Chrome's own pipe pair: it reads commands on its 3 and writes on its 4.
    let mut to = [0 as RawFd; 2];
    let mut from = [0 as RawFd; 2];
    unsafe {
        if libc::pipe2(to.as_mut_ptr(), libc::O_CLOEXEC) != 0 || libc::pipe2(from.as_mut_ptr(), libc::O_CLOEXEC) != 0 {
            eprintln!("ov-desk browser: no pipe");
            return 1;
        }
    }
    let (to_r, to_w, from_r, from_w) = (to[0], to[1], from[0], from[1]);
    let mut cmd = Command::new(&argv[0]);
    cmd.args(&argv[1..]).stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::inherit());
    unsafe {
        cmd.pre_exec(move || {
            // Above 10 first, so placing one never closes the other; dup2 leaves 3 and 4 open across exec.
            let r = libc::fcntl(to_r, libc::F_DUPFD, 10);
            let w = libc::fcntl(from_w, libc::F_DUPFD, 10);
            if r < 0 || w < 0 || libc::dup2(r, 3) < 0 || libc::dup2(w, 4) < 0 { return Err(std::io::Error::last_os_error()); }
            Ok(())
        });
    }
    let mut child = match cmd.spawn() { Ok(c) => c, Err(e) => { eprintln!("ov-desk browser: {e}"); return 1; } };
    unsafe { libc::close(to_r); libc::close(from_w); }
    let relay = Arc::new(Relay {
        to_chrome: Mutex::new(unsafe { std::fs::File::from_raw_fd(to_w) }),
        to_plugin: Mutex::new(unsafe { std::fs::File::from_raw_fd(4) }),
        waiting: Arc::new(Mutex::new(HashMap::new())),
        next: Mutex::new(OWN_IDS),
    });
    // Chrome → the plugin: answers to ov-desk's own calls stay here, everything else goes on as it came.
    {
        let relay = relay.clone();
        let from_chrome = unsafe { std::fs::File::from_raw_fd(from_r) };
        std::thread::spawn(move || {
            messages(from_chrome, |m| {
                if let Some(id) = head_id(m) {
                    if id >= OWN_IDS {
                        if let Ok(v) = serde_json::from_slice::<Value>(m) {
                            if let Some(tx) = relay.waiting.lock().unwrap().remove(&id) { let _ = tx.send(v); }
                        }
                        return true;
                    }
                }
                let mut w = relay.to_plugin.lock().unwrap();
                w.write_all(m).is_ok() && w.write_all(b"\0").is_ok()
            });
            // Chrome is gone: the plugin's read end closes with this process.
            std::process::exit(0);
        });
    }
    // The plugin → Chrome: OV.* is answered here (on a thread, so a frame never holds up the plugin's other calls).
    let from_plugin = unsafe { std::fs::File::from_raw_fd(3) };
    messages(from_plugin, |m| {
        if m.windows(6).take(64).any(|w| w == b"\"OV.fr") {
            if let Ok(v) = serde_json::from_slice::<Value>(m) {
                if v.get("method").and_then(|x| x.as_str()) == Some("OV.frame") {
                    let relay = relay.clone();
                    std::thread::spawn(move || {
                        let id = v.get("id").cloned().unwrap_or(Value::Null);
                        let session = v.get("sessionId").and_then(|s| s.as_str()).map(|s| s.to_string());
                        let params = v.get("params").cloned().unwrap_or(Value::Null);
                        let answer = match relay.frame(&params, session.as_deref()) {
                            Ok(r) => json!({ "id": id, "result": r }),
                            Err(e) => json!({ "id": id, "error": { "code": -32000, "message": e } }),
                        };
                        relay.send_plugin(&answer);
                    });
                    return true;
                }
            }
        }
        relay.send_chrome(m).is_ok()
    });
    // The plugin closed its end: the browser goes with it.
    let _ = child.kill();
    let _ = child.wait();
    0
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn answers_are_told_apart_by_their_id() {
        assert_eq!(head_id(br#"{"id":42,"result":{}}"#), Some(42));
        assert_eq!(head_id(format!("{{\"id\":{},\"result\":{{}}}}", OWN_IDS + 3).as_bytes()), Some(OWN_IDS + 3));
        assert_eq!(head_id(br#"{"method":"Page.frameNavigated","params":{}}"#), None);
    }
    #[test]
    fn messages_split_on_nul_across_reads() {
        struct Slow<'a>(&'a [u8]);
        impl<'a> Read for Slow<'a> {
            fn read(&mut self, out: &mut [u8]) -> std::io::Result<usize> {
                let n = self.0.len().min(3).min(out.len());
                out[..n].copy_from_slice(&self.0[..n]);
                self.0 = &self.0[n..];
                Ok(n)
            }
        }
        let mut got = vec![];
        messages(Slow(b"{\"a\":1}\0{\"b\":2}\0{\"c\""), |m| { got.push(m.to_vec()); true });
        assert_eq!(got, vec![b"{\"a\":1}".to_vec(), b"{\"b\":2}".to_vec()]);
    }
}
