package files

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/store"
)

// NewAdminModel returns the operator management TUI for the SFTP server. Gate
// the route that launches it on the operator allowlist (auth.IsAdmin).
func NewAdminModel(svc *Service) tea.Model {
	m := admin{svc: svc}
	m.reload()
	return m
}

type adminTab int

const (
	tabSessions adminTab = iota
	tabWorkspaces
	tabPublic
	numTabs
)

var tabNames = []string{"Sessions", "Workspaces", "Public area"}

type admin struct {
	svc *Service
	tab adminTab
	sel int
	msg string

	sessions []Conn
	users    []store.User
	public   []Entry

	input   bool
	prompt  string
	buf     string
	onInput func(string)
}

func (m *admin) reload() {
	m.sessions = m.svc.Sessions()
	m.users, _ = m.svc.Users()
	m.public, _ = m.svc.PublicList()
	if m.sel < 0 {
		m.sel = 0
	}
}

func (m admin) Init() tea.Cmd { return nil }

func (m admin) rows() int {
	switch m.tab {
	case tabSessions:
		return len(m.sessions)
	case tabWorkspaces:
		return len(m.users)
	case tabPublic:
		return len(m.public)
	}
	return 0
}

func (m admin) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.input {
		return m.updateInput(key)
	}
	switch key.String() {
	case "q", "esc":
		return m, tea.Quit
	case "tab", "right", "l":
		m.tab = (m.tab + 1) % numTabs
		m.sel, m.msg = 0, ""
	case "shift+tab", "left", "h":
		m.tab = (m.tab + numTabs - 1) % numTabs
		m.sel, m.msg = 0, ""
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < m.rows()-1 {
			m.sel++
		}
	case "g":
		m.reload()
		m.msg = "refreshed"
	default:
		return m.action(key)
	}
	return m, nil
}

func (m admin) action(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.tab {
	case tabSessions:
		if key.String() == "x" {
			if c := m.curSession(); c != nil {
				if m.svc.Kick(c.ID) {
					m.msg = "disconnected " + c.User
				}
				m.reload()
			}
		}
	case tabWorkspaces:
		switch key.String() {
		case "Q":
			if u := m.curUser(); u != nil {
				uid := u.ID
				m.startInput("quota MB for "+u.Name+" (0 = default): ", func(s string) {
					mb, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
					if err != nil || mb < 0 {
						m.msg = warnStyle.Render("invalid number")
						return
					}
					if err := m.svc.SetQuota(uid, mb<<20); err != nil {
						m.msg = warnStyle.Render(err.Error())
						return
					}
					m.msg = fmt.Sprintf("quota set to %d MB", mb)
				})
			}
		case "x":
			if u := m.curUser(); u != nil {
				fa, _ := m.svc.Access(u.ID)
				if err := m.svc.SetRevoked(u.ID, !fa.Revoked); err != nil {
					m.msg = warnStyle.Render(err.Error())
				} else if fa.Revoked {
					m.msg = "restored SFTP for " + u.Name
				} else {
					m.msg = "revoked SFTP for " + u.Name
				}
			}
		}
	case tabPublic:
		switch key.String() {
		case "t":
			on := !m.svc.PublicWritable()
			if err := m.svc.SetPublicWrite(on); err != nil {
				m.msg = warnStyle.Render(err.Error())
			} else if on {
				m.msg = "public area is now writable (members)"
			} else {
				m.msg = "public area is now read-only"
			}
		case "x":
			if e := m.curPublic(); e != nil {
				name := e.Name
				if err := m.svc.PublicRemove(name); err != nil {
					m.msg = warnStyle.Render(err.Error())
				} else {
					m.msg = "removed " + name
				}
				m.reload()
			}
		}
	}
	return m, nil
}

func (m admin) updateInput(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEnter:
		m.input = false
		if m.onInput != nil {
			m.onInput(m.buf)
		}
		m.buf = ""
		m.reload()
	case tea.KeyEsc:
		m.input, m.buf = false, ""
	case tea.KeyBackspace:
		if m.buf != "" {
			m.buf = m.buf[:len(m.buf)-1]
		}
	case tea.KeyRunes, tea.KeySpace:
		m.buf += string(key.Runes)
	}
	return m, nil
}

func (m *admin) startInput(prompt string, cb func(string)) {
	m.input, m.prompt, m.buf, m.onInput = true, prompt, "", cb
}

func (m admin) curSession() *Conn {
	if m.tab != tabSessions || m.sel >= len(m.sessions) {
		return nil
	}
	return &m.sessions[m.sel]
}
func (m admin) curUser() *store.User {
	if m.tab != tabWorkspaces || m.sel >= len(m.users) {
		return nil
	}
	return &m.users[m.sel]
}
func (m admin) curPublic() *Entry {
	if m.tab != tabPublic || m.sel >= len(m.public) {
		return nil
	}
	return &m.public[m.sel]
}

func (m admin) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("SFTP server — management") + "\n")

	var tabs []string
	for i, name := range tabNames {
		if adminTab(i) == m.tab {
			tabs = append(tabs, selStyle.Render(" "+name+" "))
		} else {
			tabs = append(tabs, dimStyle.Render(" "+name+" "))
		}
	}
	b.WriteString(strings.Join(tabs, " ") + "\n\n")

	switch m.tab {
	case tabSessions:
		b.WriteString(m.viewSessions())
	case tabWorkspaces:
		b.WriteString(m.viewWorkspaces())
	case tabPublic:
		b.WriteString(m.viewPublic())
	}

	b.WriteString("\n")
	if m.input {
		b.WriteString(titleStyle.Render(m.prompt) + m.buf + "_\n")
	} else {
		if m.msg != "" {
			b.WriteString(m.msg + "\n")
		}
		b.WriteString(dimStyle.Render(m.help()))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

func (m admin) help() string {
	common := "tab switch · ↑/↓ move · g refresh · q quit"
	switch m.tab {
	case tabSessions:
		return "x disconnect · " + common
	case tabWorkspaces:
		return "Q set quota · x revoke/restore · " + common
	case tabPublic:
		return "t toggle write · x remove entry · " + common
	}
	return common
}

func (m admin) viewSessions() string {
	if len(m.sessions) == 0 {
		return dimStyle.Render("  no live SFTP connections")
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %-16s %-22s %10s %10s %8s\n", "user", "remote", "rx", "tx", "idle")))
	for i, c := range m.sessions {
		line := fmt.Sprintf("  %-16s %-22s %10s %10s %8s",
			c.User, c.Remote, humanBytes(c.RX), humanBytes(c.TX), since(c.Started))
		b.WriteString(rowStyle(i == m.sel).Render(line) + "\n")
	}
	return b.String()
}

func (m admin) viewWorkspaces() string {
	if len(m.users) == 0 {
		return dimStyle.Render("  no members")
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %-16s %10s %10s %6s %s\n", "user", "used", "quota", "use%", "access")))
	for i, u := range m.users {
		usage, _ := m.svc.Usage(u)
		fa, _ := m.svc.Access(u.ID)
		access := "ok"
		if fa.Revoked {
			access = warnStyle.Render("revoked")
		}
		line := fmt.Sprintf("  %-16s %10s %10s %5d%% %s",
			u.Name, humanBytes(usage.Bytes), humanBytes(usage.Quota), pct(usage.Bytes, usage.Quota), access)
		b.WriteString(rowStyle(i == m.sel).Render(line) + "\n")
	}
	return b.String()
}

func (m admin) viewPublic() string {
	state := "writable (members)"
	if !m.svc.PublicWritable() {
		state = "read-only"
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render("  public-area write: ") + state + "\n\n")
	if len(m.public) == 0 {
		b.WriteString(dimStyle.Render("  (empty)"))
		return b.String()
	}
	for i, e := range m.public {
		name := e.Name
		meta := humanBytes(e.Size)
		if e.IsDir {
			name += "/"
			meta = "dir"
		}
		b.WriteString(rowStyle(i == m.sel).Render(fmt.Sprintf("  %-28s %8s", name, meta)) + "\n")
	}
	return b.String()
}

func rowStyle(selected bool) lipgloss.Style {
	if selected {
		return selStyle
	}
	return lipgloss.NewStyle()
}

func since(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
