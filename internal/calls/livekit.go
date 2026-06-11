// LiveKit subscriber: joins a PairUX room, takes the first remote VP8 video
// track, remuxes RTP into IVF, and has ffmpeg decode + scale it into raw
// RGB24 frames sized for the terminal grid.
package calls

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"
)

// Config is the LiveKit deployment shared with PairUX.
type Config struct {
	URL, Key, Secret string
}

// ConfigFromEnv reads AGENTBBS_LIVEKIT_* with LIVEKIT_* fallbacks, matching
// PairUX's env shape (LIVEKIT_API_KEY / LIVEKIT_API_SECRET).
func ConfigFromEnv() Config {
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
		return ""
	}
	return Config{
		URL:    pick("AGENTBBS_LIVEKIT_URL", "LIVEKIT_URL", "NEXT_PUBLIC_LIVEKIT_URL"),
		Key:    pick("AGENTBBS_LIVEKIT_KEY", "LIVEKIT_API_KEY"),
		Secret: pick("AGENTBBS_LIVEKIT_SECRET", "LIVEKIT_API_SECRET"),
	}
}

func (c Config) ok() bool { return c.URL != "" && c.Key != "" && c.Secret != "" }

// session is one live subscription: frames arrive on Frames sized pw*ph*3.
type session struct {
	Frames chan []byte
	Status chan string
	room   *lksdk.Room
	ffmpeg *exec.Cmd
	ivf    io.WriteCloser
	done   chan struct{}
}

// join connects to room `code` as a hidden-ish subscriber and starts the
// decode pipeline targeting pw x ph pixels.
func join(cfg Config, code, identity string, pw, ph int) (*session, error) {
	if !cfg.ok() {
		return nil, fmt.Errorf("video calls are not configured on this host (LIVEKIT_URL/API_KEY/API_SECRET)")
	}

	s := &session{
		Frames: make(chan []byte, 2),
		Status: make(chan string, 8),
		done:   make(chan struct{}),
	}

	// ffmpeg: IVF (VP8) on stdin → raw RGB24 frames on stdout.
	ffin, ivfW := io.Pipe()
	s.ivf = ivfW
	if dump := os.Getenv("AGENTBBS_VIDEO_DEBUG"); dump != "" {
		if f, err := os.Create(dump); err == nil {
			s.ivf = teeWriteCloser{io.MultiWriter(ivfW, f), ivfW, f}
		}
	}
	cmd := exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-probesize", "32", "-analyzeduration", "0",
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-f", "ivf", "-i", "pipe:0",
		"-vf", fmt.Sprintf("scale=%d:%d", pw, ph),
		"-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1",
	)
	cmd.Stdin = ffin
	cmd.Stderr = os.Stderr // surfaces decode errors in the server log
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	s.ffmpeg = cmd

	go func() { // frame pump: drop frames rather than lag
		size := pw * ph * 3
		for {
			buf := make([]byte, size)
			if _, err := io.ReadFull(out, buf); err != nil {
				close(s.Frames)
				return
			}
			select {
			case s.Frames <- buf:
			default:
			}
		}
	}()

	gotTrack := false
	cb := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if gotTrack || track.Kind() != webrtc.RTPCodecTypeVideo {
					return
				}
				if track.Codec().MimeType != webrtc.MimeTypeVP8 {
					s.status("skipping non-VP8 track from " + rp.Identity())
					return
				}
				gotTrack = true
				s.status("video from " + rp.Identity())
				// PLI is what makes the SFU start forwarding from a
				// keyframe; without it a fresh subscriber starves.
				rp.WritePLI(track.SSRC())
				go s.keyframeTicker(rp, track.SSRC())
				go s.consume(track)
			},
		},
		OnDisconnected: func() { s.status("disconnected") },
	}

	room, err := lksdk.ConnectToRoom(cfg.URL, lksdk.ConnectInfo{
		APIKey:              cfg.Key,
		APISecret:           cfg.Secret,
		RoomName:            code,
		ParticipantIdentity: identity,
	}, cb)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("livekit: %w", err)
	}
	s.room = room
	s.status("joined " + code + " — waiting for video…")
	return s, nil
}

// keyframeTicker re-requests keyframes so late joins and packet loss recover
// quickly; a periodic full refresh is cheap at terminal resolutions.
func (s *session) keyframeTicker(rp *lksdk.RemoteParticipant, ssrc webrtc.SSRC) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		select {
		case <-s.done:
			return
		default:
		}
		rp.WritePLI(ssrc)
	}
}

// consume remuxes the track's RTP into the IVF pipe until it ends.
func (s *session) consume(track *webrtc.TrackRemote) {
	w, err := ivfwriter.NewWith(s.ivf)
	if err != nil {
		s.status("ivf: " + err.Error())
		return
	}
	n := 0
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			s.status(fmt.Sprintf("track ended after %d packets: %v", n, err))
			_ = w.Close()
			return
		}
		if err := w.WriteRTP(pkt); err != nil {
			s.status("ivf write: " + err.Error())
			return
		}
		n++
		if n == 1 || n%500 == 0 {
			s.status(fmt.Sprintf("receiving (%d rtp packets)", n))
		}
	}
}

func (s *session) status(msg string) {
	select {
	case s.Status <- msg:
	default:
	}
}

// teeWriteCloser mirrors the IVF stream to a debug file (AGENTBBS_VIDEO_DEBUG).
type teeWriteCloser struct {
	io.Writer
	pipe io.WriteCloser
	file io.Closer
}

func (t teeWriteCloser) Close() error {
	_ = t.file.Close()
	return t.pipe.Close()
}

// Close tears the whole pipeline down.
func (s *session) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	if s.room != nil {
		s.room.Disconnect()
	}
	if s.ivf != nil {
		_ = s.ivf.Close()
	}
	if s.ffmpeg != nil && s.ffmpeg.Process != nil {
		_ = s.ffmpeg.Process.Kill()
		_ = s.ffmpeg.Wait()
	}
}
