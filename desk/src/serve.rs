//! A lent desktop inside the desk's sandbox (Link's internal/deskdisplay/serve.go, in Rust): starts Xvfb, then each app
//! with its output in HOME/.logs, then answers the plugin one JSON line per request on fds 3 (in) and 4 (out): shot (a
//! JPEG made here), xdotool (done in-process, or by xdotool itself where it is not), logs, stop. Returns when stop is
//! asked, the pipe closes or the display ends.
use crate::display::{Display, Fail};
use base64::Engine as _;
use serde_json::{json, Value};
use std::fs::{self, File, OpenOptions};
use std::io::{BufRead, BufReader, Read, Seek, SeekFrom, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::Duration;

pub struct App { pub name: String, pub argv: Vec<String> }

/// The plugin's apps JSON: [["wm", ["/usr/bin/openbox"]], ["browser", [...]], ...].
pub fn parse_apps(raw: &str) -> Result<Vec<App>, String> {
    let list: Vec<(String, Vec<String>)> = serde_json::from_str(raw).map_err(|e| format!("apps: {e}"))?;
    list.into_iter().map(|(name, argv)| if argv.is_empty() { Err("apps: argv is a list of strings".to_string()) } else { Ok(App { name, argv }) }).collect()
}

struct Proc { name: String, child: Child }
impl Proc {
    fn alive(&mut self) -> bool { matches!(self.child.try_wait(), Ok(None)) }
}

fn shell() -> &'static str { if Path::new("/bin/bash").exists() { "/bin/bash" } else { "/bin/sh" } }

fn tail(path: &Path, n: u64) -> String {
    let Ok(mut f) = File::open(path) else { return String::new() };
    let size = f.metadata().map(|m| m.len()).unwrap_or(0);
    let _ = f.seek(SeekFrom::Start(size.saturating_sub(n)));
    let mut b = Vec::new();
    let _ = f.read_to_end(&mut b);
    String::from_utf8_lossy(&b).into_owned()
}

fn clip(s: &str, n: usize) -> String { if s.len() > n { s.chars().take(n).collect() } else { s.to_string() } }

fn trimmed(argv: &[String]) -> String {
    let s = argv.join(" ");
    if s.chars().count() > 80 { format!("{}…", s.chars().take(80).collect::<String>()) } else { s }
}

/// The real xdotool, for what is not done in-process (a character with no key, an unknown verb).
fn fork_xdotool(argv: &[String], env: &[(String, String)]) -> Result<String, String> {
    if argv.first().map(|s| s != "xdotool").unwrap_or(true) { return Err("xdotool and its arguments".into()); }
    let path = ["/usr/bin/xdotool", "/bin/xdotool", "/usr/local/bin/xdotool"].iter().find(|p| Path::new(p).exists())
        .ok_or("this desktop has no xdotool for that input")?;
    let out = Command::new(path).args(&argv[1..]).env_clear().envs(env.iter().cloned()).stdin(Stdio::null()).output().map_err(|e| e.to_string())?;
    if !out.status.success() { return Err(format!("xdotool: {}", out.status)); }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

pub fn serve(width: usize, height: usize, apps: Vec<App>, input: File, mut output: File) -> Result<(), String> {
    let home = std::env::var("HOME").ok().filter(|h| !h.is_empty()).unwrap_or_else(|| "/tmp/desk".into());
    let env: Vec<(String, String)> = vec![
        ("DISPLAY".into(), ":0".into()), ("HOME".into(), home.clone()), ("TMPDIR".into(), home.clone()), ("PATH".into(), "/usr/bin:/bin".into()),
        ("LANG".into(), "C.UTF-8".into()), ("TERM".into(), "xterm-256color".into()), ("SHELL".into(), shell().into()),
        ("DBUS_SESSION_BUS_ADDRESS".into(), "disabled:".into()), ("XDG_RUNTIME_DIR".into(), format!("{home}/run")), ("NO_AT_BRIDGE".into(), "1".into()),
    ];
    let _ = fs::create_dir_all(format!("{home}/run"));
    let logs = PathBuf::from(&home).join(".logs");
    let _ = fs::create_dir_all(&logs);
    // Xvfb is not root in here, so it will not make its socket's directory itself.
    let _ = fs::create_dir_all("/tmp/.X11-unix");
    #[cfg(unix)]
    { use std::os::unix::fs::PermissionsExt; let _ = fs::set_permissions("/tmp/.X11-unix", fs::Permissions::from_mode(0o1777)); }

    let mut write = |v: Value| { let mut b = serde_json::to_vec(&v).unwrap_or_default(); b.push(b'\n'); let _ = output.write_all(&b); };
    let start = |name: &str, argv: &[String]| -> Result<Proc, String> {
        let f = OpenOptions::new().create(true).append(true).open(logs.join(format!("{name}.log"))).map_err(|e| e.to_string())?;
        let child = Command::new(&argv[0]).args(&argv[1..]).env_clear().envs(env.iter().cloned()).current_dir(&home)
            .stdin(Stdio::null()).stdout(f.try_clone().map_err(|e| e.to_string())?).stderr(f).spawn().map_err(|e| e.to_string())?;
        Ok(Proc { name: name.to_string(), child })
    };
    let mut procs: Vec<Proc> = Vec::new();
    let geometry = format!("{width}x{height}x24");
    let x = match start("display", &["Xvfb", ":0", "-screen", "0", &geometry, "-nolisten", "tcp", "-noreset"].map(String::from)) {
        Ok(p) => p,
        Err(e) => { write(json!({"ready": false, "error": format!("the display did not start: {e}")})); return Err(e); }
    };
    procs.push(x);
    let mut d = None;
    for _ in 0..150 {
        if Path::new("/tmp/.X11-unix/X0").exists() {
            if let Ok(x) = Display::open(":0") { d = Some(x); break; }
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    let Some(mut d) = d else {
        write(json!({"ready": false, "error": "the display did not start"}));
        end(&mut procs);
        return Err("the display did not start".into());
    };
    let mut started = Vec::new();
    for a in &apps {
        if let Ok(p) = start(&a.name, &a.argv) {
            procs.push(p);
            started.push(a.name.clone());
            if a.name == "wm" { std::thread::sleep(Duration::from_millis(300)); }
        }
    }
    let (capture, encoder) = d.paths();
    write(json!({"ready": true, "apps": started, "native": true, "helper": "ov-desk", "capture": capture, "encoder": encoder}));

    let result = (|| {
        for line in BufReader::new(input).lines() {
            let Ok(line) = line else { break };
            let Ok(req) = serde_json::from_str::<Value>(&line) else { continue };
            let op = req.get("op").and_then(Value::as_str).unwrap_or("");
            let mut reply = json!({"id": req.get("id").cloned().unwrap_or(Value::Null)});
            match op {
                "shot" => {
                    let mw = req.get("max_width").and_then(Value::as_u64).unwrap_or(0) as usize;
                    let mb = req.get("max_bytes").and_then(Value::as_u64).unwrap_or(0) as usize;
                    match d.shot(mw, mb) {
                        Ok((pic, w, h)) => { reply["ok"] = json!(true); reply["jpeg"] = json!(base64::engine::general_purpose::STANDARD.encode(pic)); reply["width"] = json!(w); reply["height"] = json!(h); }
                        Err(e) => { reply["ok"] = json!(false); reply["err"] = json!(e); }
                    }
                }
                "xdotool" => {
                    let argv: Vec<String> = req.get("argv").and_then(Value::as_array).map(|a| a.iter().filter_map(|v| v.as_str().map(String::from)).collect()).unwrap_or_default();
                    let r = match d.xdotool(&argv) {
                        Err(Fail::Fallback) => fork_xdotool(&argv, &env),
                        Err(Fail::Msg(m)) => Err(m),
                        Ok(o) => Ok(o),
                    };
                    match r {
                        Ok(o) => { reply["ok"] = json!(true); reply["out"] = json!(o); }
                        Err(e) => { reply["ok"] = json!(false); reply["out"] = json!(""); reply["err"] = json!(clip(&format!("{e} ({})", trimmed(&argv)), 300)); }
                    }
                }
                "logs" => {
                    let mut names: Vec<PathBuf> = fs::read_dir(&logs).map(|r| r.filter_map(|e| e.ok().map(|e| e.path())).filter(|p| p.extension().map(|x| x == "log").unwrap_or(false)).collect()).unwrap_or_default();
                    names.sort();
                    let tails: serde_json::Map<String, Value> = names.iter().take(8).map(|n| (n.file_stem().unwrap_or_default().to_string_lossy().into_owned(), json!(tail(n, 2048)))).collect();
                    let running: serde_json::Map<String, Value> = procs.iter_mut().map(|p| (p.name.clone(), json!(p.alive()))).collect();
                    reply["ok"] = json!(true); reply["logs"] = Value::Object(tails); reply["running"] = Value::Object(running);
                }
                "stop" => { reply["ok"] = json!(true); write(reply); return Ok(()); }
                _ => { reply["ok"] = json!(false); reply["err"] = json!("unknown op"); }
            }
            write(reply);
            if !procs[0].alive() { write(json!({"ended": true})); return Err("the display ended".to_string()); }
        }
        Ok(())
    })();
    drop(d);
    end(&mut procs);
    result
}

/// Ends every program the desktop started: an interrupt, last first, then a kill for what is still running.
fn end(procs: &mut [Proc]) {
    for p in procs.iter().rev() { unsafe { libc::kill(p.child.id() as i32, libc::SIGINT) }; }
    std::thread::sleep(Duration::from_millis(300));
    for p in procs.iter_mut() { if p.alive() { let _ = p.child.kill(); } let _ = p.child.wait(); }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn apps_like_link() {
        let a = parse_apps(r#"[["wm", ["/usr/bin/openbox"]], ["browser", ["/usr/bin/chromium", "--x"]]]"#).unwrap();
        assert_eq!((a[0].name.as_str(), a[1].argv.len()), ("wm", 2));
        assert!(parse_apps(r#"[["wm", []]]"#).is_err());
        assert!(parse_apps("nope").is_err());
    }
}
