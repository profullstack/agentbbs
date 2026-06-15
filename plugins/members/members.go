// Package members is the member directory + messaging plugin (the BBS "who" and
// store-and-forward inbox). Members browse who else has an account, see who is
// online now, finger a profile, leave a message, and read their own inbox.
//
// It is members-only (RequiresAuth) — guests have no identity to send from or
// receive to. Messaging is store-and-forward via the store's messages table;
// the same inbox is fed by the `ssh msg@host <user>` CLI route.
package members

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
	"github.com/profullstack/agentbbs/internal/store"
)

type Plugin struct{}

func (Plugin) ID() string          { return "members" }
func (Plugin) Title() string       { return "Members" }
func (Plugin) Description() string { return "Who's here · finger a profile · leave a message · inbox" }
func (Plugin) RequiresAuth() bool  { return true }

func (Plugin) New(user auth.User, ctx plugin.Context) tea.Model {
	return &model{user: user, ctx: ctx, state: stList}
}

// state is which sub-screen is showing.
type state int

const (
	stList state = iota
	stProfile
	stCompose
	stInbox
)

// person is one directory row.
type person struct {
	name     string
	kind     string
	online   bool
	lastSeen time.Time
	seenOK   bool
}

type model struct {
	user  auth.User
	ctx   plugin.Context
	state state

	people []person
	inbox  []store.Message
	cursor int // list/inbox cursor
	target string // who we're fingering/composing to

	draft string // compose buffer
	note  string // transient status line
	err   error

	width, height int
}

// --- loading ---------------------------------------------------------------

type loadedMsg struct {
	people []person
	err    error
}

func (m *model) load() tea.Cmd {
	st := m.ctx.Store
	me := m.user.Name
	return func() tea.Msg {
		users, err := st.ListUsers(500)
		if err != nil {
			return loadedMsg{err: err}
		}
		online, _ := st.OnlineUsers()
		out := make([]person, 0, len(users))
		for _, u := range users {
			if u.Name == me {
				continue // don't list yourself in the directory
			}
			p := person{name: u.Name, kind: u.Kind, online: online[strings.ToLower(u.Name)]}
			if t, ok, _ := st.LastSeen(u.ID); ok {
				p.lastSeen, p.seenOK = t, true
			}
			out = append(out, p)
		}
		// Online first, then most-recently-seen, then name.
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].online != out[j].online {
				return out[i].online
			}
			if out[i].seenOK != out[j].seenOK {
				return out[i].seenOK
			}
			if out[i].seenOK && !out[i].lastSeen.Equal(out[j].lastSeen) {
				return out[i].lastSeen.After(out[j].lastSeen)
			}
			return out[i].name < out[j].name
		})
		return loadedMsg{people: out}
	}
}

type inboxMsg struct {
	msgs []store.Message
	err  error
}

func (m *model) loadInbox() tea.Cmd {
	st := m.ctx.Store
	me := m.user.Name
	return func() tea.Msg {
		msgs, err := st.Inbox(me, 100)
		if err != nil {
			return inboxMsg{err: err}
		}
		// Opening the inbox marks everything read.
		var unread []int64
		for _, mm := range msgs {
			if !mm.Read {
				unread = append(unread, mm.ID)
			}
		}
		_ = st.MarkRead(me, unread)
		return inboxMsg{msgs: msgs}
	}
}

type sentMsg struct{ err error }

func (m *model) send(to, body string) tea.Cmd {
	st := m.ctx.Store
	from := m.user.Name
	return func() tea.Msg { return sentMsg{err: st.SendMessage(from, to, body)} }
}

func (m *model) Init() tea.Cmd { return m.load() }

// --- update ----------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case loadedMsg:
		m.people, m.err = msg.people, msg.err
		if m.cursor >= len(m.people) {
			m.cursor = 0
		}
		return m, nil
	case inboxMsg:
		m.inbox, m.err = msg.msgs, msg.err
		return m, nil
	case sentMsg:
		if msg.err != nil {
			m.note = "send failed: " + msg.err.Error()
		} else {
			m.note = "✓ message sent to " + m.target
			m.state = stProfile
		}
		m.draft = ""
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.state == stCompose {
		return m.composeKey(k)
	}
	m.note = ""
	switch m.state {
	case stList:
		switch k.String() {
		case "q", "esc":
			return m, plugin.Exit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.people)-1 {
				m.cursor++
			}
		case "i":
			m.state = stInbox
			m.cursor = 0
			return m, m.loadInbox()
		case "r":
			return m, m.load()
		case "enter":
			if p := m.selected(); p != nil {
				m.target = p.name
				m.state = stProfile
			}
		case "m":
			if p := m.selected(); p != nil {
				m.target = p.name
				m.draft = ""
				m.state = stCompose
			}
		}
	case stProfile:
		switch k.String() {
		case "q", "esc", "backspace":
			m.state = stList
		case "m":
			m.draft = ""
			m.state = stCompose
		}
	case stInbox:
		switch k.String() {
		case "q", "esc", "backspace":
			m.state = stList
			return m, m.load() // refresh unread badge state
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.inbox)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

// composeKey runs the minimal one-line message editor.
func (m *model) composeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.state = stProfile
		m.draft = ""
		return m, nil
	case tea.KeyEnter:
		body := strings.TrimSpace(m.draft)
		if body == "" {
			m.note = "type a message first (esc to cancel)"
			return m, nil
		}
		return m, m.send(m.target, body)
	case tea.KeyBackspace, tea.KeyDelete:
		if n := len(m.draft); n > 0 {
			r := []rune(m.draft)
			m.draft = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeySpace:
		m.draft += " "
		return m, nil
	case tea.KeyRunes:
		m.draft += string(k.Runes)
		return m, nil
	}
	return m, nil
}

func (m *model) selected() *person {
	if m.cursor < 0 || m.cursor >= len(m.people) {
		return nil
	}
	return &m.people[m.cursor]
}

// --- view ------------------------------------------------------------------

var (
	hdr   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	dim   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	sel   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e2e8f0"))
	on    = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ade80"))
	off   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	warn  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	cur   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	frame = lipgloss.NewStyle().Padding(1, 2)
)

func (m *model) View() string {
	var s string
	switch m.state {
	case stProfile:
		s = m.profileView()
	case stCompose:
		s = m.composeView()
	case stInbox:
		s = m.inboxView()
	default:
		s = m.listView()
	}
	if m.note != "" {
		s += "\n" + warn.Render(m.note)
	}
	return frame.Render(s)
}

func (m *model) listView() string {
	s := hdr.Render("Members") + dim.Render("  ·  who's here") + "\n\n"
	if m.err != nil {
		return s + warn.Render("error: "+m.err.Error())
	}
	if len(m.people) == 0 {
		return s + dim.Render("no members yet")
	}
	for i, p := range m.people {
		dot := off.Render("○")
		if p.online {
			dot = on.Render("●")
		}
		name := p.name
		c := "  "
		if i == m.cursor {
			c = cur.Render("❯ ")
			name = sel.Render(name)
		}
		seen := "online"
		if !p.online {
			seen = "last " + relTime(p.lastSeen, p.seenOK)
		}
		row := fmt.Sprintf("%s%s %-20s %-8s %s", c, dot, name, p.kind, dim.Render(seen))
		s += row + "\n"
	}
	s += "\n" + dim.Render("↑/↓ move · enter finger · m message · i inbox · r refresh · q back")
	return s
}

func (m *model) profileView() string {
	p := m.find(m.target)
	s := hdr.Render("finger "+m.target) + "\n\n"
	if p == nil {
		return s + dim.Render("unknown member")
	}
	status := off.Render("offline") + dim.Render(" · last "+relTime(p.lastSeen, p.seenOK))
	if p.online {
		status = on.Render("online now")
	}
	home := "~" + p.name
	if m.ctx.Host != "" {
		home = "https://" + m.ctx.Host + "/~" + p.name
	}
	lines := []string{
		"  Login:  " + sel.Render(p.name) + "    Kind: " + p.kind,
		"  Status: " + status,
		"  Home:   " + dim.Render(home),
	}
	s += strings.Join(lines, "\n")
	s += "\n\n" + dim.Render("m message "+p.name+" · esc back")
	return s
}

func (m *model) composeView() string {
	s := hdr.Render("message "+m.target) + "\n\n"
	s += dim.Render("from "+m.user.Name+" → "+m.target) + "\n\n"
	s += "  " + m.draft + cur.Render("▏") + "\n\n"
	s += dim.Render("enter send · esc cancel")
	return s
}

func (m *model) inboxView() string {
	s := hdr.Render("Inbox") + dim.Render("  ·  "+m.user.Name) + "\n\n"
	if m.err != nil {
		return s + warn.Render("error: "+m.err.Error())
	}
	if len(m.inbox) == 0 {
		return s + dim.Render("no messages — select a member and press m to send one") +
			"\n\n" + dim.Render("esc back")
	}
	for i, msg := range m.inbox {
		c := "  "
		from := msg.From
		if i == m.cursor {
			c = cur.Render("❯ ")
			from = sel.Render(from)
		}
		s += fmt.Sprintf("%s%-16s %s\n", c, from, dim.Render(relTime(msg.At, true)+" ago"))
		s += "    " + msg.Body + "\n"
	}
	s += "\n" + dim.Render("↑/↓ scroll · esc back")
	return s
}

func (m *model) find(name string) *person {
	for i := range m.people {
		if m.people[i].name == name {
			return &m.people[i]
		}
	}
	return nil
}

// relTime renders a coarse "2h", "3d", "just now" style age. ok=false → "never".
func relTime(t time.Time, ok bool) string {
	if !ok || t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
