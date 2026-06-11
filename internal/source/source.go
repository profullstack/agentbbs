// Package source turns a user-supplied URL (YouTube Live or direct HLS) into
// a stream of packed RGB24 frames, sized for the half-block terminal renderer
// in internal/ascii. It is the URL counterpart to internal/calls, which
// ingests LiveKit tracks: same RGB24 frame contract, different front end.
//
// A YouTube URL is resolved to a playable stream with `yt-dlp -g`; a direct
// .m3u8 is used as-is. The resolved URL is then decoded by ffmpeg into raw
// rgb24 frames. Every URL — the one the user typed and the one yt-dlp returns —
// is run through an SSRF guard (guardURL) before any connection is made.
package source

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// Kind classifies a source URL.
type Kind string

const (
	KindYouTube Kind = "youtube"
	KindHLS     Kind = "hls"
)

func (k Kind) String() string { return string(k) }

// Options configures a worker. PW/PH are the pixel dimensions the decoder
// scales to — compute them with ascii.FitEven(cols, rows).
type Options struct {
	URL      string
	FPS      int
	PW, PH   int
	AllowHLS bool // permit direct .m3u8 / http(s) inputs (not just YouTube)
}

// Worker is one running decode pipeline: ffmpeg reading the resolved stream
// and emitting RGB24 frames on Frames. Mirrors internal/calls.session so the
// Phase 1 fan-out multiplexer can drive either source identically.
type Worker struct {
	Frames chan []byte // each frame is PW*PH*3 bytes
	Status chan string // human-readable status, best-effort (non-blocking)
	Kind   Kind        // resolved source kind
	ffmpeg *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
}

// youtubeHosts are the hostnames routed through yt-dlp resolution.
var youtubeHosts = map[string]bool{
	"youtube.com":       true,
	"www.youtube.com":   true,
	"m.youtube.com":     true,
	"music.youtube.com": true,
	"youtu.be":          true,
}

// Classify decides how a raw URL is handled, and rejects anything that is not
// http(s). It does not touch the network.
func Classify(raw string) (Kind, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported URL scheme %q (only http/https are allowed)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if youtubeHosts[host] {
		return KindYouTube, nil
	}
	if strings.Contains(strings.ToLower(u.Path), ".m3u8") {
		return KindHLS, nil
	}
	// Default unknown http(s) sources to HLS handling; the caller decides
	// whether AllowHLS permits them.
	return KindHLS, nil
}

// isBlockedIP reports whether an address must not be dialed: loopback,
// link-local (incl. the 169.254.169.254 cloud-metadata endpoint), private
// (RFC1918 / fc00::/7), multicast, or unspecified.
func isBlockedIP(ip net.IP) bool {
	return ip == nil ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		ip.IsPrivate()
}

// guardURL validates scheme and resolves the host, rejecting any URL that
// points at a private, loopback, link-local, or metadata address. It is the
// SSRF gate for both the user URL and the yt-dlp-resolved URL.
//
// Note: ffmpeg follows HTTP redirects internally, so a public host that 302s
// to a private one is not caught here. Re-checking post-redirect is a Phase 1
// item (see docs/ascii-live.md §3); for the local CLI the pre-DNS guard holds.
func guardURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("refusing %q: only http/https are allowed", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("refusing URL with no host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("cannot resolve host %q: %w", host, err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("refusing to connect to %s (%s): private or reserved address", host, ip)
		}
	}
	return nil
}

// resolve guards the input URL, runs yt-dlp for YouTube sources, and guards the
// resolved URL too. It returns the playable stream URL and the source kind.
func resolve(ctx context.Context, opts Options) (streamURL string, kind Kind, err error) {
	kind, err = Classify(opts.URL)
	if err != nil {
		return "", "", err
	}
	if kind == KindHLS && !opts.AllowHLS {
		return "", "", fmt.Errorf("direct HLS/URL input is disabled on this host")
	}
	if err := guardURL(opts.URL); err != nil {
		return "", "", err
	}
	if kind != KindYouTube {
		return opts.URL, kind, nil
	}

	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return "", "", fmt.Errorf("yt-dlp is not installed — required to resolve YouTube URLs")
	}
	// -g prints the direct media URL(s); the format preference keeps the
	// terminal-resolution stream small.
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"-f", "best[height<=480]/best", "-g", "--no-warnings", opts.URL)
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("could not resolve YouTube stream")
	}
	for _, line := range strings.Split(string(out), "\n") {
		streamURL = strings.TrimSpace(line)
		if streamURL != "" {
			break // first URL is the (combined) video stream
		}
	}
	if streamURL == "" {
		return "", "", fmt.Errorf("could not resolve YouTube stream")
	}
	if err := guardURL(streamURL); err != nil {
		return "", "", err
	}
	return streamURL, kind, nil
}

// Start resolves the source and launches the ffmpeg decode pipeline. The
// returned Worker emits frames until the stream ends or Close is called.
func Start(ctx context.Context, opts Options) (*Worker, error) {
	if opts.FPS <= 0 {
		opts.FPS = 10
	}
	if opts.PW <= 0 || opts.PH <= 0 {
		return nil, fmt.Errorf("invalid frame geometry %dx%d", opts.PW, opts.PH)
	}
	streamURL, kind, err := resolve(ctx, opts)
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithCancel(ctx)
	w := &Worker{
		Frames: make(chan []byte, 2),
		Status: make(chan string, 8),
		Kind:   kind,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	// ffmpeg: resolved stream → fps-limited, scaled, rgb24 raw frames.
	// format=rgb24 is mandatory — a yuv420p stream would otherwise fail the
	// rawvideo muxer.
	cmd := exec.CommandContext(cctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", streamURL,
		"-vf", fmt.Sprintf("fps=%d,scale=%d:%d:flags=lanczos,format=rgb24", opts.FPS, opts.PW, opts.PH),
		"-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1",
	)
	cmd.Stderr = os.Stderr // ffmpeg decode errors surface in the host log
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	w.ffmpeg = cmd
	w.status(fmt.Sprintf("decoding %s source…", kind))

	go w.pump(out, opts.PW*opts.PH*3)
	return w, nil
}

// pump reads fixed-size frames from ffmpeg and forwards them, dropping rather
// than blocking when a viewer lags.
func (w *Worker) pump(out io.Reader, size int) {
	defer close(w.Frames)
	r := bufio.NewReaderSize(out, size)
	for {
		buf := make([]byte, size)
		if _, err := io.ReadFull(r, buf); err != nil {
			w.status("stream ended")
			return
		}
		select {
		case w.Frames <- buf:
		case <-w.done:
			return
		default: // drop frame; keep latency low
		}
	}
}

func (w *Worker) status(msg string) {
	select {
	case w.Status <- msg:
	default:
	}
}

// Close tears down ffmpeg and stops the pump.
func (w *Worker) Close() {
	select {
	case <-w.done:
	default:
		close(w.done)
	}
	if w.cancel != nil {
		w.cancel()
	}
	if w.ffmpeg != nil && w.ffmpeg.Process != nil {
		_ = w.ffmpeg.Process.Kill()
		_ = w.ffmpeg.Wait()
	}
}
