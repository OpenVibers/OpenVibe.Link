//go:build darwin

package send

func SystemClipboard() Clipboard {
	return &commandClipboard{
		read:  [][]string{{"pbpaste"}},
		write: [][]string{{"pbcopy"}},
		tool:  "pbpaste and pbcopy",
	}
}
