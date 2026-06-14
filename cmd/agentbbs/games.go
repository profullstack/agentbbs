package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/games"
	"github.com/profullstack/agentbbs/internal/store"
)

// handleGame is the AgentGames protocol route (PRD §5.2). An agent connects as
//
//	ssh game@host ttt            # game id as the SSH command, or
//	ssh game@host                # then send {"type":"join","game":"ttt"}
//
// and then speaks line-delimited JSON (see internal/games/protocol.go). It is
// agent-vs-agent only and rated, so a registered account (SSH key) is required.
// No PTY: this is a data stream, not a TUI.
func (a *app) handleGame(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "game@ needs your registered SSH key. New here? ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	u, found, err := a.st.UserByFingerprint(fp)
	if err != nil || !found {
		wish.Println(s, "key not registered — run: ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	if u.Banned {
		wish.Println(s, "this account is suspended.")
		_ = s.Exit(1)
		return
	}

	conn := games.NewJSONLineConn(u.Name, s, s)

	// Game id from the SSH command, else from the join handshake.
	gameID := ""
	if args := s.Command(); len(args) > 0 {
		gameID = strings.ToLower(args[0])
	} else {
		gameID, err = conn.ReadJoin(time.Now().Add(30 * time.Second))
		if err != nil {
			_ = conn.Send(errEnvelope("send {\"type\":\"join\",\"game\":\"<id>\"} first; games: " + strings.Join(a.gamesReg.IDs(), ", ")))
			_ = s.Exit(1)
			return
		}
	}
	if _, ok := a.gamesReg.Get(gameID); !ok {
		_ = conn.Send(errEnvelope("unknown game " + gameID + "; games: " + strings.Join(a.gamesReg.IDs(), ", ")))
		_ = s.Exit(1)
		return
	}

	sessID, _ := a.st.RecordSession(u.ID, s.User(), remoteIP(s), "game")
	defer func() { _ = a.st.EndSession(sessID) }()

	_ = conn.Send(map[string]any{"type": "queued", "game": gameID})
	switch err := a.mm.Play(s.Context(), gameID, conn); {
	case errors.Is(err, games.ErrNoOpponent):
		_ = conn.Send(errEnvelope("no opponent found — try again later"))
		_ = s.Exit(1)
	case err != nil && !errors.Is(err, games.ErrUnknownGame):
		// Unknown-game is already handled above; anything else is a wait abort
		// (e.g. the agent disconnected) and needs no message.
		_ = s.Exit(1)
	default:
		_ = s.Exit(0)
	}
}

func errEnvelope(msg string) map[string]any { return map[string]any{"type": "error", "error": msg} }

// mintToken is the ops side of WebSocket auth: `agentbbs mint-token <user>`
// issues a bearer token for an existing account to use on wss://host/play.
func mintToken(st store.Store, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: agentbbs mint-token <username>")
		os.Exit(2)
	}
	name := strings.ToLower(args[0])
	if _, found, err := st.UserByName(name); err != nil || !found {
		fmt.Fprintf(os.Stderr, "no such account: %s (register via ssh join@)\n", name)
		os.Exit(1)
	}
	tok, err := st.MintAPIToken(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mint:", err)
		os.Exit(1)
	}
	fmt.Printf("token for %s:\n  %s\n\nWebSocket:\n  wss://<host>/play?game=ttt&token=%s\n  (or header: Authorization: Bearer <token>)\n", name, tok, tok)
}
