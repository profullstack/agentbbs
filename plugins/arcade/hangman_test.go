package arcade

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
)

// key builds a single-rune key press the way bubbletea delivers it.
func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// guest avoids the store path (only members persist), so ctx.Store can be nil.
func newTestHangman(word string) *hangman {
	h := newHangman(auth.User{Kind: auth.Guest}, plugin.Context{})
	h.word = word
	h.guessed = make(map[byte]bool)
	h.wrong = 0
	h.won = false
	return h
}

func TestHangmanSolveScores(t *testing.T) {
	h := newTestHangman("CAT")
	for _, r := range "CAT" {
		m, _ := h.Update(key(r))
		h = m.(*hangman)
	}
	if !h.won {
		t.Fatalf("expected won after guessing every letter")
	}
	if h.solved != 1 {
		t.Fatalf("solved = %d, want 1", h.solved)
	}
	// len 3 *10 + (6-0)*5 = 60.
	if h.score != 60 {
		t.Fatalf("score = %d, want 60", h.score)
	}
	if h.dead {
		t.Fatalf("should not be dead after a solve")
	}
}

func TestHangmanWrongGuessIsCountedOnce(t *testing.T) {
	h := newTestHangman("CAT")
	for i := 0; i < 3; i++ { // repeat the same wrong letter
		m, _ := h.Update(key('Z'))
		h = m.(*hangman)
	}
	if h.wrong != 1 {
		t.Fatalf("wrong = %d, want 1 (repeat guesses must not stack)", h.wrong)
	}
}

func TestHangmanDeathAfterSixMisses(t *testing.T) {
	h := newTestHangman("CAT")
	for _, r := range "BDEFGH" { // six letters absent from CAT
		m, _ := h.Update(key(r))
		h = m.(*hangman)
	}
	if h.wrong != hangmanMaxWrong {
		t.Fatalf("wrong = %d, want %d", h.wrong, hangmanMaxWrong)
	}
	if !h.dead {
		t.Fatalf("expected dead after %d misses", hangmanMaxWrong)
	}
}

func TestHangmanLowercaseInputAndAdvance(t *testing.T) {
	h := newTestHangman("CAT")
	for _, r := range "cat" { // lowercase should still solve
		m, _ := h.Update(key(r))
		h = m.(*hangman)
	}
	if !h.won {
		t.Fatalf("lowercase input should solve the word")
	}
	// space deals a fresh word and clears the round flags.
	m, _ := h.Update(tea.KeyMsg{Type: tea.KeySpace})
	h = m.(*hangman)
	if h.won || h.wrong != 0 || len(h.guessed) != 0 {
		t.Fatalf("space should deal a fresh round: won=%v wrong=%d guessed=%d", h.won, h.wrong, len(h.guessed))
	}
}
