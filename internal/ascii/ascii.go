// Package ascii converts raw video frames into truecolor terminal art.
//
// Each character cell renders two vertical pixels using the upper-half block
// (▀): foreground colors the top pixel, background the bottom — the same
// technique doom-ascii uses. A WxH terminal therefore displays a Wx(2H)
// pixel image.
package ascii

import (
	"fmt"
	"strings"
)

// FrameRGB renders a packed RGB24 frame (w*h*3 bytes, as ffmpeg's rawvideo
// rgb24 emits) sized exactly for the target cell grid: w columns, h*2 rows
// of pixels. Rows are joined with \r\n so the output is PTY-safe.
func FrameRGB(buf []byte, w, h int) string {
	if len(buf) < w*h*3 || w <= 0 || h <= 0 {
		return ""
	}
	rows := h / 2
	var b strings.Builder
	b.Grow(rows * w * 40)
	for row := 0; row < rows; row++ {
		top := row * 2
		bot := top + 1
		for x := 0; x < w; x++ {
			ti := (top*w + x) * 3
			bi := (bot*w + x) * 3
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀",
				buf[ti], buf[ti+1], buf[ti+2],
				buf[bi], buf[bi+1], buf[bi+2])
		}
		b.WriteString("\x1b[0m")
		if row != rows-1 {
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

// FitEven clamps a terminal geometry to an even pixel height for the
// half-block renderer and returns pixel dimensions (pw, ph) for the decoder.
func FitEven(cols, rows int) (pw, ph int) {
	if cols < 8 {
		cols = 8
	}
	if rows < 4 {
		rows = 4
	}
	return cols, (rows - 1) * 2 // leave one status line
}
