//! Key names to X keysyms: X's own names and the computer plugin's short ones (Link's internal/deskdisplay/keys.go).

/// The keysym a key name or a single printable character means.
pub fn keysym_of(name: &str) -> Option<u32> {
    let b = name.as_bytes();
    if b.len() == 1 && (0x20..0x7f).contains(&b[0]) {
        return Some(b[0] as u32);
    }
    let lower = name.to_ascii_lowercase();
    if let Some(n) = lower.strip_prefix('f') {
        if let Ok(i) = n.parse::<u32>() {
            if (1..=12).contains(&i) {
                return Some(0xffbe + i - 1);
            }
        }
    }
    Some(match lower.as_str() {
        "return" | "enter" => 0xff0d,
        "kp_enter" => 0xff8d,
        "tab" => 0xff09,
        "backspace" => 0xff08,
        "escape" | "esc" => 0xff1b,
        "delete" => 0xffff,
        "insert" => 0xff63,
        "home" => 0xff50,
        "end" => 0xff57,
        "left" => 0xff51,
        "up" => 0xff52,
        "right" => 0xff53,
        "down" => 0xff54,
        "page_up" | "prior" => 0xff55,
        "page_down" | "next" => 0xff56,
        "space" => 0x20,
        "menu" => 0xff67,
        "print" => 0xff61,
        "caps_lock" => 0xffe5,
        "shift" | "shift_l" => 0xffe1,
        "shift_r" => 0xffe2,
        "ctrl" | "control" | "control_l" => 0xffe3,
        "control_r" => 0xffe4,
        "alt" | "alt_l" => 0xffe9,
        "alt_r" => 0xffea,
        "meta" | "super" | "super_l" | "cmd" | "win" => 0xffeb,
        "super_r" => 0xffec,
        "minus" => 0x2d,
        "equal" => 0x3d,
        "plus" => 0x2b,
        "comma" => 0x2c,
        "period" => 0x2e,
        "slash" => 0x2f,
        "backslash" => 0x5c,
        "semicolon" => 0x3b,
        "apostrophe" => 0x27,
        "grave" => 0x60,
        "bracketleft" => 0x5b,
        "bracketright" => 0x5d,
        _ => return None,
    })
}

/// "ctrl+shift+t" as its keysyms in order; None when any part is unknown. "ctrl++" is ctrl and the plus key.
pub fn chord(spec: &str) -> Option<Vec<u32>> {
    let (spec, plus) = match spec.strip_suffix("++") {
        Some(s) => (s, true),
        None => (spec, false),
    };
    let mut parts: Vec<&str> = spec.split('+').collect();
    if plus {
        parts.push("+");
    }
    if parts.is_empty() || parts.len() > 5 {
        return None;
    }
    parts.iter().map(|p| if p.is_empty() { None } else { keysym_of(p) }).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn chords_like_link() {
        assert_eq!(chord("ctrl+s"), Some(vec![0xffe3, 0x73]));
        assert_eq!(chord("Return"), Some(vec![0xff0d]));
        assert_eq!(chord("ctrl++"), Some(vec![0xffe3, 0x2b]));
        assert_eq!(chord("F5"), Some(vec![0xffc2]));
        assert_eq!(chord("ctrl+nope"), None);
        assert_eq!(chord("a+b+c+d+e+f"), None);
    }
}
