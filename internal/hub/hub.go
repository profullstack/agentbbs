// Package hub is the Bubble Tea model every BBS session lands in (PRD §4.1):
// it lists registered plugins and routes the session to the selection,
// reclaiming it when the plugin emits ExitMsg.
//
// Besides in-hub plugins, the hub also lists "session apps" — features that take
// over the whole terminal (a pod shell, the IRC client, the newsreader, the mail
// reader, Tor) rather than rendering inside the hub. Selecting one suspends the
// hub via tea.Exec, runs it, and returns to the menu. This is what lets a member
// reach everything from a single `ssh <name>@host` login instead of separate
// `ssh pod@` / `ssh irc@` / `ssh news@` connections.
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

// SessionApp is a hub entry that takes over the terminal — a pod shell, the IRC
// client, the newsreader, the mail reader, or Tor — instead of running as an
// in-hub model. Selecting it suspends the hub (tea.Exec), runs Cmd, then returns
// to the menu.
type SessionApp struct {
	Title       string
	Description string
	// Locked, when non-empty, marks the entry visible-but-unavailable (e.g. a
	// paid feature for a free member, or members-only for a guest). The reason
	// is shown when the entry is selected; the app cannot be launched.
	Locked string
	// Cmd is the terminal-takeover command run under tea.Exec.
	Cmd tea.ExecCommand
}

// appExitMsg is delivered when a SessionApp launched via tea.Exec returns.
type appExitMsg struct{ err error }

// Model is the hub menu.
type Model struct {
	user    auth.User
	ctx     plugin.Context
	plugins []plugin.Plugin
	apps    []SessionApp

	cursor int
	active tea.Model
	width  int
	height int
	note   string
}

// New builds a hub for one session. apps are terminal-takeover features listed
// after the in-hub plugins (may be nil).
func New(user auth.User, ctx plugin.Context, plugins []plugin.Plugin, apps []SessionApp) Model {
	return Model{user: user, ctx: ctx, plugins: plugins, apps: apps}
}

func (m Model) Init() tea.Cmd { return nil }

// entries is the total number of selectable rows (plugins then apps).
func (m Model) entries() int { return len(m.plugins) + len(m.apps) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Window size is shared with whichever model is active.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
	}

	// A returning session app (tea.Exec finished) hands control back here.
	if ae, ok := msg.(appExitMsg); ok {
		if ae.err != nil {
			m.note = ae.err.Error()
		}
		return m, nil
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
			if m.cursor < m.entries()-1 {
				m.cursor++
			}
		case "enter":
			return m.selectEntry()
		}
	}
	return m, nil
}

// selectEntry launches whatever the cursor is on: an in-hub plugin or a session app.
func (m Model) selectEntry() (tea.Model, tea.Cmd) {
	if m.cursor < len(m.plugins) {
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

	app := m.apps[m.cursor-len(m.plugins)]
	if app.Locked != "" {
		m.note = app.Locked
		return m, nil
	}
	return m, tea.Exec(app.Cmd, func(err error) tea.Msg { return appExitMsg{err} })
}

func (m Model) View() string {
	if m.active != nil {
		return m.active.View()
	}
	who := fmt.Sprintf("%s (%s)", m.user.Name, m.user.Kind)
	s := titleStyle.Render("AgentBBS") + dimStyle.Render("  ·  "+who) + "\n\n"
	row := 0
	for _, p := range m.plugins {
		label := p.Title()
		if p.RequiresAuth() && m.user.Kind == auth.Guest {
			label += lockStyle.Render("  [members]")
		}
		s += m.renderRow(row, label, p.Description())
		row++
	}
	for _, app := range m.apps {
		label := app.Title
		if app.Locked != "" {
			label += lockStyle.Render("  [locked]")
		}
		s += m.renderRow(row, label, app.Description)
		row++
	}
	s += "\n" + dimStyle.Render("↑/↓ move · enter select · q quit")
	if m.note != "" {
		s += "\n" + lockStyle.Render(m.note)
	}
	return frameStyle.Render(s)
}

// renderRow renders one menu line with the cursor and dimmed description.
func (m Model) renderRow(i int, label, desc string) string {
	cur := "  "
	if i == m.cursor {
		cur = cursorStyle.Render("> ")
	}
	return fmt.Sprintf("%s%s\n  %s\n", cur, label, dimStyle.Render(desc))
}
