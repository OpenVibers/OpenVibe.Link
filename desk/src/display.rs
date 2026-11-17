//! One X display driven in-process (Link's internal/deskdisplay/x11.go, in Rust): the screen read through MIT-SHM (else
//! GetImage), shrunk when asked as X's own BGRX pixels, encoded by libjpeg-turbo (loaded at run time; a pure-Rust encoder
//! where the library is missing), and the mouse and keyboard through XTest. Every input is a checked round trip.
use crate::keys;
use std::collections::HashMap;
use x11rb::connection::{Connection, RequestConnection};
use x11rb::protocol::shm::{self, ConnectionExt as _};
use x11rb::protocol::xproto::{self, ConnectionExt as _};
use x11rb::protocol::xtest::{self, ConnectionExt as _};
use x11rb::rust_connection::RustConnection;

/// An input this helper does not do itself (a character with no key here, an xdotool verb it does not know): the
/// caller runs the real xdotool for it.
#[derive(Debug)]
pub enum Fail {
    Fallback,
    Msg(String),
}
impl From<String> for Fail {
    fn from(s: String) -> Self { Fail::Msg(s) }
}
fn msg<E: std::fmt::Display>(what: &str) -> impl FnOnce(E) -> Fail + '_ { move |e| Fail::Msg(format!("{what}: {e}")) }

#[derive(Clone, Copy)]
struct Key { code: u8, shift: bool }

struct Shm { seg: u32, addr: *mut u8, size: usize }

type TjInit = unsafe extern "C" fn() -> *mut libc::c_void;
type TjCompress2 = unsafe extern "C" fn(*mut libc::c_void, *const u8, i32, i32, i32, i32, *mut *mut u8, *mut libc::c_ulong, i32, i32, i32) -> i32;
type TjFree = unsafe extern "C" fn(*mut u8);
struct Turbo { _lib: libloading::Library, handle: *mut libc::c_void, compress: TjCompress2, free: TjFree }
const TJPF_BGRX: i32 = 3;
const TJSAMP_420: i32 = 2;
const TJFLAG_FASTDCT: i32 = 2048;

impl Turbo {
    fn load() -> Option<Turbo> {
        let names = ["libturbojpeg.so.0", "libturbojpeg.so", "/usr/lib/x86_64-linux-gnu/libturbojpeg.so.0", "/usr/lib/aarch64-linux-gnu/libturbojpeg.so.0"];
        let lib = names.iter().find_map(|n| unsafe { libloading::Library::new(n).ok() })?;
        unsafe {
            let init: TjInit = *lib.get::<TjInit>(b"tjInitCompress\0").ok()?;
            let compress: TjCompress2 = *lib.get::<TjCompress2>(b"tjCompress2\0").ok()?;
            let free: TjFree = *lib.get::<TjFree>(b"tjFree\0").ok()?;
            let handle = init();
            if handle.is_null() { return None; }
            Some(Turbo { _lib: lib, handle, compress, free })
        }
    }
    fn encode(&self, bgrx: &[u8], w: usize, h: usize, pitch: usize, quality: i32) -> Option<Vec<u8>> {
        if bgrx.len() < pitch * h { return None; }
        let mut out: *mut u8 = std::ptr::null_mut();
        let mut size: libc::c_ulong = 0;
        let rc = unsafe { (self.compress)(self.handle, bgrx.as_ptr(), w as i32, pitch as i32, h as i32, TJPF_BGRX, &mut out, &mut size, TJSAMP_420, quality, TJFLAG_FASTDCT) };
        if rc != 0 || out.is_null() {
            if !out.is_null() { unsafe { (self.free)(out) }; }
            return None;
        }
        let pic = unsafe { std::slice::from_raw_parts(out, size as usize).to_vec() };
        unsafe { (self.free)(out) };
        Some(pic)
    }
}

pub struct Display {
    conn: RustConnection,
    root: xproto::Window,
    pub width: usize,
    pub height: usize,
    keys: HashMap<u32, Key>,
    shift_k: u8,
    shm: Option<Shm>,
    turbo: Option<Turbo>,
    scaled: Vec<u8>,
    rgb: Vec<u8>,
}

impl Display {
    /// Connects to the display (":0") and reads its size and keyboard map.
    pub fn open(name: &str) -> Result<Display, String> {
        let (conn, screen_num) = x11rb::connect(Some(name)).map_err(|e| format!("display {name}: {e}"))?;
        if conn.extension_information(xtest::X11_EXTENSION_NAME).ok().flatten().is_none() {
            return Err(format!("display {name} has no XTEST"));
        }
        let setup = conn.setup();
        let scr = &setup.roots[screen_num];
        let (root, width, height, depth) = (scr.root, scr.width_in_pixels as usize, scr.height_in_pixels as usize, scr.root_depth);
        let (min, max) = (setup.min_keycode, setup.max_keycode);
        let bpp = setup.pixmap_formats.iter().find(|f| f.depth == depth).map(|f| f.bits_per_pixel).unwrap_or(0);
        let km = conn.get_keyboard_mapping(min, max - min + 1).map_err(|e| e.to_string())?.reply().map_err(|e| format!("keyboard map: {e}"))?;
        let per = km.keysyms_per_keycode as usize;
        let mut keys: HashMap<u32, Key> = HashMap::new();
        if per > 0 {
            for (i, row) in km.keysyms.chunks(per).enumerate() {
                let code = min as usize + i;
                for (col, &ks) in row.iter().take(2).enumerate() {
                    if ks != 0 { keys.entry(ks).or_insert(Key { code: code as u8, shift: col == 1 }); }
                }
            }
        }
        let shift_k = keys.get(&0xffe1).map(|k| k.code).unwrap_or(0);
        let mut d = Display { conn, root, width, height, keys, shift_k, shm: None, turbo: Turbo::load(), scaled: Vec::new(), rgb: Vec::new() };
        if bpp == 32 { d.shm = d.open_shm().ok(); }
        Ok(d)
    }

    fn open_shm(&self) -> Result<Shm, String> {
        self.conn.extension_information(shm::X11_EXTENSION_NAME).ok().flatten().ok_or("no MIT-SHM")?;
        let size = self.width * self.height * 4;
        let id = unsafe { libc::shmget(libc::IPC_PRIVATE, size, libc::IPC_CREAT | 0o600) };
        if id < 0 { return Err("shmget".into()); }
        let addr = unsafe { libc::shmat(id, std::ptr::null(), 0) };
        // Marked for removal at once: the segment lives while it is attached and goes when both sides let go or die.
        let drop_id = || unsafe { libc::shmctl(id, libc::IPC_RMID, std::ptr::null_mut()) };
        if addr as isize == -1 { drop_id(); return Err("shmat".into()); }
        let seg = self.conn.generate_id().map_err(|e| e.to_string())?;
        let ok = self.conn.shm_attach(seg, id as u32, false).map_err(|e| e.to_string()).and_then(|c| c.check().map_err(|e| e.to_string()));
        drop_id();
        if let Err(e) = ok { unsafe { libc::shmdt(addr) }; return Err(e); }
        Ok(Shm { seg, addr: addr as *mut u8, size })
    }

    /// How pictures are made here: the screen read ("shm" or "socket") and the encoder ("libjpeg-turbo" or "rust").
    pub fn paths(&self) -> (&'static str, &'static str) {
        (if self.shm.is_some() { "shm" } else { "socket" }, if self.turbo.is_some() { "libjpeg-turbo" } else { "rust" })
    }

    /// The whole screen as X's 32-bit pixels: through the shared segment, else (or once that fails) over the socket.
    fn read(&mut self) -> Result<Vec<u8>, String> {
        let (w, h) = (self.width as u16, self.height as u16);
        if let Some(s) = &self.shm {
            let ok = self.conn.shm_get_image(self.root, 0, 0, w, h, 0xffffffff, xproto::ImageFormat::Z_PIXMAP.into(), s.seg, 0)
                .map_err(|e| e.to_string()).and_then(|c| c.reply().map_err(|e| e.to_string()));
            if ok.is_ok() {
                // A view, not a copy, would be cheaper still; the segment is reused by the next read, so the caller
                // gets the pixels it may keep.
                return Ok(unsafe { std::slice::from_raw_parts(s.addr, s.size) }.to_vec());
            }
            if let Some(s) = self.shm.take() { let _ = self.conn.shm_detach(s.seg); unsafe { libc::shmdt(s.addr as *const libc::c_void) }; }
        }
        let img = self.conn.get_image(xproto::ImageFormat::Z_PIXMAP, self.root, 0, 0, w, h, 0xffffffff)
            .map_err(|e| e.to_string())?.reply().map_err(|e| format!("the screen could not be read: {e}"))?;
        if img.data.len() < self.width * self.height * 4 {
            return Err(format!("the screen came back short ({} bytes for {}×{}: depth {})", img.data.len(), self.width, self.height, img.depth));
        }
        Ok(img.data)
    }

    /// The screen without a copy when it can be: the shared segment itself, valid until the next read.
    fn frame(&mut self) -> Result<Frame, String> {
        if let Some(s) = &self.shm {
            let (w, h) = (self.width as u16, self.height as u16);
            let ok = self.conn.shm_get_image(self.root, 0, 0, w, h, 0xffffffff, xproto::ImageFormat::Z_PIXMAP.into(), s.seg, 0)
                .map_err(|e| e.to_string()).and_then(|c| c.reply().map_err(|e| e.to_string()));
            if ok.is_ok() { return Ok(Frame::Shared(s.addr, s.size)); }
        }
        self.read().map(Frame::Owned)
    }

    /// The screen as X's BGRX pixels no wider than max_width (0: its own width), tightly packed: the shared segment
    /// itself when it is the screen's own size, else the shrunk copy. Valid until the next read. For the live stream.
    pub fn frame_bgrx(&mut self, max_width: usize) -> Result<(&[u8], usize, usize), String> {
        let frame = self.frame()?;
        let (sw, sh) = (self.width, self.height);
        let w = if max_width > 0 && max_width < sw { max_width } else { sw };
        if w < sw {
            let src: &[u8] = match &frame { Frame::Shared(p, n) => unsafe { std::slice::from_raw_parts(*p, *n) }, Frame::Owned(v) => v };
            let h = shrink_bgrx(&mut self.scaled, src, sw, sh, w);
            return Ok((&self.scaled[..w * h * 4], w, h));
        }
        match frame {
            Frame::Shared(p, n) => Ok((unsafe { std::slice::from_raw_parts(p, n) }, sw, sh)),
            Frame::Owned(v) => { self.scaled = v; Ok((&self.scaled[..sw * sh * 4], sw, sh)) }
        }
    }

    /// The whole screen as a JPEG no wider than max_width (0: its own width) and, by lowering the quality and then the
    /// width, no larger than max_bytes (0: no bound). Answers the picture and its size.
    pub fn shot(&mut self, max_width: usize, max_bytes: usize) -> Result<(Vec<u8>, usize, usize), String> {
        let frame = self.frame()?;
        let src: &[u8] = match &frame { Frame::Shared(p, n) => unsafe { std::slice::from_raw_parts(*p, *n) }, Frame::Owned(v) => v };
        let mut w = self.width;
        if max_width > 0 && max_width < w { w = max_width; }
        let (mut quality, mut made_w) = (75, usize::MAX);
        let (mut fw, mut fh) = (self.width, self.height);
        loop {
            if w != made_w {
                made_w = w;
                if w < self.width {
                    fh = shrink_bgrx(&mut self.scaled, src, self.width, self.height, w);
                    fw = w;
                } else {
                    fw = self.width;
                    fh = self.height;
                }
            }
            let pix: &[u8] = if fw < self.width { &self.scaled } else { src };
            let pic = encode(self.turbo.as_ref(), &mut self.rgb, pix, fw, fh, fw * 4, quality)?;
            if max_bytes == 0 || pic.len() <= max_bytes || w <= 320 { return Ok((pic, fw, fh)); }
            if quality > 45 { quality -= 15; } else { w = std::cmp::max(320, w * 3 / 4); }
        }
    }

    fn fake(&self, kind: u8, detail: u8, x: i16, y: i16) -> Result<(), Fail> {
        self.conn.xtest_fake_input(kind, detail, 0, self.root, x, y, 0).map_err(msg("input"))?.check().map_err(msg("input"))
    }

    /// Puts the pointer at (x, y), clamped to the screen.
    pub fn move_to(&self, x: i64, y: i64) -> Result<(), Fail> {
        let x = x.clamp(0, self.width as i64 - 1) as i16;
        let y = y.clamp(0, self.height as i64 - 1) as i16;
        self.fake(xproto::MOTION_NOTIFY_EVENT, 0, x, y)
    }

    /// Presses and releases a button (1 left, 2 middle, 3 right, 4/5 the wheel) repeat times.
    pub fn click(&self, button: i64, repeat: i64) -> Result<(), Fail> {
        if !(1..=9).contains(&button) || !(1..=50).contains(&repeat) { return Err(Fail::Msg("click: button 1-9, repeat 1-50".into())); }
        for _ in 0..repeat {
            self.fake(xproto::BUTTON_PRESS_EVENT, button as u8, 0, 0)?;
            self.fake(xproto::BUTTON_RELEASE_EVENT, button as u8, 0, 0)?;
        }
        Ok(())
    }

    fn press(&self, code: u8, down: bool) -> Result<(), Fail> {
        self.fake(if down { xproto::KEY_PRESS_EVENT } else { xproto::KEY_RELEASE_EVENT }, code, 0, 0)
    }

    /// A chord ("ctrl+s", "Return", "alt+Tab"): each key down in order, then up in reverse.
    pub fn key(&self, spec: &str) -> Result<(), Fail> {
        let syms = keys::chord(spec).ok_or(Fail::Fallback)?;
        let codes: Vec<Key> = syms.iter().map(|s| self.keys.get(s).copied().ok_or(Fail::Fallback)).collect::<Result<_, _>>()?;
        let shift = codes.len() == 1 && codes[0].shift && self.shift_k != 0;
        if shift { self.press(self.shift_k, true)?; }
        for k in &codes { self.press(k.code, true)?; }
        for k in codes.iter().rev() { self.press(k.code, false)?; }
        if shift { self.press(self.shift_k, false)?; }
        Ok(())
    }

    /// Types text a character at a time; a character with no key on this keymap is Fallback, before any is typed.
    pub fn type_text(&self, text: &str) -> Result<(), Fail> {
        let mut plan = Vec::with_capacity(text.len());
        for c in text.chars() {
            let ks = match c { '\n' => 0xff0d, '\t' => 0xff09, c if (c as u32) > 0x7e => return Err(Fail::Fallback), c => c as u32 };
            plan.push(*self.keys.get(&ks).ok_or(Fail::Fallback)?);
        }
        for k in plan {
            let shift = k.shift && self.shift_k != 0;
            if shift { self.press(self.shift_k, true)?; }
            self.press(k.code, true)?;
            self.press(k.code, false)?;
            if shift { self.press(self.shift_k, false)?; }
        }
        Ok(())
    }

    /// The xdotool commands the computer plugin sends, done in-process: mousemove X Y, click [--repeat N] B (chained),
    /// key [--] CHORD, type [--delay N] [--] TEXT, getdisplaygeometry. Anything else is Fallback. Answers what xdotool
    /// would print.
    pub fn xdotool(&self, argv: &[String]) -> Result<String, Fail> {
        if argv.len() < 2 || argv[0] != "xdotool" { return Err(Fail::Fallback); }
        let mut args = &argv[1..];
        let num = |s: &String| s.parse::<i64>().map_err(|_| Fail::Fallback);
        while let Some(verb) = args.first() {
            match verb.as_str() {
                "getdisplaygeometry" => return Ok(format!("{} {}\n", self.width, self.height)),
                "mousemove" => {
                    if args.len() < 3 { return Err(Fail::Fallback); }
                    self.move_to(num(&args[1])?, num(&args[2])?)?;
                    args = &args[3..];
                }
                "click" => {
                    let (mut repeat, mut rest) = (1, &args[1..]);
                    if rest.len() >= 2 && rest[0] == "--repeat" { repeat = num(&rest[1])?; rest = &rest[2..]; }
                    let b = num(rest.first().ok_or(Fail::Fallback)?)?;
                    self.click(b, repeat)?;
                    args = &rest[1..];
                }
                "key" => {
                    let mut rest = &args[1..];
                    if rest.first().map(|s| s == "--").unwrap_or(false) { rest = &rest[1..]; }
                    if rest.len() != 1 { return Err(Fail::Fallback); }
                    return self.key(&rest[0]).map(|_| String::new());
                }
                "type" => {
                    let mut rest = &args[1..];
                    if rest.len() >= 2 && rest[0] == "--delay" { rest = &rest[2..]; }
                    if rest.first().map(|s| s == "--").unwrap_or(false) { rest = &rest[1..]; }
                    if rest.len() != 1 { return Err(Fail::Fallback); }
                    return self.type_text(&rest[0]).map(|_| String::new());
                }
                _ => return Err(Fail::Fallback),
            }
        }
        Ok(String::new())
    }
}

impl Drop for Display {
    fn drop(&mut self) {
        if let Some(s) = self.shm.take() {
            let _ = self.conn.shm_detach(s.seg);
            unsafe { libc::shmdt(s.addr as *const libc::c_void) };
        }
    }
}

enum Frame { Shared(*mut u8, usize), Owned(Vec<u8>) }

/// A BGRX frame as a JPEG: libjpeg-turbo where it is installed, else the pure-Rust encoder (BGRX turned into RGB).
fn encode(turbo: Option<&Turbo>, rgb: &mut Vec<u8>, pix: &[u8], w: usize, h: usize, pitch: usize, quality: i32) -> Result<Vec<u8>, String> {
    if let Some(t) = turbo {
        if let Some(pic) = t.encode(pix, w, h, pitch, quality) { return Ok(pic); }
    }
    rgb.clear();
    rgb.reserve(w * h * 3);
    for y in 0..h {
        for px in pix[y * pitch..y * pitch + w * 4].chunks_exact(4) { rgb.extend_from_slice(&[px[2], px[1], px[0]]); }
    }
    let mut out = Vec::new();
    jpeg_encoder::Encoder::new(&mut out, quality as u8)
        .encode(rgb, w as u16, h as u16, jpeg_encoder::ColorType::Rgb)
        .map_err(|e| format!("encode: {e}"))?;
    Ok(out)
}

/// Shrinks a BGRX frame (sw×sh, tightly packed) to w wide, keeping its shape, into dst (grown when too small): each output
/// pixel averages the source pixels it covers, so a picture of text stays legible. Answers the new height. Link's
/// shrinkBGRX, line for line.
pub fn shrink_bgrx(dst: &mut Vec<u8>, src: &[u8], sw: usize, sh: usize, w: usize) -> usize {
    if w >= sw {
        dst.clear();
        dst.extend_from_slice(&src[..sw * sh * 4]);
        return sh;
    }
    let h = std::cmp::max(1, sh * w / sw);
    if dst.len() < w * h * 4 { dst.resize(w * h * 4, 0); }
    let xs: Vec<usize> = (0..=w).map(|x| x * sw / w).collect();
    let max_n = (sw / w + 1) * (sh / h + 1);
    let inv: Vec<u64> = (0..=max_n).map(|n| if n == 0 { 0 } else { ((1u64 << 24) + n as u64 / 2) / n as u64 }).collect();
    let mut br = vec![0u64; w];
    let mut gg = vec![0u64; w];
    for y in 0..h {
        let (y0, y1) = (y * sh / h, (y + 1) * sh / h);
        br.fill(0);
        gg.fill(0);
        for yy in y0..y1 {
            let row = &src[yy * sw * 4..(yy + 1) * sw * 4];
            for x in 0..w {
                let (mut a, mut b) = (0u64, 0u64);
                for px in row[xs[x] * 4..xs[x + 1] * 4].chunks_exact(4) {
                    let v = u32::from_le_bytes([px[0], px[1], px[2], px[3]]);
                    a += (v & 0xff) as u64 | ((v & 0xff0000) as u64) << 16;
                    b += ((v >> 8) & 0xff) as u64;
                }
                br[x] += a;
                gg[x] += b;
            }
        }
        let out = &mut dst[y * w * 4..(y + 1) * w * 4];
        for x in 0..w {
            let r = inv[(xs[x + 1] - xs[x]) * (y1 - y0)];
            let o = &mut out[x * 4..x * 4 + 4];
            o[0] = std::cmp::min(255, ((br[x] & 0xffffffff) * r + (1 << 23)) >> 24) as u8;
            o[1] = std::cmp::min(255, (gg[x] * r + (1 << 23)) >> 24) as u8;
            o[2] = std::cmp::min(255, ((br[x] >> 32) * r + (1 << 23)) >> 24) as u8;
            o[3] = 0xff;
        }
    }
    h
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn shrink_averages_spans() {
        // 4×2 white and black columns to 2 wide: each output pixel averages a white and a black one.
        let mut src = Vec::new();
        for _ in 0..2 { for x in 0..4 { let v = if x % 2 == 0 { 255 } else { 0 }; src.extend_from_slice(&[v, v, v, 0]); } }
        let mut dst = Vec::new();
        let h = shrink_bgrx(&mut dst, &src, 4, 2, 2);
        assert_eq!(h, 1);
        assert_eq!(&dst[..8], &[128, 128, 128, 255, 128, 128, 128, 255]);
    }
}
