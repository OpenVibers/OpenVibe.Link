package safety

import (
	"fmt"
	"strings"
	"unicode"
)

// DisplayText makes untrusted approval text literal on terminals and desktops.
// Format characters include bidi overrides, isolates and invisible separators.
func DisplayText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			if r <= 0xffff {
				fmt.Fprintf(&b, "\\u%04x", r)
			} else {
				fmt.Fprintf(&b, "\\U%08x", r)
			}
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// QuoteHint quotes a displayed identifier as one POSIX shell argument.
func QuoteHint(s string) string {
	return "'" + strings.ReplaceAll(DisplayText(s), "'", "'\"'\"'") + "'"
}
