// Command ascii-live previews a live video source as truecolor ASCII in the
// local terminal — the Phase 0 deliverable of docs/ascii-live.md. It shares
// the source adapter (internal/source) and renderer (internal/ascii) with the
// AgentBBS SSH route, so what you see here is what tv-<slug>@ will serve.
//
// Usage:
//
//	ascii-live watch <url> [--fps N] [--width COLS]
//
//	ascii-live watch "https://youtube.com/live/<id>"
//	ascii-live watch "https://example.com/stream.m3u8"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/profullstack/agentbbs/internal/ascii"
	"github.com/profullstack/agentbbs/internal/source"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "watch" {
		usage()
		os.Exit(2)
	}

	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	fps := fs.Int("fps", 10, "frames per second")
	width := fs.Int("width", 120, "max width in terminal columns")
	_ = fs.Parse(os.Args[2:])

	if fs.NArg() < 1 {
		usage()
		os.Exit(2)
	}
	url := fs.Arg(0)

	if err := run(url, *fps, *width); err != nil {
		fmt.Fprintln(os.Stderr, "ascii-live: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ascii-live watch <url> [--fps N] [--width COLS]")
}

func run(url string, fps, maxWidth int) error {
	// Lock geometry at start (the decoder output width is fixed for the run,
	// matching how internal/calls locks pw at join time).
	cols, rows := terminalSize()
	if maxWidth > 0 && maxWidth < cols {
		cols = maxWidth
	}
	pw, ph := ascii.FitEven(cols, rows)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	w, err := source.Start(ctx, source.Options{
		URL: url, FPS: fps, PW: pw, PH: ph, AllowHLS: true,
	})
	if err != nil {
		return err
	}
	defer w.Close()

	// Alt screen + hidden cursor; always restored on exit.
	fmt.Print("\x1b[?1049h\x1b[?25l")
	defer fmt.Print("\x1b[?25h\x1b[?1049l")

	status := fmt.Sprintf("starting %s…", w.Kind)
	for {
		select {
		case <-ctx.Done():
			return nil
		case s, ok := <-w.Status:
			if ok {
				status = s
			}
		case buf, ok := <-w.Frames:
			if !ok {
				return nil // stream ended
			}
			frameH := (len(buf) / 3) / pw
			// Home the cursor and repaint in place.
			fmt.Print("\x1b[H" + ascii.FrameRGB(buf, pw, frameH) +
				"\r\n\x1b[2K " + url + " · " + w.Kind.String() + " · " + status + " · ctrl-c to quit")
		}
	}
}

// terminalSize returns the stdout terminal dimensions, or a sane default when
// stdout is not a TTY (e.g. piped output).
func terminalSize() (cols, rows int) {
	if c, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil && c > 0 && r > 0 {
		return c, r
	}
	return 120, 40
}
