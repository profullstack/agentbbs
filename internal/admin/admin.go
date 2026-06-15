// Package admin is the privileged admin console (PRD §6), reached over SSH as
// `ssh admin@host` by an account in the operator allowlist ($AGENTBBS_ADMINS).
//
// It is a self-contained Bubble Tea model (not a hub plugin) so it never shows
// up in the public menu: the route gates access, the model drives the console.
// Four sections cover the M2 scope — users, live sessions, moderation/audit,
// and config/plugins — over the shared store plus a live-session registry.
package admin

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/store"
	"github.com/profullstack/agentbbs/internal/ui"
)

// Live is one connected SSH session, as seen by the registry.
type Live struct {
	ID     int64
	User   string
	Remote string
	Route  string
	Start  time.Time
}

// LiveSessions is the registry of currently-connected sessions the console can
// list and disconnect. main wires the concrete implementation.
type LiveSessions interface {
	List() []Live
	Kill(id int64) bool
}

// PluginInfo identifies a registered hub plugin for the config screen.
type PluginInfo struct{ ID, Title string }

// Env is the read-only runtime snapshot shown on the config screen.
type Env struct {
	Host           string
	Sandbox        string
	PodsEngine     string // "" when pods are disabled on this host
	MailConfigured bool
	Admins         []string
	Plugins        []PluginInfo
}

type screen int

const (
	screenMenu screen = iota
	screenUsers
	screenSessions
	screenAudit
	screenConfig
)

var menuItems = []struct {
	screen screen
	label  string
	desc   string
}{
	{screenUsers, "Users & members", "List accounts, suspend/ban, inspect"},
	{screenSessions, "Sessions & pods", "Live connections — disconnect abusers"},
	{screenAudit, "Moderation & audit", "Admin action log + agent@ transcripts"},
	{screenConfig, "Config & plugins", "Runtime config; enable/disable plugins"},
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(ui.Green)
	dimStyle    = ui.Dim
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(ui.Green)
	warnStyle   = ui.Danger
	okStyle     = lipgloss.NewStyle().Foreground(ui.Green)
	headStyle   = lipgloss.NewStyle().Bold(true).Foreground(ui.Blue)
	frameStyle  = ui.Frame
)

// Model is the admin console.
type Model struct {
	admin    auth.User
	st       store.Store
	live     LiveSessions
	selfLive int64 // the admin's own live-session id, never killed
	env      Env

	screen screen
	cursor int
	note   string
	width  int
	height int

	// loaded per-screen data
	users    []store.User
	sessions []Live
	actions  []store.AdminAction
	chats    []store.ChatRow
	disabled map[string]bool
	auditTab int // 0 = admin actions, 1 = agent@ chats
}

// New builds the admin console for one session.
func New(admin auth.User, st store.Store, live LiveSessions, selfLive int64, env Env) Model {
	return Model{admin: admin, st: st, live: live, selfLive: selfLive, env: env, disabled: map[string]bool{}}
}

func (m Model) Init() tea.Cmd { return nil }

func (m *Model) log(action, target, detail string) {
	_ = m.st.LogAdminAction(m.admin.Name, action, target, detail)
}

// load refreshes the data backing the current screen.
func (m *Model) load() {
	m.cursor = 0
	switch m.screen {
	case screenUsers:
		m.users, _ = m.st.ListUsers(200)
	case screenSessions:
		m.sessions = m.live.List()
	case screenAudit:
		m.actions, _ = m.st.RecentAdminActions(100)
		m.chats, _ = m.st.RecentChatsAll(100)
	case screenConfig:
		m.disabled, _ = m.st.DisabledPlugins()
	}
}

// rowCount is the number of selectable rows on the active screen.
func (m Model) rowCount() int {
	switch m.screen {
	case screenUsers:
		return len(m.users)
	case screenSessions:
		return len(m.sessions)
	case screenConfig:
		return len(m.env.Plugins)
	default:
		return 0
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		m.note = ""
		switch msg.String() {
		case "ctrl+c", "Q":
			return m, tea.Quit
		}
		if m.screen == screenMenu {
			return m.updateMenu(msg)
		}
		return m.updateScreen(msg)
	}
	return m, nil
}

func (m Model) updateMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(menuItems)-1 {
			m.cursor++
		}
	case "enter", "right", "l":
		m.screen = menuItems[m.cursor].screen
		m.load()
	}
	return m, nil
}

func (m Model) updateScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "left", "h", "backspace":
		m.screen = screenMenu
		m.cursor = 0
		return m, nil
	case "r":
		m.load()
		m.note = "refreshed"
		return m, nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < m.rowCount()-1 {
			m.cursor++
		}
		return m, nil
	}

	switch m.screen {
	case screenUsers:
		return m.updateUsers(msg)
	case screenSessions:
		return m.updateSessions(msg)
	case screenAudit:
		return m.updateAudit(msg)
	case screenConfig:
		return m.updateConfig(msg)
	}
	return m, nil
}

func (m Model) updateUsers(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() != "b" || m.cursor >= len(m.users) {
		return m, nil
	}
	u := m.users[m.cursor]
	if auth.IsAdmin(u.Name) {
		m.note = warnStyle.Render("refusing to ban an operator (" + u.Name + ")")
		return m, nil
	}
	ban := !u.Banned
	if err := m.st.SetBanned(u.ID, ban); err != nil {
		m.note = warnStyle.Render("ban failed: " + err.Error())
		return m, nil
	}
	action := "unban"
	if ban {
		action = "ban"
	}
	m.log(action, u.Name, "")
	m.users[m.cursor].Banned = ban
	m.note = okStyle.Render(action + "ned " + u.Name)
	return m, nil
}

func (m Model) updateSessions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() != "k" || m.cursor >= len(m.sessions) {
		return m, nil
	}
	s := m.sessions[m.cursor]
	if s.ID == m.selfLive {
		m.note = warnStyle.Render("that's your own session")
		return m, nil
	}
	if m.live.Kill(s.ID) {
		m.log("kill-session", s.User, s.Remote+" "+s.Route)
		m.note = okStyle.Render("disconnected " + s.User + " (" + s.Route + ")")
	} else {
		m.note = warnStyle.Render("session already gone")
	}
	m.sessions = m.live.List()
	if m.cursor >= len(m.sessions) && m.cursor > 0 {
		m.cursor--
	}
	return m, nil
}

func (m Model) updateAudit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "tab" {
		m.auditTab = 1 - m.auditTab
	}
	return m, nil
}

func (m Model) updateConfig(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if (msg.String() != " " && msg.String() != "enter") || m.cursor >= len(m.env.Plugins) {
		return m, nil
	}
	p := m.env.Plugins[m.cursor]
	disable := !m.disabled[p.ID]
	if err := m.st.SetPluginDisabled(p.ID, disable); err != nil {
		m.note = warnStyle.Render("toggle failed: " + err.Error())
		return m, nil
	}
	m.disabled[p.ID] = disable
	action := "enable-plugin"
	state := "enabled"
	if disable {
		action, state = "disable-plugin", "disabled"
	}
	m.log(action, p.ID, "")
	m.note = okStyle.Render(p.Title + " " + state + " (takes effect on next sign-in)")
	return m, nil
}

func (m Model) View() string {
	var body, help string
	switch m.screen {
	case screenMenu:
		body, help = m.viewMenu()
	case screenUsers:
		body, help = m.viewUsers()
	case screenSessions:
		body, help = m.viewSessions()
	case screenAudit:
		body, help = m.viewAudit()
	case screenConfig:
		body, help = m.viewConfig()
	}

	header := titleStyle.Render("AgentBBS admin") + dimStyle.Render("  ·  "+m.admin.Name)
	out := header + "\n\n" + body + "\n" + ui.KeyBar(help)
	if m.note != "" {
		out += "\n" + m.note
	}
	return frameStyle.Render(out)
}

func (m Model) viewMenu() (string, string) {
	var b strings.Builder
	for i, it := range menuItems {
		cur := "  "
		if i == m.cursor {
			cur = cursorStyle.Render("> ")
		}
		fmt.Fprintf(&b, "%s%s\n  %s\n", cur, it.label, dimStyle.Render(it.desc))
	}
	return b.String(), "↑/↓ move · enter open · q quit"
}

func (m Model) viewUsers() (string, string) {
	var b strings.Builder
	b.WriteString(headStyle.Render(fmt.Sprintf("Users (%d)", len(m.users))) + "\n\n")
	if len(m.users) == 0 {
		b.WriteString(dimStyle.Render("  (no accounts yet)\n"))
	}
	for i, u := range m.users {
		cur := "  "
		if i == m.cursor {
			cur = cursorStyle.Render("> ")
		}
		flags := []string{u.Kind}
		if u.Premium {
			flags = append(flags, "premium")
		}
		if u.EmailVerified {
			flags = append(flags, "verified")
		}
		name := u.Name
		if u.Banned {
			name = warnStyle.Render(name + " [BANNED]")
		}
		fmt.Fprintf(&b, "%s%-20s %s\n", cur, name, dimStyle.Render(strings.Join(flags, " · ")))
	}
	return b.String(), "↑/↓ move · b ban/unban · r refresh · esc back"
}

func (m Model) viewSessions() (string, string) {
	var b strings.Builder
	b.WriteString(headStyle.Render(fmt.Sprintf("Live sessions (%d)", len(m.sessions))) + "\n\n")
	if len(m.sessions) == 0 {
		b.WriteString(dimStyle.Render("  (nobody connected)\n"))
	}
	for i, s := range m.sessions {
		cur := "  "
		if i == m.cursor {
			cur = cursorStyle.Render("> ")
		}
		who := s.User
		if s.ID == m.selfLive {
			who += " (you)"
		}
		age := time.Since(s.Start).Round(time.Second)
		fmt.Fprintf(&b, "%s%-18s %-8s %-16s %s\n", cur, who, s.Route, s.Remote, dimStyle.Render(age.String()))
	}
	return b.String(), "↑/↓ move · k disconnect · r refresh · esc back"
}

func (m Model) viewAudit() (string, string) {
	var b strings.Builder
	tabs := "[ admin actions ]  agent@ chats"
	if m.auditTab == 1 {
		tabs = "  admin actions   [ agent@ chats ]"
	}
	b.WriteString(headStyle.Render("Moderation & audit") + "  " + dimStyle.Render(tabs) + "\n\n")
	if m.auditTab == 0 {
		if len(m.actions) == 0 {
			b.WriteString(dimStyle.Render("  (no admin actions logged yet)\n"))
		}
		for _, a := range m.actions {
			line := fmt.Sprintf("  %s  %s %s", a.At.Local().Format("01-02 15:04"), a.Admin, a.Action)
			if a.Target != "" {
				line += " → " + a.Target
			}
			if a.Detail != "" {
				line += "  " + dimStyle.Render(a.Detail)
			}
			b.WriteString(line + "\n")
		}
	} else {
		if len(m.chats) == 0 {
			b.WriteString(dimStyle.Render("  (no agent@ messages yet)\n"))
		}
		for _, c := range m.chats {
			text := strings.ReplaceAll(c.Text, "\n", " ")
			if len(text) > 60 {
				text = text[:60] + "…"
			}
			fmt.Fprintf(&b, "  %s  %-12s %-5s %s\n",
				c.At.Local().Format("01-02 15:04"), c.Username, c.Role, dimStyle.Render(text))
		}
	}
	return b.String(), "tab switch view · r refresh · esc back · (ban from Users)"
}

func (m Model) viewConfig() (string, string) {
	var b strings.Builder
	b.WriteString(headStyle.Render("Runtime config") + "\n")
	pods := m.env.PodsEngine
	if pods == "" {
		pods = warnStyle.Render("disabled")
	}
	mail := "configured"
	if !m.env.MailConfigured {
		mail = warnStyle.Render("not configured")
	}
	admins := strings.Join(m.env.Admins, ", ")
	if admins == "" {
		admins = warnStyle.Render("(none — set AGENTBBS_ADMINS)")
	}
	fmt.Fprintf(&b, "  host       %s\n", m.env.Host)
	fmt.Fprintf(&b, "  sandbox    %s\n", m.env.Sandbox)
	fmt.Fprintf(&b, "  pods       %s\n", pods)
	fmt.Fprintf(&b, "  mail       %s\n", mail)
	fmt.Fprintf(&b, "  admins     %s\n", admins)

	b.WriteString("\n" + headStyle.Render("Plugins") + "\n\n")
	for i, p := range m.env.Plugins {
		cur := "  "
		if i == m.cursor {
			cur = cursorStyle.Render("> ")
		}
		state := okStyle.Render("on ")
		if m.disabled[p.ID] {
			state = warnStyle.Render("off")
		}
		fmt.Fprintf(&b, "%s[%s] %-12s %s\n", cur, state, p.ID, dimStyle.Render(p.Title))
	}
	if len(m.env.Plugins) == 0 {
		b.WriteString(dimStyle.Render("  (no plugins registered)\n"))
	}
	return b.String(), "↑/↓ move · space toggle · esc back"
}
