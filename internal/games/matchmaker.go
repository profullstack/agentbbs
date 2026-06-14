package games

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrNoOpponent means nobody else joined the queue before the wait expired.
var ErrNoOpponent = errors.New("no opponent found")

// ErrUnknownGame means the requested game id is not in the registry.
var ErrUnknownGame = errors.New("unknown game")

// Store persists finished matches and tracks per-game ELO ratings. The SQLite
// store implements it; the matchmaker stays storage-agnostic.
type Store interface {
	// Rating returns a player's current rating for a game (DefaultRating if the
	// player has no rated history there).
	Rating(user, game string) (float64, error)
	// SaveMatch records a finished match and the updated ratings.
	SaveMatch(FinishedMatch) error
}

// FinishedMatch is everything persisted about one completed match.
type FinishedMatch struct {
	Game         string
	Players      [2]string
	Winner       int
	Reason       string
	Moves        []Move
	RatingBefore [2]float64
	RatingAfter  [2]float64
	StartedAt    time.Time
	EndedAt      time.Time
}

// Matchmaker pairs agents into agent-vs-agent matches per game.
type Matchmaker struct {
	reg         *Registry
	store       Store // may be nil (matches are not persisted)
	moveTimeout time.Duration
	queueWait   time.Duration

	mu    sync.Mutex
	queue map[string]*waiter // gameID -> the single player waiting
}

type waiter struct {
	io   PlayerIO
	done chan struct{}
}

// NewMatchmaker builds a matchmaker over a game registry. store may be nil.
// moveTimeout bounds each ply; queueWait bounds how long a lone player waits
// for an opponent before giving up.
func NewMatchmaker(reg *Registry, store Store, moveTimeout, queueWait time.Duration) *Matchmaker {
	if moveTimeout <= 0 {
		moveTimeout = 15 * time.Second
	}
	if queueWait <= 0 {
		queueWait = 2 * time.Minute
	}
	return &Matchmaker{reg: reg, store: store, moveTimeout: moveTimeout, queueWait: queueWait, queue: map[string]*waiter{}}
}

// Play enrolls a player in the queue for gameID and blocks until their match
// finishes. A lone player waits up to queueWait for an opponent (or until ctx
// is done, e.g. they disconnect) and then returns ErrNoOpponent. Once paired,
// the match always runs to completion regardless of ctx/queueWait — those only
// govern the waiting phase.
func (mm *Matchmaker) Play(ctx context.Context, gameID string, io PlayerIO) error {
	g, ok := mm.reg.Get(gameID)
	if !ok {
		return ErrUnknownGame
	}

	mm.mu.Lock()
	if w, waiting := mm.queue[gameID]; waiting && w.io.Name() != io.Name() {
		// An opponent is waiting — pair up and run the match.
		delete(mm.queue, gameID)
		mm.mu.Unlock()
		go mm.run(g, [2]PlayerIO{w.io, io}, w.done)
		<-w.done // both players unblock when the match completes
		return nil
	}
	self := &waiter{io: io, done: make(chan struct{})}
	mm.queue[gameID] = self
	mm.mu.Unlock()

	timer := time.NewTimer(mm.queueWait)
	defer timer.Stop()
	select {
	case <-self.done:
		return nil
	case <-ctx.Done():
		return mm.giveUp(gameID, self, ctx.Err())
	case <-timer.C:
		return mm.giveUp(gameID, self, ErrNoOpponent)
	}
}

// giveUp removes a still-waiting player from the queue. If the player was
// already paired (the match-start goroutine won the race), it instead waits
// for that match to finish and reports success — the match must never be
// abandoned mid-flight.
func (mm *Matchmaker) giveUp(gameID string, self *waiter, reason error) error {
	mm.mu.Lock()
	stillQueued := mm.queue[gameID] == self
	if stillQueued {
		delete(mm.queue, gameID)
	}
	mm.mu.Unlock()
	if stillQueued {
		return reason
	}
	<-self.done // paired after all; let the match complete
	return nil
}

// run plays one match, persists it, sends each player their result, then
// releases both Play calls by closing done.
func (mm *Matchmaker) run(g Game, p [2]PlayerIO, done chan struct{}) {
	defer close(done)
	start := time.Now()
	res := RunMatch(g, p, mm.moveTimeout)
	end := time.Now()

	before := [2]float64{DefaultRating, DefaultRating}
	if mm.store != nil {
		for i := 0; i < 2; i++ {
			if r, err := mm.store.Rating(res.Players[i], g.ID()); err == nil {
				before[i] = r
			}
		}
	}
	na, nb := EloUpdate(before[0], before[1], ScoreFor(res.Winner, 0))
	after := [2]float64{na, nb}

	if mm.store != nil {
		_ = mm.store.SaveMatch(FinishedMatch{
			Game: g.ID(), Players: res.Players, Winner: res.Winner, Reason: res.Reason,
			Moves: res.Moves, RatingBefore: before, RatingAfter: after,
			StartedAt: start, EndedAt: end,
		})
	}

	for i := 0; i < 2; i++ {
		_ = p[i].Send(ResultMsg{
			Type:    "result",
			Winner:  res.Winner,
			Outcome: Outcome(res.Winner, i),
			Reason:  res.Reason,
			Rating:  after[i],
		})
	}
}
