package arcade

import (
	"fmt"
	"math/rand"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
)

// hangman is a built-in leaderboard game: guess the hidden word a letter at a
// time before the gallows fills. It runs endless — each solved word banks
// points and deals a fresh word with full lives; the run ends (and the score
// persists for members) when a single word exhausts all six wrong guesses.
type hangman struct {
	user auth.User
	ctx  plugin.Context

	word    string        // current word, upper-case A–Z
	guessed map[byte]bool // letters tried this word
	wrong   int           // wrong guesses on the current word
	score   int64
	solved  int  // words solved this run
	won     bool // current word fully revealed
	dead    bool // ran out of guesses
	saved   bool
}

const hangmanMaxWrong = 6

// hangmanWords is the word bank — common, all-caps, letters only so the masked
// display and A–Z input stay simple.
var hangmanWords = []string{
	"TERMINAL", "SANDBOX", "KEYBOARD", "NETWORK", "PROTOCOL", "FIREWALL",
	"COMPILER", "VARIABLE", "FUNCTION", "POINTER", "BINARY", "KERNEL",
	"PACKET", "ROUTER", "CIPHER", "GALLOWS", "ARCADE", "INVADER",
	"GHOST", "MAZE", "ROCKET", "LASER", "CRATER", "PIXEL",
	"WIDGET", "BUBBLE", "GOPHER", "DAEMON", "SOCKET", "THREAD",
	"BUFFER", "MODEM", "CURSOR", "SYNTAX", "MODULE", "VECTOR",
}

func newHangman(user auth.User, ctx plugin.Context) *hangman {
	h := &hangman{user: user, ctx: ctx}
	h.deal()
	return h
}

// deal starts a fresh word with full lives.
func (h *hangman) deal() {
	h.word = hangmanWords[rand.Intn(len(hangmanWords))]
	h.guessed = make(map[byte]bool)
	h.wrong = 0
	h.won = false
}

// revealed reports whether every letter of the word has been guessed.
func (h *hangman) revealed() bool {
	for i := 0; i < len(h.word); i++ {
		if !h.guessed[h.word[i]] {
			return false
		}
	}
	return true
}

func (h *hangman) Init() tea.Cmd { return nil }

func (h *hangman) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return h, nil
	}
	switch key.String() {
	case "q", "esc":
		return h, back
	case "r":
		if h.dead {
			return newHangman(h.user, h.ctx), nil
		}
		return h, nil
	case " ", "enter":
		if h.won {
			h.deal() // advance to the next word
		}
		return h, nil
	}

	if h.dead || h.won {
		return h, nil
	}

	// A single letter is a guess.
	s := key.String()
	if len(s) != 1 {
		return h, nil
	}
	c := s[0]
	if c >= 'a' && c <= 'z' {
		c -= 'a' - 'A'
	}
	if c < 'A' || c > 'Z' || h.guessed[c] {
		return h, nil
	}
	h.guessed[c] = true

	if strings.IndexByte(h.word, c) < 0 {
		h.wrong++
		if h.wrong >= hangmanMaxWrong {
			h.dead = true
			// Guests play, members persist (PRD §5.1).
			if !h.saved && h.user.Kind != auth.Guest && h.user.StoreID > 0 && h.score > 0 {
				_ = h.ctx.Store.AddScore(h.user.StoreID, "hangman", h.score)
				h.saved = true
			}
		}
		return h, nil
	}

	if h.revealed() {
		h.won = true
		h.solved++
		// Longer words and unused guesses are worth more.
		h.score += int64(len(h.word)*10 + (hangmanMaxWrong-h.wrong)*5)
	}
	return h, nil
}

// hangmanStages are the gallows ASCII for 0..6 wrong guesses.
var hangmanStages = []string{
	"  +---+\n      |\n      |\n      |\n     ===",
	"  +---+\n  O   |\n      |\n      |\n     ===",
	"  +---+\n  O   |\n  |   |\n      |\n     ===",
	"  +---+\n  O   |\n /|   |\n      |\n     ===",
	"  +---+\n  O   |\n /|\\  |\n      |\n     ===",
	"  +---+\n  O   |\n /|\\  |\n /    |\n     ===",
	"  +---+\n  O   |\n /|\\  |\n / \\  |\n     ===",
}

var (
	hmWordStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24")).Bold(true)
	hmWrongStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	hmGoodStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ade80"))
	hmGallows    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func (h *hangman) View() string {
	out := fmt.Sprintf("Hangman — score %d · solved %d\n\n", h.score, h.solved)
	out += hmGallows.Render(hangmanStages[h.wrong]) + "\n\n"

	// Masked word: reveal the whole thing once the round is over.
	var b strings.Builder
	for i := 0; i < len(h.word); i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		if h.guessed[h.word[i]] || h.dead {
			b.WriteByte(h.word[i])
		} else {
			b.WriteByte('_')
		}
	}
	out += hmWordStyle.Render(b.String()) + "\n\n"

	// Wrong letters tried.
	var wrong []string
	for c := byte('A'); c <= 'Z'; c++ {
		if h.guessed[c] && strings.IndexByte(h.word, c) < 0 {
			wrong = append(wrong, string(c))
		}
	}
	out += fmt.Sprintf("misses (%d/%d): ", h.wrong, hangmanMaxWrong)
	if len(wrong) > 0 {
		out += hmWrongStyle.Render(strings.Join(wrong, " "))
	} else {
		out += hmGallows.Render("—")
	}
	out += "\n\n"

	switch {
	case h.dead:
		out += hmWrongStyle.Render("☠  out of guesses — the word was "+h.word) + "\n"
		out += "r restart · q back"
	case h.won:
		out += hmGoodStyle.Render("✓  solved!") + "  space next word · q back"
	default:
		out += "guess a letter · q back"
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(out)
}
