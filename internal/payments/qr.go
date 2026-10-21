package payments

import (
	"strings"

	"rsc.io/qr"
)

// TerminalQR renders text as a QR code made of Unicode half blocks, two module
// rows per text line, with a quiet zone. Light modules are drawn as blocks so it
// scans as dark-on-light on the usual light-on-dark terminal. Each line is
// prefixed with indent. Returns "" if the text cannot be encoded.
func TerminalQR(text, indent string) string {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return ""
	}
	const quiet = 2
	n := code.Size
	light := func(x, y int) bool {
		if x < 0 || y < 0 || x >= n || y >= n {
			return true // quiet zone
		}
		return !code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		b.WriteString(indent)
		for x := -quiet; x < n+quiet; x++ {
			top, bot := light(x, y), light(x, y+1)
			switch {
			case top && bot:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bot:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
