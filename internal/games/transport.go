package games

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// JSONLineConn adapts a byte stream (an SSH session, a WebSocket bridged to
// io, etc.) into a PlayerIO speaking line-delimited JSON. A background reader
// turns the blocking stream into a channel so ReadMove can honor deadlines.
type JSONLineConn struct {
	name  string
	wmu   sync.Mutex
	w     io.Writer
	lines chan []byte
}

// NewJSONLineConn starts reading lines from r in the background. name is the
// player's account name (used for logging and the ladder).
func NewJSONLineConn(name string, r io.Reader, w io.Writer) *JSONLineConn {
	c := &JSONLineConn{name: name, w: w, lines: make(chan []byte, 4)}
	go c.readLoop(r)
	return c
}

func (c *JSONLineConn) readLoop(r io.Reader) {
	defer close(c.lines)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // up to 1 MiB per line
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		c.lines <- line
	}
}

func (c *JSONLineConn) Name() string { return c.name }

func (c *JSONLineConn) Send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

type inbound struct {
	Type string `json:"type"`
	Move string `json:"move"`
	Game string `json:"game"`
}

// ReadJoin reads the opening handshake and returns the requested game id.
func (c *JSONLineConn) ReadJoin(deadline time.Time) (string, error) {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				return "", ErrClosed
			}
			var m inbound
			if json.Unmarshal(line, &m) == nil && m.Type == "join" && m.Game != "" {
				return m.Game, nil
			}
			// ignore noise until a valid join arrives or we time out
		case <-timer.C:
			return "", ErrTimeout
		}
	}
}

// ReadMove blocks for the next move message until deadline. Non-move messages
// (pings, stray joins) are ignored.
func (c *JSONLineConn) ReadMove(deadline time.Time) (string, error) {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				return "", ErrClosed
			}
			var m inbound
			if json.Unmarshal(line, &m) == nil && m.Type == "move" && m.Move != "" {
				return m.Move, nil
			}
		case <-timer.C:
			return "", ErrTimeout
		}
	}
}
