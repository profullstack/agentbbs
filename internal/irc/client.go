// Package irc is a minimal, in-process IRC client for the `irc@` route: it
// connects a member to the BBS's own (members-only) Ergo network and drives a
// Bubble Tea TUI (see tui.go). It runs inside the agentbbs process on the host,
// so it reaches Ergo's loopback listener directly and — unlike running a
// third-party client like irssi in a pod — offers no /exec shell escape.
//
// The member is already authenticated to the BBS by their SSH key; we present
// their account name to Ergo over SASL PLAIN. Ergo's auth-script approves the
// login on membership alone (the passphrase is ignored, by design), so we send
// a placeholder.
package irc

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"time"
)

// DefaultAddr is the host-local plaintext Ergo listener (loopback only; the
// public front door is 6697/TLS). Overridable for dev hosts.
const DefaultAddr = "127.0.0.1:6667"

// Event is one thing that happened on the connection, already formatted for
// display. Kind groups them so the TUI can colorize.
type Event struct {
	Kind EventKind
	// Channel is the conversation the event belongs to ("" = server/status).
	Channel string
	Nick    string
	Text    string
}

type EventKind int

const (
	EvMessage EventKind = iota // a PRIVMSG to a channel or to us
	EvNotice                   // a NOTICE (often from services)
	EvJoin
	EvPart
	EvQuit
	EvNick
	EvSystem // numerics, topics, names, errors, our own status lines
	EvClosed // the connection ended; Text carries the reason
)

// Client is a single member's connection to the network.
type Client struct {
	conn   net.Conn
	w      *bufio.Writer
	r      *bufio.Reader
	nick   string
	events chan Event
}

// Dial connects to addr, performs the SASL PLAIN handshake as nick, and blocks
// until the server welcomes us (001) or the attempt fails. On success the read
// loop is running and Events() is live.
func Dial(ctx context.Context, addr, nick string) (*Client, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("could not reach the IRC server (%s): %w", addr, err)
	}
	c := &Client{
		conn:   conn,
		w:      bufio.NewWriter(conn),
		r:      bufio.NewReader(conn),
		nick:   nick,
		events: make(chan Event, 256),
	}
	if err := c.handshake(nick); err != nil {
		_ = conn.Close()
		return nil, err
	}
	go c.readLoop()
	return c, nil
}

// Events is the stream the TUI consumes. It is closed when the connection ends.
func (c *Client) Events() <-chan Event { return c.events }

// Nick reports the connection's current nickname.
func (c *Client) Nick() string { return c.nick }

func (c *Client) send(format string, a ...any) error {
	if _, err := fmt.Fprintf(c.w, format+"\r\n", a...); err != nil {
		return err
	}
	return c.w.Flush()
}

// handshake authenticates with SASL PLAIN and waits for welcome (001) or an
// auth failure (904/906) / error. The membership gate lives server-side; the
// passphrase is a placeholder the auth-script ignores.
func (c *Client) handshake(nick string) error {
	_ = c.conn.SetDeadline(time.Now().Add(20 * time.Second))
	if err := c.send("CAP LS 302"); err != nil {
		return err
	}
	if err := c.send("NICK %s", nick); err != nil {
		return err
	}
	if err := c.send("USER %s 0 * :%s", nick, nick); err != nil {
		return err
	}
	if err := c.send("CAP REQ :sasl"); err != nil {
		return err
	}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("connection closed during login: %w", err)
		}
		msg := parse(line)
		switch msg.command {
		case "PING":
			_ = c.send("PONG :%s", msg.trailing())
		case "CAP":
			// params: <*> ACK :sasl  → start PLAIN exchange
			if len(msg.params) >= 2 && msg.params[1] == "ACK" {
				_ = c.send("AUTHENTICATE PLAIN")
			}
		case "AUTHENTICATE":
			if msg.first() == "+" {
				tok := base64.StdEncoding.EncodeToString([]byte("\x00" + nick + "\x00x"))
				_ = c.send("AUTHENTICATE %s", tok)
			}
		case "903": // SASL success → close capability negotiation so we get 001
			_ = c.send("CAP END")
		case "900": // RPL_LOGGEDIN (informational; 903 drives CAP END)
		case "904", "905", "906": // SASL failed/aborted
			return fmt.Errorf("the network is members-only and rejected %q — register first: ssh join@", nick)
		case "433": // nick in use
			return fmt.Errorf("nickname %q is already in use on the network", nick)
		case "ERROR":
			return fmt.Errorf("server refused the connection: %s", msg.trailing())
		case "001": // welcome — we're in
			_ = c.conn.SetDeadline(time.Time{}) // clear; read loop manages liveness
			return nil
		}
	}
}

// readLoop turns inbound IRC into Events until the connection drops.
func (c *Client) readLoop() {
	defer close(c.events)
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			c.emit(Event{Kind: EvClosed, Text: "disconnected"})
			return
		}
		msg := parse(line)
		switch msg.command {
		case "PING":
			_ = c.send("PONG :%s", msg.trailing())
		case "PRIVMSG":
			c.emit(Event{Kind: EvMessage, Channel: msg.first(), Nick: msg.nick(), Text: msg.trailing()})
		case "NOTICE":
			c.emit(Event{Kind: EvNotice, Channel: msg.first(), Nick: msg.nick(), Text: msg.trailing()})
		case "JOIN":
			c.emit(Event{Kind: EvJoin, Channel: msg.first(), Nick: msg.nick()})
		case "PART":
			c.emit(Event{Kind: EvPart, Channel: msg.first(), Nick: msg.nick(), Text: msg.trailing()})
		case "QUIT":
			c.emit(Event{Kind: EvQuit, Nick: msg.nick(), Text: msg.trailing()})
		case "NICK":
			n := msg.nick()
			if n == c.nick {
				c.nick = msg.trailing()
			}
			c.emit(Event{Kind: EvNick, Nick: n, Text: msg.trailing()})
		case "332": // RPL_TOPIC
			c.emit(Event{Kind: EvSystem, Channel: msg.nth(1), Text: "topic: " + msg.trailing()})
		case "353": // RPL_NAMREPLY
			c.emit(Event{Kind: EvSystem, Channel: msg.nth(2), Text: "names: " + msg.trailing()})
		case "ERROR":
			c.emit(Event{Kind: EvClosed, Text: msg.trailing()})
			return
		default:
			// Surface numeric replies (server info, MOTD, errors) as status.
			if len(msg.command) == 3 && msg.command[0] >= '0' && msg.command[0] <= '9' {
				c.emit(Event{Kind: EvSystem, Text: msg.trailing()})
			}
		}
	}
}

func (c *Client) emit(e Event) {
	select {
	case c.events <- e:
	default: // drop if the TUI is far behind rather than block the read loop
	}
}

// Privmsg sends a message to a channel or nick.
func (c *Client) Privmsg(target, text string) error { return c.send("PRIVMSG %s :%s", target, text) }

// Join joins a channel.
func (c *Client) Join(ch string) error { return c.send("JOIN %s", ch) }

// Part leaves a channel.
func (c *Client) Part(ch string) error { return c.send("PART %s", ch) }

// Raw sends a pre-formed IRC command (for /-commands the TUI doesn't model).
func (c *Client) Raw(line string) error { return c.send("%s", line) }

// Close quits cleanly and tears down the socket.
func (c *Client) Close() error {
	_ = c.send("QUIT :leaving")
	return c.conn.Close()
}

// --- minimal message parsing ------------------------------------------------

type message struct {
	prefix   string
	command  string
	params   []string // includes the trailing param as the last element
	hasTrail bool
}

func parse(line string) message {
	line = strings.TrimRight(line, "\r\n")
	var m message
	if strings.HasPrefix(line, "@") { // strip IRCv3 tags; we don't use them here
		if i := strings.IndexByte(line, ' '); i >= 0 {
			line = line[i+1:]
		}
	}
	if strings.HasPrefix(line, ":") {
		i := strings.IndexByte(line, ' ')
		if i < 0 {
			return m
		}
		m.prefix = line[1:i]
		line = line[i+1:]
	}
	// trailing param
	if i := strings.Index(line, " :"); i >= 0 {
		trail := line[i+2:]
		line = line[:i]
		m.command, m.params = splitCmd(line)
		m.params = append(m.params, trail)
		m.hasTrail = true
		return m
	}
	m.command, m.params = splitCmd(line)
	return m
}

func splitCmd(s string) (string, []string) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return "", nil
	}
	return strings.ToUpper(f[0]), f[1:]
}

// nick returns the nick from the prefix (nick!user@host).
func (m message) nick() string {
	if i := strings.IndexByte(m.prefix, '!'); i >= 0 {
		return m.prefix[:i]
	}
	return m.prefix
}

// first returns the first param (often the target/channel), else "".
func (m message) first() string { return m.nth(0) }

func (m message) nth(i int) string {
	if i >= 0 && i < len(m.params) {
		return m.params[i]
	}
	return ""
}

// trailing returns the trailing (message) param, else the last param.
func (m message) trailing() string {
	if len(m.params) == 0 {
		return ""
	}
	return m.params[len(m.params)-1]
}
