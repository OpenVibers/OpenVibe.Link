//go:build linux

package send

// SystemClipboard prefers Wayland when the session is Wayland, and still falls back to X11 tools when it is not
// installed (a Wayland session can run X11 applications, but a missing wl-clipboard is common).
func SystemClipboard() Clipboard {
	return &commandClipboard{
		read: [][]string{
			{"wl-paste", "--no-newline"},
			{"xclip", "-selection", "clipboard", "-o"},
			{"xsel", "--clipboard", "--output"},
		},
		write: [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard", "-i"},
			{"xsel", "--clipboard", "--input"},
		},
		tool: "wl-clipboard or xclip",
	}
}
