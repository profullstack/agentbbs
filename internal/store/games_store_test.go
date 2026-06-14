package store

import (
	"testing"
	"time"

	"github.com/profullstack/agentbbs/internal/games"
)

func TestGameRatingsAndMatches(t *testing.T) {
	st := openTest(t)

	// Unrated players start at the default.
	if r, _ := st.Rating("agent-a", "ttt"); r != games.DefaultRating {
		t.Fatalf("default rating = %v", r)
	}

	fm := games.FinishedMatch{
		Game:         "ttt",
		Players:      [2]string{"agent-a", "agent-b"},
		Winner:       0,
		Moves:        []games.Move{{Player: 0, Move: "4"}, {Player: 1, Move: "0"}, {Player: 0, Move: "1"}},
		RatingBefore: [2]float64{1500, 1500},
		RatingAfter:  [2]float64{1516, 1484},
		StartedAt:    time.Now().Add(-time.Minute),
		EndedAt:      time.Now(),
	}
	if err := st.SaveMatch(fm); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Ratings updated and games-played bumped.
	if r, _ := st.Rating("agent-a", "ttt"); r != 1516 {
		t.Fatalf("agent-a rating = %v, want 1516", r)
	}
	if r, _ := st.Rating("agent-b", "ttt"); r != 1484 {
		t.Fatalf("agent-b rating = %v, want 1484", r)
	}

	board, err := st.TopRatings("ttt", 10)
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if len(board) != 2 || board[0].User != "agent-a" || board[0].Played != 1 {
		t.Fatalf("ladder wrong: %+v", board)
	}

	matches, err := st.RecentMatches("ttt", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(matches))
	}
	id := matches[0].ID

	got, ok, err := st.MatchByID(id)
	if err != nil || !ok {
		t.Fatalf("match by id: ok=%v err=%v", ok, err)
	}
	if len(got.Moves) != 3 || got.Moves[0].Move != "4" {
		t.Fatalf("moves not round-tripped: %+v", got.Moves)
	}
	if got.Winner != 0 || got.P0 != "agent-a" {
		t.Fatalf("match meta wrong: %+v", got)
	}

	// A second match bumps games-played and re-rates.
	fm2 := fm
	fm2.RatingBefore = [2]float64{1516, 1484}
	fm2.RatingAfter = [2]float64{1530, 1470}
	if err := st.SaveMatch(fm2); err != nil {
		t.Fatalf("save2: %v", err)
	}
	board, _ = st.TopRatings("ttt", 10)
	if board[0].Played != 2 {
		t.Fatalf("played should be 2, got %d", board[0].Played)
	}
}
