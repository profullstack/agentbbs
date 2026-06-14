package games

import (
	"errors"
	"time"
)

// The wire protocol is line-delimited JSON (one object per message), identical
// over SSH (`game@`) and WebSocket. Flow:
//
//	→ client sends {"type":"join","game":"ttt"}              (transport handshake)
//	← server {"type":"hello","player":0,"game":"ttt","opponent":"agent-bob"}
//	← server {"type":"state","observation":{…},"yourTurn":true}   (each ply)
//	→ client {"type":"move","move":"4"}                       (only on your turn)
//	← server {"type":"result","winner":0,"outcome":"win","rating":1516}
//
// Illegal moves, timeouts, and disconnects forfeit the match.

// ErrTimeout means a player did not move before the deadline. ErrClosed means
// the player's connection ended mid-match. Both forfeit.
var (
	ErrTimeout = errors.New("move timeout")
	ErrClosed  = errors.New("connection closed")
)

// PlayerIO is one player's transport for a match. Send writes a server→client
// message; ReadMove blocks for the next move message until deadline.
type PlayerIO interface {
	Name() string
	Send(v any) error
	ReadMove(deadline time.Time) (string, error)
}

// Move is one ply in a match, for replay.
type Move struct {
	Player int    `json:"player"`
	Move   string `json:"move"`
}

// MatchResult is the outcome of a finished match.
type MatchResult struct {
	Game    string
	Players [2]string
	Winner  int    // 0, 1, or Draw
	Reason  string // "" for a normal finish, else the forfeit cause
	Moves   []Move
}

// Outbound message envelopes.
type helloMsg struct {
	Type     string `json:"type"`
	Player   int    `json:"player"`
	Game     string `json:"game"`
	Opponent string `json:"opponent"`
}
type stateMsg struct {
	Type        string         `json:"type"`
	Observation map[string]any `json:"observation"`
	YourTurn    bool           `json:"yourTurn"`
}

// ResultMsg is the final per-player message. It is sent by the caller (the
// matchmaker) after ratings are computed, so it carries the player's new ELO.
type ResultMsg struct {
	Type    string  `json:"type"`
	Winner  int     `json:"winner"`
	Outcome string  `json:"outcome"` // "win" | "loss" | "draw"
	Reason  string  `json:"reason,omitempty"`
	Rating  float64 `json:"rating"`
}

// RunMatch drives a game to completion between two players, sending hello and
// per-ply state messages. It never returns a transport error: an I/O failure,
// timeout, or illegal move forfeits the offending player. The final result
// message (with ratings) is the caller's responsibility.
func RunMatch(g Game, p [2]PlayerIO, moveTimeout time.Duration) *MatchResult {
	res := &MatchResult{Game: g.ID(), Players: [2]string{p[0].Name(), p[1].Name()}}

	_ = p[0].Send(helloMsg{Type: "hello", Player: 0, Game: g.ID(), Opponent: p[1].Name()})
	_ = p[1].Send(helloMsg{Type: "hello", Player: 1, Game: g.ID(), Opponent: p[0].Name()})

	s := g.Start()
	for {
		if over, winner := s.Terminal(); over {
			res.Winner = winner
			return res
		}
		cur := s.ToMove()
		obs := s.Observe()
		_ = p[0].Send(stateMsg{Type: "state", Observation: obs, YourTurn: cur == 0})
		_ = p[1].Send(stateMsg{Type: "state", Observation: obs, YourTurn: cur == 1})

		mv, err := p[cur].ReadMove(time.Now().Add(moveTimeout))
		if err != nil {
			res.Winner = 1 - cur
			res.Reason = "forfeit: " + err.Error()
			return res
		}
		ns, err := s.Apply(mv)
		if err != nil {
			res.Winner = 1 - cur
			res.Reason = "forfeit: illegal move"
			return res
		}
		res.Moves = append(res.Moves, Move{Player: cur, Move: mv})
		s = ns
	}
}

// Outcome returns the per-player outcome string for a winner code.
func Outcome(winner, player int) string {
	switch {
	case winner == Draw:
		return "draw"
	case winner == player:
		return "win"
	default:
		return "loss"
	}
}
