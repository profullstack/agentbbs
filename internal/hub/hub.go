// Package hub is the Bubble Tea model every BBS session lands in (PRD §4.1):
// it lists registered plugins and routes the session to the selection,
// reclaiming it when the plugin emits ExitMsg.
package hub

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ade80"))
	lockStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	frameStyle  = lipgloss.NewStyle().Padding(1, 2)
)

// Model is the hub menu.
type Model struct {
	user    auth.User
	ctx     plugin.Context
	plugins []plugin.Plugin

	cursor int
	active tea.Model
	width  int
	height int
	note   string
}

// New builds a hub for one session.
func New(user auth.User, ctx plugin.Context, plugins []plugin.Plugin) Model {
	return Model{user: user, ctx: ctx, plugins: plugins}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Window size is shared with whichever model is active.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
	}

	// A plugin owns the session until it emits ExitMsg (PRD §4.3).
	if m.active != nil {
		if _, ok := msg.(plugin.ExitMsg); ok {
			m.active = nil
			return m, nil
		}
		next, cmd := m.active.Update(msg)
		m.active = next
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		m.note = ""
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.plugins)-1 {
				m.cursor++
			}
		case "enter":
			p := m.plugins[m.cursor]
			if p.RequiresAuth() && m.user.Kind == auth.Guest {
				m.note = "members only — ssh join@ to register"
				return m, nil
			}
			m.active = p.New(m.user, m.ctx)
			cmds := []tea.Cmd{m.active.Init()}
			if m.width > 0 {
				next, cmd := m.active.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
				m.active = next
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.active != nil {
		return m.active.View()
	}
	who := fmt.Sprintf("%s (%s)", m.user.Name, m.user.Kind)
	s := titleStyle.Render("AgentBBS") + dimStyle.Render("  ·  "+who) + "\n\n"
	for i, p := range m.plugins {
		cur := "  "
		if i == m.cursor {
			cur = cursorStyle.Render("> ")
		}
		label := p.Title()
		if p.RequiresAuth() && m.user.Kind == auth.Guest {
			label += lockStyle.Render("  [members]")
		}
		s += fmt.Sprintf("%s%s\n  %s\n", cur, label, dimStyle.Render(p.Description()))
	}
	s += "\n" + dimStyle.Render("↑/↓ move · enter select · q quit")
	if m.note != "" {
		s += "\n" + lockStyle.Render(m.note)
	}
	return frameStyle.Render(s)
}
