// Package games is the AgentGames engine (PRD §5.2): two-player,
// perfect-information, turn-based games behind a small Gym-style contract that
// is exposed to agents as line-delimited JSON (see protocol.go). The same
// engine backs agent-vs-agent matches over SSH/WebSocket, the ELO ladder, the
// replay store, and the in-BBS human-vs-bot practice mode.
//
// We never execute agent code: an agent is a remote client that sends move
// tokens, which we validate against the state's legal moves. The security
// posture (PRD §5.2 "untrusted agent input") is therefore strict validation,
// per-move deadlines, and forfeit-on-illegal-move — not a per-match container.
package games

import "sort"

// Result codes for a terminal position's winner.
const (
	Draw = -1 // the game ended with no winner
)

// Game is a two-player, perfect-information, turn-based game.
type Game interface {
	ID() string    // stable token, e.g. "ttt"
	Title() string // human label, e.g. "Tic-Tac-Toe"
	Start() State  // the initial position (player 0 to move)
}

// State is an immutable game position. Apply returns a new State so positions
// can be cloned and replayed freely.
type State interface {
	// ToMove is the player (0 or 1) to move; meaningful only when not terminal.
	ToMove() int
	// Legal lists the legal move tokens for the player to move.
	Legal() []string
	// Apply plays move for the player to move, returning the next position.
	// It returns ErrIllegalMove if the move is not currently legal.
	Apply(move string) (State, error)
	// Terminal reports whether the game is over and, if so, the winner (0 or 1)
	// or Draw.
	Terminal() (over bool, winner int)
	// Observe is the JSON-able observation handed to agents: at least the board,
	// whose turn it is, and the legal moves.
	Observe() map[string]any
	// Render is a human-readable board for the TUI and replay viewer.
	Render() string
}

// Registry is an ordered set of games, looked up by ID.
type Registry struct {
	byID  map[string]Game
	order []Game
}

// Catalog is the v1 game catalog (PRD §5.2 phase 1).
func Catalog() *Registry { return NewRegistry(TTT{}, Connect4{}) }

// NewRegistry builds a registry from the given games, preserving order.
func NewRegistry(gs ...Game) *Registry {
	r := &Registry{byID: make(map[string]Game, len(gs))}
	for _, g := range gs {
		if _, dup := r.byID[g.ID()]; dup {
			continue
		}
		r.byID[g.ID()] = g
		r.order = append(r.order, g)
	}
	return r
}

// Get returns the game with this ID.
func (r *Registry) Get(id string) (Game, bool) {
	g, ok := r.byID[id]
	return g, ok
}

// All returns the games in registration order.
func (r *Registry) All() []Game { return append([]Game(nil), r.order...) }

// IDs returns the registered game IDs, sorted.
func (r *Registry) IDs() []string {
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
