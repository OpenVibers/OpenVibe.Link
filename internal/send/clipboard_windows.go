//go:build windows

package send

func SystemClipboard() Clipboard {
	return &commandClipboard{
		read:  [][]string{{"powershell", "-NoProfile", "-NonInteractive", "-Command", "[Console]::Out.Write((Get-Clipboard -Raw))"}},
		write: [][]string{{"powershell", "-NoProfile", "-NonInteractive", "-Command", "[Console]::In.ReadToEnd() | Set-Clipboard"}},
		tool:  "PowerShell's clipboard",
	}
}
