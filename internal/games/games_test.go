package games

import (
	"math"
	"math/rand"
	"testing"
)

// play applies a sequence of moves, failing on any illegal one.
func play(t *testing.T, s State, moves ...string) State {
	t.Helper()
	for _, m := range moves {
		ns, err := s.Apply(m)
		if err != nil {
			t.Fatalf("move %q illegal: %v", m, err)
		}
		s = ns
	}
	return s
}

func TestTTTWinRow(t *testing.T) {
	// X: 0,1,2 ; O: 3,4
	s := play(t, TTT{}.Start(), "0", "3", "1", "4", "2")
	over, w := s.Terminal()
	if !over || w != 0 {
		t.Fatalf("expected X win, got over=%v w=%d", over, w)
	}
}

func TestTTTDraw(t *testing.T) {
	// A standard drawn game.
	s := play(t, TTT{}.Start(), "0", "1", "2", "4", "3", "5", "7", "6", "8")
	over, w := s.Terminal()
	if !over || w != Draw {
		t.Fatalf("expected draw, got over=%v w=%d", over, w)
	}
}

func TestTTTIllegal(t *testing.T) {
	s := play(t, TTT{}.Start(), "4")
	if _, err := s.Apply("4"); err != ErrIllegalMove {
		t.Fatalf("replaying an occupied cell should be illegal, got %v", err)
	}
	if _, err := s.Apply("9"); err != ErrIllegalMove {
		t.Fatalf("out-of-range move should be illegal, got %v", err)
	}
}

func TestConnect4VerticalWin(t *testing.T) {
	// X drops col 0 four times; O drops col 1 between.
	s := play(t, Connect4{}.Start(), "0", "1", "0", "1", "0", "1", "0")
	over, w := s.Terminal()
	if !over || w != 0 {
		t.Fatalf("expected X vertical win, got over=%v w=%d", over, w)
	}
}

func TestConnect4HorizontalWin(t *testing.T) {
	// X fills cols 0-3 on the bottom row; O stacks col 6.
	s := play(t, Connect4{}.Start(), "0", "6", "1", "6", "2", "6", "3")
	over, w := s.Terminal()
	if !over || w != 0 {
		t.Fatalf("expected X horizontal win, got over=%v w=%d", over, w)
	}
}

func TestConnect4LegalAndFullColumn(t *testing.T) {
	s := Connect4{}.Start()
	// Fill column 0 (6 pieces) alternating; nobody connects 4 vertically
	// because players alternate, so the column just fills.
	s = play(t, s, "0", "0", "0", "0", "0", "0")
	for _, m := range s.Legal() {
		if m == "0" {
			t.Fatal("full column 0 should not be legal")
		}
	}
}

func TestEloUpdate(t *testing.T) {
	// Equal ratings, A wins → A gains exactly K/2, B loses K/2.
	na, nb := EloUpdate(1500, 1500, 1)
	if math.Abs(na-1516) > 1e-9 || math.Abs(nb-1484) > 1e-9 {
		t.Fatalf("equal-rating win: na=%.4f nb=%.4f", na, nb)
	}
	// Zero-sum: total rating is conserved.
	if math.Abs((na+nb)-3000) > 1e-9 {
		t.Fatalf("elo not zero-sum: %.4f", na+nb)
	}
	// A draw between equals is a no-op.
	da, db := EloUpdate(1500, 1500, 0.5)
	if math.Abs(da-1500) > 1e-9 || math.Abs(db-1500) > 1e-9 {
		t.Fatalf("equal draw should not move ratings: %.4f %.4f", da, db)
	}
}

func TestGreedyBotTakesWin(t *testing.T) {
	// X to move with 0,1 played and a free 2 → bot must complete the row.
	s := play(t, TTT{}.Start(), "0", "4", "1", "5") // X:0,1 O:4,5, X to move
	bot := GreedyBot{R: rand.New(rand.NewSource(1))}
	if m := bot.Move(s); m != "2" {
		t.Fatalf("greedy bot should win at 2, played %q", m)
	}
}

func TestGreedyBotBlocks(t *testing.T) {
	// O to move; X threatens 0,1 with 2 open → bot must block at 2.
	s := play(t, TTT{}.Start(), "0", "4", "1") // X:0,1 O:4, O to move
	bot := GreedyBot{R: rand.New(rand.NewSource(1))}
	if m := bot.Move(s); m != "2" {
		t.Fatalf("greedy bot should block at 2, played %q", m)
	}
}

func TestRegistry(t *testing.T) {
	r := Catalog()
	if _, ok := r.Get("ttt"); !ok {
		t.Fatal("ttt missing from catalog")
	}
	if _, ok := r.Get("c4"); !ok {
		t.Fatal("c4 missing from catalog")
	}
	if len(r.All()) != 2 {
		t.Fatalf("want 2 games, got %d", len(r.All()))
	}
}
