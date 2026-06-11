package arcade

import (
	"fmt"
	"math/rand"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
)

// snake is the built-in leaderboard game: simple, fair, trivially judged.
type snake struct {
	user auth.User
	ctx  plugin.Context

	w, h  int // board size in cells
	body  []pos
	dir   pos
	food  pos
	score int64
	dead  bool
	saved bool
}

type pos struct{ x, y int }

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func newSnake(user auth.User, ctx plugin.Context, termW, termH int) *snake {
	w, h := 32, 16
	if termW > 0 && termW/2-4 < w {
		w = termW/2 - 4
	}
	if termH > 0 && termH-8 < h {
		h = termH - 8
	}
	if w < 10 {
		w = 10
	}
	if h < 8 {
		h = 8
	}
	s := &snake{user: user, ctx: ctx, w: w, h: h,
		body: []pos{{w / 2, h / 2}}, dir: pos{1, 0}}
	s.placeFood()
	return s
}

func (s *snake) placeFood() {
	for {
		p := pos{rand.Intn(s.w), rand.Intn(s.h)}
		if !s.hits(p) {
			s.food = p
			return
		}
	}
}

func (s *snake) hits(p pos) bool {
	for _, b := range s.body {
		if b == p {
			return true
		}
	}
	return false
}

func (s *snake) Init() tea.Cmd { return tick() }

func (s *snake) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc":
			return s, back
		case "up", "w":
			if s.dir.y == 0 {
				s.dir = pos{0, -1}
			}
		case "down", "s":
			if s.dir.y == 0 {
				s.dir = pos{0, 1}
			}
		case "left", "a":
			if s.dir.x == 0 {
				s.dir = pos{-1, 0}
			}
		case "right", "d":
			if s.dir.x == 0 {
				s.dir = pos{1, 0}
			}
		case "r":
			if s.dead {
				ns := newSnake(s.user, s.ctx, 0, 0)
				ns.w, ns.h = s.w, s.h
				return ns, ns.Init()
			}
		}
	case tickMsg:
		if s.dead {
			return s, nil
		}
		head := pos{s.body[0].x + s.dir.x, s.body[0].y + s.dir.y}
		if head.x < 0 || head.y < 0 || head.x >= s.w || head.y >= s.h || s.hits(head) {
			s.dead = true
			// Guests play, members persist (PRD §5.1).
			if !s.saved && s.user.Kind != auth.Guest && s.user.StoreID > 0 && s.score > 0 {
				_ = s.ctx.Store.AddScore(s.user.StoreID, "snake", s.score)
				s.saved = true
			}
			return s, nil
		}
		s.body = append([]pos{head}, s.body...)
		if head == s.food {
			s.score += 10
			s.placeFood()
		} else {
			s.body = s.body[:len(s.body)-1]
		}
		return s, tick()
	}
	return s, nil
}

var (
	snakeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ade80"))
	foodStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	wallStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func (s *snake) View() string {
	out := fmt.Sprintf("Snake — score %d", s.score)
	if s.dead {
		out += "  ☠  dead (r restart · q back)"
	}
	out += "\n" + wallStyle.Render("┌"+repeat("──", s.w)+"┐") + "\n"
	for y := 0; y < s.h; y++ {
		row := wallStyle.Render("│")
		for x := 0; x < s.w; x++ {
			switch {
			case s.hits(pos{x, y}):
				row += snakeStyle.Render("██")
			case s.food == pos{x, y}:
				row += foodStyle.Render("◆ ")
			default:
				row += "  "
			}
		}
		out += row + wallStyle.Render("│") + "\n"
	}
	out += wallStyle.Render("└" + repeat("──", s.w) + "┘")
	return lipgloss.NewStyle().Padding(1, 2).Render(out)
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
