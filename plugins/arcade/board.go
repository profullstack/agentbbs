package arcade

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/plugin"
	"github.com/profullstack/agentbbs/internal/store"
)

// board renders the global top scores (PRD §5.1 leaderboards).
type board struct {
	ctx    plugin.Context
	scores []store.Score
	err    error
}

func newBoard(ctx plugin.Context) *board { return &board{ctx: ctx} }

func (b *board) Init() tea.Cmd {
	return func() tea.Msg {
		scores, err := b.ctx.Store.TopScores("snake", 10)
		return boardMsg{scores: scores, err: err}
	}
}

type boardMsg struct {
	scores []store.Score
	err    error
}

func (b *board) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case boardMsg:
		b.scores, b.err = msg.scores, msg.err
	case tea.KeyMsg:
		return b, back
	}
	return b, nil
}

func (b *board) View() string {
	s := tStyle.Render("Leaderboard — snake") + "\n\n"
	switch {
	case b.err != nil:
		s += eStyle.Render("error: " + b.err.Error())
	case len(b.scores) == 0:
		s += dStyle.Render("no scores yet — be the first")
	default:
		for i, sc := range b.scores {
			s += fmt.Sprintf("%2d. %-20s %6d\n", i+1, sc.User, sc.Score)
		}
	}
	s += "\n" + dStyle.Render("any key to return")
	return lipgloss.NewStyle().Padding(1, 2).Render(s)
}
