// Command lkpublish is a dev tool: it publishes a VP8 IVF file into a
// LiveKit room so the video-<code>@ SSH route has something to render.
//
//	ffmpeg -f lavfi -i testsrc=size=320x240:rate=15 -t 60 -c:v libvpx test.ivf
//	go run ./cmd/lkpublish -room test123 -file test.ivf
package main

import (
	"flag"
	"os"
	"os/signal"
	"time"

	"github.com/charmbracelet/log"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	room := flag.String("room", "test123", "room/call code to publish into")
	file := flag.String("file", "test.ivf", "VP8 IVF file to stream")
	fps := flag.Int("fps", 15, "frame rate to pace the file at")
	flag.Parse()

	url := env("LIVEKIT_URL", "ws://localhost:7880")
	key := env("LIVEKIT_API_KEY", "devkey")
	secret := env("LIVEKIT_API_SECRET", "secret")

	r, err := lksdk.ConnectToRoom(url, lksdk.ConnectInfo{
		APIKey:              key,
		APISecret:           secret,
		RoomName:            *room,
		ParticipantIdentity: "lkpublish",
	}, &lksdk.RoomCallback{})
	if err != nil {
		log.Fatal("connect", "err", err)
	}
	defer r.Disconnect()

	// Explicit frame duration: don't trust IVF timebase interpretation.
	track, err := lksdk.NewLocalFileTrack(*file,
		lksdk.ReaderTrackWithFrameDuration(time.Second/time.Duration(*fps)))
	if err != nil {
		log.Fatal("track", "err", err)
	}
	// Width/height/source matter: without dimensions the SFU's dynacast has
	// no layer to subscribe to and pauses the track entirely.
	if _, err := r.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:        "test-pattern",
		Source:      livekit.TrackSource_CAMERA,
		VideoWidth:  320,
		VideoHeight: 240,
	}); err != nil {
		log.Fatal("publish", "err", err)
	}
	log.Info("publishing", "room", *room, "file", *file)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}
