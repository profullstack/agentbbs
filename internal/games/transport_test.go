package games

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONLineConnReadJoinAndMove(t *testing.T) {
	pr, pw := io.Pipe()
	var out bytes.Buffer
	var mu sync.Mutex
	c := NewJSONLineConn("p", pr, writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return out.Write(b)
	}))

	go func() {
		_, _ = io.WriteString(pw, `{"type":"join","game":"ttt"}`+"\n")
		_, _ = io.WriteString(pw, `{"type":"ping"}`+"\n") // ignored noise
		_, _ = io.WriteString(pw, `{"type":"move","move":"4"}`+"\n")
	}()

	g, err := c.ReadJoin(time.Now().Add(time.Second))
	if err != nil || g != "ttt" {
		t.Fatalf("ReadJoin = %q, %v", g, err)
	}
	mv, err := c.ReadMove(time.Now().Add(time.Second))
	if err != nil || mv != "4" {
		t.Fatalf("ReadMove = %q, %v", mv, err)
	}

	if err := c.Send(map[string]string{"type": "ok"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	mu.Lock()
	got := out.String()
	mu.Unlock()
	if !strings.HasSuffix(got, "\n") || !strings.Contains(got, `"type":"ok"`) {
		t.Fatalf("Send wrote %q", got)
	}

	_ = pw.Close()
	if _, err := c.ReadMove(time.Now().Add(time.Second)); err != ErrClosed {
		t.Fatalf("closed stream should give ErrClosed, got %v", err)
	}
}

func TestJSONLineConnTimeout(t *testing.T) {
	pr, _ := io.Pipe()
	c := NewJSONLineConn("p", pr, io.Discard)
	if _, err := c.ReadMove(time.Now().Add(20 * time.Millisecond)); err != ErrTimeout {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }
