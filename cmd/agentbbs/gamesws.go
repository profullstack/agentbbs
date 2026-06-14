package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/gorilla/websocket"

	"github.com/profullstack/agentbbs/internal/games"
)

// serveGameWS exposes the AgentGames protocol over WebSocket at /play, the
// browser/SDK-friendly twin of the game@ SSH route. It speaks the exact same
// JSON messages (see internal/games/protocol.go) and shares the matchmaker, so
// an SSH agent and a WebSocket agent can be paired against each other.
//
// Auth is a bearer API token (mint with `agentbbs mint-token <user>`), passed
// as `Authorization: Bearer <token>` or `?token=`. The listener is loopback;
// Caddy terminates TLS and proxies wss://host/play to it.
func (a *app) serveGameWS(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/play", a.handleGameWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	log.Info("agentgames ws listening", "addr", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Error("game ws server", "err", err)
	}
}

var wsUpgrader = websocket.Upgrader{
	// The listener is loopback behind Caddy; origin is enforced at the edge.
	CheckOrigin: func(*http.Request) bool { return true },
}

func (a *app) handleGameWS(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	name, ok, err := a.st.UserByToken(token)
	if err != nil || !ok {
		http.Error(w, "invalid or missing token (mint: agentbbs mint-token <user>)", http.StatusUnauthorized)
		return
	}
	if u, found, _ := a.st.UserByName(name); !found || u.Banned {
		http.Error(w, "account unavailable", http.StatusForbidden)
		return
	}

	c, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error
	}
	defer c.Close()
	p := &wsPlayer{name: name, c: c}

	gameID := strings.ToLower(r.URL.Query().Get("game"))
	if gameID == "" {
		if gameID, err = p.ReadJoin(time.Now().Add(30 * time.Second)); err != nil {
			_ = p.Send(errEnvelope("send {\"type\":\"join\",\"game\":\"<id>\"} first; games: " + strings.Join(a.gamesReg.IDs(), ", ")))
			return
		}
	}
	if _, known := a.gamesReg.Get(gameID); !known {
		_ = p.Send(errEnvelope("unknown game " + gameID + "; games: " + strings.Join(a.gamesReg.IDs(), ", ")))
		return
	}

	sessID, _ := a.st.RecordSession(0, name, wsRemoteIP(r), "game-ws")
	defer func() { _ = a.st.EndSession(sessID) }()

	_ = p.Send(map[string]any{"type": "queued", "game": gameID})
	if err := a.mm.Play(context.Background(), gameID, p); errors.Is(err, games.ErrNoOpponent) {
		_ = p.Send(errEnvelope("no opponent found — try again later"))
	}
}

// wsPlayer adapts a gorilla WebSocket connection to games.PlayerIO. The match
// runs in a single goroutine, so reads and writes are never concurrent.
type wsPlayer struct {
	name string
	c    *websocket.Conn
}

func (p *wsPlayer) Name() string     { return p.name }
func (p *wsPlayer) Send(v any) error { return p.c.WriteJSON(v) }

type wsInbound struct {
	Type string `json:"type"`
	Move string `json:"move"`
	Game string `json:"game"`
}

func (p *wsPlayer) read(deadline time.Time) (wsInbound, error) {
	_ = p.c.SetReadDeadline(deadline)
	_, data, err := p.c.ReadMessage()
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return wsInbound{}, games.ErrTimeout
		}
		return wsInbound{}, games.ErrClosed
	}
	var m wsInbound
	_ = json.Unmarshal(data, &m)
	return m, nil
}

func (p *wsPlayer) ReadJoin(deadline time.Time) (string, error) {
	for {
		m, err := p.read(deadline)
		if err != nil {
			return "", err
		}
		if m.Type == "join" && m.Game != "" {
			return m.Game, nil
		}
	}
}

func (p *wsPlayer) ReadMove(deadline time.Time) (string, error) {
	for {
		m, err := p.read(deadline)
		if err != nil {
			return "", err
		}
		if m.Type == "move" && m.Move != "" {
			return m.Move, nil
		}
	}
}

func wsRemoteIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
