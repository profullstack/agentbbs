package games

import (
	"errors"
	"math"
)

// ErrIllegalMove is returned by State.Apply for a move that is not legal in the
// current position. The match driver treats it as a forfeit.
var ErrIllegalMove = errors.New("illegal move")

// DefaultRating is the ELO a player starts at before their first rated match.
const DefaultRating = 1500.0

// kFactor scales how far a single result moves a rating.
const kFactor = 32.0

// Expected returns the expected score (0..1) for a player rated a against an
// opponent rated b, per the standard logistic ELO model.
func Expected(a, b float64) float64 {
	return 1.0 / (1.0 + math.Pow(10, (b-a)/400.0))
}

// EloUpdate returns the new ratings for two players after a game, given the
// score for player A (1 win, 0.5 draw, 0 loss). B's score is the complement.
func EloUpdate(ra, rb, scoreA float64) (na, nb float64) {
	ea := Expected(ra, rb)
	eb := Expected(rb, ra)
	na = ra + kFactor*(scoreA-ea)
	nb = rb + kFactor*((1-scoreA)-eb)
	return na, nb
}

// ScoreFor converts a terminal winner into player p's score (1/0.5/0).
func ScoreFor(winner, p int) float64 {
	switch {
	case winner == Draw:
		return 0.5
	case winner == p:
		return 1
	default:
		return 0
	}
}
