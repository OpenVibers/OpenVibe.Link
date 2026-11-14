package deskdisplay

import "strings"

// keysyms maps the key names xdotool and the computer plugin use (X's keysym names, and the plugin's own short ones)
// to X keysyms. A printable ASCII character is its own keysym.
var keysyms = map[string]uint32{
	"return": 0xff0d, "enter": 0xff0d, "kp_enter": 0xff8d, "tab": 0xff09, "backspace": 0xff08, "escape": 0xff1b, "esc": 0xff1b,
	"delete": 0xffff, "insert": 0xff63, "home": 0xff50, "end": 0xff57, "left": 0xff51, "up": 0xff52, "right": 0xff53,
	"down": 0xff54, "page_up": 0xff55, "prior": 0xff55, "page_down": 0xff56, "next": 0xff56, "space": 0x20,
	"menu": 0xff67, "print": 0xff61, "caps_lock": 0xffe5,
	"shift": 0xffe1, "shift_l": 0xffe1, "shift_r": 0xffe2, "ctrl": 0xffe3, "control": 0xffe3, "control_l": 0xffe3,
	"control_r": 0xffe4, "alt": 0xffe9, "alt_l": 0xffe9, "alt_r": 0xffea, "meta": 0xffeb, "super": 0xffeb, "super_l": 0xffeb,
	"super_r": 0xffec, "cmd": 0xffeb, "win": 0xffeb,
	"minus": 0x2d, "equal": 0x3d, "plus": 0x2b, "comma": 0x2c, "period": 0x2e, "slash": 0x2f, "backslash": 0x5c,
	"semicolon": 0x3b, "apostrophe": 0x27, "grave": 0x60, "bracketleft": 0x5b, "bracketright": 0x5d,
}

func init() {
	for i := 1; i <= 12; i++ {
		keysyms["f"+itoa(i)] = 0xffbe + uint32(i-1)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// keysymOf is the keysym a key name or a single character means, and false when it means none known here.
func keysymOf(name string) (uint32, bool) {
	if len(name) == 1 && name[0] >= 0x20 && name[0] < 0x7f {
		return uint32(name[0]), true
	}
	ks, ok := keysyms[strings.ToLower(name)]
	return ks, ok
}

// chord splits "ctrl+shift+t" into its keysyms, modifiers first; false when any part is unknown.
func chord(spec string) ([]uint32, bool) {
	plus := strings.HasSuffix(spec, "++") // "ctrl++": ctrl and the plus key
	if plus {
		spec = strings.TrimSuffix(spec, "++")
	}
	parts := strings.Split(spec, "+")
	if plus {
		parts = append(parts, "+")
	}
	if len(parts) == 0 || len(parts) > 5 {
		return nil, false
	}
	out := make([]uint32, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, false
		}
		ks, ok := keysymOf(p)
		if !ok {
			return nil, false
		}
		out = append(out, ks)
	}
	return out, true
}
