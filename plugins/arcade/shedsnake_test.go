package arcade

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
)

func newTestShed() *shedsnake {
	s := newShedSnake(auth.User{Kind: auth.Guest}, plugin.Context{}, 0, 0)
	return s
}

// step advances one game tick.
func (s *shedsnake) step() *shedsnake {
	next, _ := s.Update(shedTickMsg{})
	return next.(*shedsnake)
}

func TestShedMoltsOnEat(t *testing.T) {
	s := newTestShed()
	startLen := len(s.body)
	startSpeed := s.speedMS

	// Put the apple directly in front of the head so the next tick eats it.
	head := s.wrap(pos{s.body[0].x + s.dir.x, s.body[0].y + s.dir.y})
	s.food = head

	s = s.step()

	if s.gen != 1 {
		t.Fatalf("expected 1 molt after eating, got %d", s.gen)
	}
	// The whole body at molt time (startLen cells, straight → all unique) is shed.
	if len(s.skin) != startLen {
		t.Fatalf("expected %d shed scales, got %d", startLen, len(s.skin))
	}
	// The cell the snake ate on is part of the shed skin it stands on.
	if _, ok := s.skin[head]; !ok {
		t.Errorf("head cell %v where it ate should be a scale", head)
	}
	if len(s.body) != startLen+shedGrow {
		t.Errorf("body should grow by %d on molt: got %d want %d", shedGrow, len(s.body), startLen+shedGrow)
	}
	if s.speedMS >= startSpeed {
		t.Errorf("speed should increase (interval shrink) on molt: %d !< %d", s.speedMS, startSpeed)
	}
}

func TestShedGracePeriodNoInstantDeath(t *testing.T) {
	// After molting, the fresh scales sit under the body; the snake must be able
	// to slither off them without dying.
	s := newTestShed()
	s.food = s.wrap(pos{s.body[0].x + s.dir.x, s.body[0].y + s.dir.y})
	s = s.step() // eat + molt
	if s.dead {
		t.Fatal("died on the tick it molted")
	}
	// Slither straight for a few ticks over its own fresh scales.
	for i := 0; i < len(s.body)+2; i++ {
		s.food = pos{-9, -9} // keep food away
		s = s.step()
		if s.dead {
			t.Fatalf("died sliding off fresh molt at tick %d", i)
		}
	}
}

func TestShedDeathOnBitingOldScale(t *testing.T) {
	s := newTestShed()
	// Plant an old scale one cell ahead of the head; heading right into it kills.
	ahead := s.wrap(pos{s.body[0].x + s.dir.x, s.body[0].y + s.dir.y})
	s.skin[ahead] = 0 // an old generation, not under the body
	s.food = pos{-9, -9}
	s = s.step()
	if !s.dead {
		t.Fatal("expected death from biting a shed scale")
	}
}

func TestShedWraps(t *testing.T) {
	s := newTestShed()
	// Drive the head to the right edge, then one more step wraps to x=0.
	s.body = []pos{{s.w - 1, 3}, {s.w - 2, 3}, {s.w - 3, 3}}
	s.dir, s.next = pos{1, 0}, pos{1, 0}
	s.food = pos{-9, -9}
	s = s.step()
	if s.body[0].x != 0 {
		t.Fatalf("head should wrap to x=0, got x=%d", s.body[0].x)
	}
}

func TestShedNo180(t *testing.T) {
	// Pressing the reverse direction must not fold the snake back on itself.
	s := newTestShed() // heading right
	next, _ := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}) // left
	s = next.(*shedsnake)
	if s.next == (pos{-1, 0}) {
		t.Fatal("180° reversal should be ignored while moving horizontally")
	}
}
