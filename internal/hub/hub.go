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
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	selStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e2e8f0"))
	lockStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	bannerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38bdf8"))
	motdStyle   = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#4ade80")).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)
	frameStyle = lipgloss.NewStyle().Padding(1, 2)
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
	banner  string // ASCII brand banner shown above the menu (may be empty)
	motd    string // message-of-the-day shown in a box under the title (may be empty)

	cursor int
	active tea.Model
	width  int
	height int
	note   string
}

// New builds a hub for one session. apps are terminal-takeover features listed
// after the in-hub plugins (may be nil). banner is an ASCII brand banner and
// motd a short welcome message; either may be empty.
func New(user auth.User, ctx plugin.Context, plugins []plugin.Plugin, apps []SessionApp, banner, motd string) Model {
	return Model{user: user, ctx: ctx, plugins: plugins, apps: apps, banner: banner, motd: motd}
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

	// A plugin owns the session until it emits ExitMsg (PRD §4.3) — except
	// ctrl+c, which the hub always reclaims so EVERY menu/game has a guaranteed
	// way back to the main menu (some plugins, e.g. snake, don't handle it).
	if m.active != nil {
		if k, ok := msg.(tea.KeyMsg); ok && k.String() == "ctrl+c" {
			m.active = nil
			m.note = "← back to main menu"
			return m, nil
		}
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
	var b strings.Builder
	if m.banner != "" {
		b.WriteString(bannerStyle.Render(m.banner) + "\n\n")
	}
	who := fmt.Sprintf("%s (%s)", m.user.Name, m.user.Kind)
	b.WriteString(titleStyle.Render("AgentBBS") + dimStyle.Render("  ·  "+who) + "\n")
	if m.motd != "" {
		b.WriteString("\n" + motdStyle.Render(m.motd) + "\n")
	}
	b.WriteString("\n")
	row := 0
	for _, p := range m.plugins {
		label := p.Title()
		if p.RequiresAuth() && m.user.Kind == auth.Guest {
			label += lockStyle.Render("  [members]")
		}
		b.WriteString(m.renderRow(row, label, p.Description()))
		row++
	}
	for _, app := range m.apps {
		label := app.Title
		if app.Locked != "" {
			label += lockStyle.Render("  [locked]")
		}
		b.WriteString(m.renderRow(row, label, app.Description))
		row++
	}
	b.WriteString("\n" + dimStyle.Render("↑/↓ move · enter select · ctrl+c back · q quit"))
	if m.note != "" {
		b.WriteString("\n" + lockStyle.Render(m.note))
	}
	return frameStyle.Render(b.String())
}

// renderRow renders one menu line with the cursor and dimmed description. The
// selected row's cursor and label are highlighted.
func (m Model) renderRow(i int, label, desc string) string {
	cur := "  "
	if i == m.cursor {
		cur = cursorStyle.Render("❯ ")
		label = selStyle.Render(label)
	}
	return fmt.Sprintf("%s%s\n  %s\n", cur, label, dimStyle.Render(desc))
}
