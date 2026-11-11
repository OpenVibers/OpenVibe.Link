//go:build !linux && !darwin && !windows

package send

// SystemClipboard is absent on platforms without a clipboard Link can reach. Clipboard sync stays off there and
// the window says so instead of pretending.
func SystemClipboard() Clipboard { return nil }
