package games

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

// firstLegalPlayer plays the first legal move it is offered — enough to drive a
// full match deterministically through the protocol and matchmaker.
type firstLegalPlayer struct {
	name      string
	lastLegal []string
	hello     *helloMsg
	result    *ResultMsg
}

func (p *firstLegalPlayer) Name() string { return p.name }

func (p *firstLegalPlayer) Send(v any) error {
	switch m := v.(type) {
	case helloMsg:
		p.hello = &m
	case stateMsg:
		if m.YourTurn {
			if legal, ok := m.Observation["legal"].([]string); ok {
				p.lastLegal = legal
			}
		}
	case ResultMsg:
		p.result = &m
	}
	return nil
}

func (p *firstLegalPlayer) ReadMove(time.Time) (string, error) {
	if len(p.lastLegal) == 0 {
		return "", ErrClosed
	}
	return p.lastLegal[0], nil
}

type fakeStore struct {
	mu      sync.Mutex
	ratings map[string]float64
	saved   []FinishedMatch
}

func (s *fakeStore) Rating(user, game string) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.ratings[user+"/"+game]; ok {
		return r, nil
	}
	return DefaultRating, nil
}

func (s *fakeStore) SaveMatch(fm FinishedMatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, fm)
	s.ratings[fm.Players[0]+"/"+fm.Game] = fm.RatingAfter[0]
	s.ratings[fm.Players[1]+"/"+fm.Game] = fm.RatingAfter[1]
	return nil
}

func TestMatchmakerFullMatch(t *testing.T) {
	store := &fakeStore{ratings: map[string]float64{}}
	mm := NewMatchmaker(Catalog(), store, time.Second, time.Minute)

	a := &firstLegalPlayer{name: "agent-a"}
	b := &firstLegalPlayer{name: "agent-b"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = mm.Play(context.Background(), "ttt", a) }()
	// Give a a moment to enter the queue so pairing is deterministic.
	time.Sleep(20 * time.Millisecond)
	go func() { defer wg.Done(); _ = mm.Play(context.Background(), "ttt", b) }()
	wg.Wait()

	if len(store.saved) != 1 {
		t.Fatalf("want 1 saved match, got %d", len(store.saved))
	}
	fm := store.saved[0]
	if fm.Game != "ttt" || fm.Players != [2]string{"agent-a", "agent-b"} {
		t.Fatalf("unexpected match meta: %+v", fm)
	}
	// The recorded moves must replay to the same terminal winner.
	s := State(TTT{}.Start())
	for _, mv := range fm.Moves {
		ns, err := s.Apply(mv.Move)
		if err != nil {
			t.Fatalf("replay move %q illegal: %v", mv.Move, err)
		}
		s = ns
	}
	over, winner := s.Terminal()
	if !over {
		t.Fatal("replayed moves did not reach a terminal position")
	}
	if winner != fm.Winner {
		t.Fatalf("replay winner %d != recorded %d", winner, fm.Winner)
	}
	// ELO is zero-sum and both players were notified with matching ratings.
	if math.Abs((fm.RatingAfter[0]+fm.RatingAfter[1])-2*DefaultRating) > 1e-9 {
		t.Fatalf("elo not zero-sum: %v", fm.RatingAfter)
	}
	if a.result == nil || b.result == nil {
		t.Fatal("both players must receive a result message")
	}
	if a.result.Outcome == "win" && b.result.Outcome != "loss" {
		t.Fatalf("outcomes disagree: a=%s b=%s", a.result.Outcome, b.result.Outcome)
	}
	if a.hello == nil || a.hello.Player != 0 || b.hello.Player != 1 {
		t.Fatalf("hello player indices wrong: %+v %+v", a.hello, b.hello)
	}
}

func TestMatchmakerNoOpponent(t *testing.T) {
	mm := NewMatchmaker(Catalog(), nil, time.Second, 30*time.Millisecond)
	err := mm.Play(context.Background(), "ttt", &firstLegalPlayer{name: "lonely"})
	if err != ErrNoOpponent {
		t.Fatalf("want ErrNoOpponent, got %v", err)
	}
}

func TestMatchmakerUnknownGame(t *testing.T) {
	mm := NewMatchmaker(Catalog(), nil, time.Second, time.Second)
	if err := mm.Play(context.Background(), "nope", &firstLegalPlayer{name: "x"}); err != ErrUnknownGame {
		t.Fatalf("want ErrUnknownGame, got %v", err)
	}
}
