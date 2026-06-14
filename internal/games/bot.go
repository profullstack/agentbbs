package games

import "math/rand"

// Bot picks a move for the player to move in a position. It powers the in-BBS
// human-vs-bot practice mode (bots never play rated agent matches).
type Bot interface {
	Name() string
	Move(s State) string
}

// GreedyBot is a light heuristic that works for any game implementing State:
// it takes an immediately winning move if one exists, blocks the opponent's
// immediate win if forced, and otherwise plays a random legal move. It is a
// fine sparring partner without being game-specific.
type GreedyBot struct{ R *rand.Rand }

func (GreedyBot) Name() string { return "greedy-bot" }

func (b GreedyBot) Move(s State) string {
	legal := s.Legal()
	if len(legal) == 0 {
		return ""
	}
	me := s.ToMove()

	// 1) Win now if we can.
	for _, m := range legal {
		if ns, err := s.Apply(m); err == nil {
			if over, w := ns.Terminal(); over && w == me {
				return m
			}
		}
	}
	// 2) Block an opponent move that would let them win next turn.
	for _, m := range legal {
		ns, err := s.Apply(m)
		if err != nil {
			continue
		}
		if over, _ := ns.Terminal(); over {
			continue
		}
		if !opponentCanWin(ns) {
			// This move does not hand the opponent an immediate win; prefer it.
			return m
		}
	}
	// 3) Otherwise random.
	return legal[b.pick(len(legal))]
}

// opponentCanWin reports whether the player to move in s has an immediate win.
func opponentCanWin(s State) bool {
	opp := s.ToMove()
	for _, m := range s.Legal() {
		if ns, err := s.Apply(m); err == nil {
			if over, w := ns.Terminal(); over && w == opp {
				return true
			}
		}
	}
	return false
}

func (b GreedyBot) pick(n int) int {
	if b.R != nil {
		return b.R.Intn(n)
	}
	return rand.Intn(n)
}
