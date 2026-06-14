package news

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/dustin/go-nntp"
)

var (
	nTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#c084fc"))
	nSel   = lipgloss.NewStyle().Foreground(lipgloss.Color("#0b1020")).Background(lipgloss.Color("#38bdf8"))
	nMeta  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	nFrom  = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ade80"))
	nErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	nHint  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

// RunReader connects the member to the loopback NNTP server and drives the
// newsreader TUI over the SSH session until they leave.
func RunReader(s ssh.Session, addr, user string) error {
	ptyReq, winCh, hasPty := s.Pty()
	if !hasPty {
		_, _ = s.Write([]byte("news needs a terminal (ssh -t news@<host>)\r\n"))
		return nil
	}
	r, err := Dial(addr, user)
	if err != nil {
		return err
	}
	m := &model{r: r, width: ptyReq.Window.Width, height: ptyReq.Window.Height, mode: modeGroups}
	p := tea.NewProgram(m, tea.WithInput(s), tea.WithOutput(s), tea.WithAltScreen())
	go func() {
		for w := range winCh {
			p.Send(tea.WindowSizeMsg{Width: w.Width, Height: w.Height})
		}
	}()
	_, runErr := p.Run()
	_ = r.Close()
	return runErr
}

type mode int

const (
	modeGroups mode = iota
	modeThreads
	modeArticle
	modeCompose
)

type model struct {
	r    *Reader
	mode mode

	groups []nntp.Group
	gSel   int

	group   nntp.Group
	threads []Overview
	tSel    int

	article string
	aScroll int

	// compose
	cField   int // 0=subject, 1=body
	cSubject string
	cBody    string
	cRefs    string

	status        string
	width, height int
}

// async message types
type groupsMsg struct {
	groups []nntp.Group
	err    error
}
type threadsMsg struct {
	group   nntp.Group
	threads []Overview
	err     error
}
type articleMsg struct {
	text string
	err  error
}
type postedMsg struct{ err error }

func (m *model) Init() tea.Cmd { return m.loadGroups }

func (m *model) loadGroups() tea.Msg {
	gs, err := m.r.Groups()
	return groupsMsg{groups: gs, err: err}
}

func (m *model) loadThreads(name string) tea.Cmd {
	return func() tea.Msg {
		g, err := m.r.Select(name)
		if err != nil {
			return threadsMsg{err: err}
		}
		ov, err := m.r.Overview(g.Low, g.High)
		return threadsMsg{group: g, threads: ov, err: err}
	}
}

func (m *model) loadArticle(num int64) tea.Cmd {
	return func() tea.Msg {
		txt, err := m.r.Article(num)
		return articleMsg{text: txt, err: err}
	}
}

func (m *model) submitPost() tea.Cmd {
	group, subject, refs, body := m.group.Name, strings.TrimSpace(m.cSubject), m.cRefs, m.cBody
	return func() tea.Msg {
		if subject == "" {
			subject = "(no subject)"
		}
		return postedMsg{err: m.r.Post(group, subject, refs, body)}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case groupsMsg:
		if msg.err != nil {
			m.status = "groups: " + msg.err.Error()
		} else {
			m.groups = msg.groups
		}
	case threadsMsg:
		if msg.err != nil {
			m.status = "open group: " + msg.err.Error()
			m.mode = modeGroups
		} else {
			m.group, m.threads, m.tSel, m.mode = msg.group, msg.threads, 0, modeThreads
		}
	case articleMsg:
		if msg.err != nil {
			m.status = "open article: " + msg.err.Error()
		} else {
			m.article, m.aScroll, m.mode = msg.text, 0, modeArticle
		}
	case postedMsg:
		if msg.err != nil {
			m.status = "post failed: " + msg.err.Error()
			return m, nil
		}
		m.status = "posted to " + m.group.Name
		m.mode = modeThreads
		return m, m.loadThreads(m.group.Name)
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeCompose {
		return m.composeKey(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	}
	switch m.mode {
	case modeGroups:
		switch msg.String() {
		case "q", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.gSel > 0 {
				m.gSel--
			}
		case "down", "j":
			if m.gSel < len(m.groups)-1 {
				m.gSel++
			}
		case "enter", "right", "l":
			if len(m.groups) > 0 {
				return m, m.loadThreads(m.groups[m.gSel].Name)
			}
		}
	case modeThreads:
		switch msg.String() {
		case "q", "esc", "left", "h":
			m.mode = modeGroups
		case "up", "k":
			if m.tSel > 0 {
				m.tSel--
			}
		case "down", "j":
			if m.tSel < len(m.threads)-1 {
				m.tSel++
			}
		case "enter", "right", "l":
			if len(m.threads) > 0 {
				return m, m.loadArticle(m.threads[m.tSel].Num)
			}
		case "p":
			m.startCompose("", "")
		}
	case modeArticle:
		switch msg.String() {
		case "q", "esc", "left", "h":
			m.mode = modeThreads
		case "up", "k":
			if m.aScroll > 0 {
				m.aScroll--
			}
		case "down", "j":
			m.aScroll++
		case "r":
			cur := m.threads[m.tSel]
			subj := cur.Subject
			if !strings.HasPrefix(strings.ToLower(subj), "re:") {
				subj = "Re: " + subj
			}
			refs := strings.TrimSpace(cur.Refs + " " + cur.MsgID)
			m.startCompose(subj, refs)
		}
	}
	return m, nil
}

func (m *model) startCompose(subject, refs string) {
	m.mode = modeCompose
	m.cField = 0
	m.cSubject = subject
	m.cBody = ""
	m.cRefs = refs
}

func (m *model) composeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeThreads
		m.status = "compose cancelled"
		return m, nil
	case "ctrl+s":
		return m, m.submitPost()
	case "tab":
		m.cField = (m.cField + 1) % 2
		return m, nil
	}
	if m.cField == 0 { // subject
		switch msg.String() {
		case "enter":
			m.cField = 1
		case "backspace":
			if len(m.cSubject) > 0 {
				m.cSubject = m.cSubject[:len(m.cSubject)-1]
			}
		default:
			if msg.Type == tea.KeyRunes || msg.String() == " " {
				m.cSubject += string(msg.Runes)
			}
		}
		return m, nil
	}
	// body
	switch msg.String() {
	case "enter":
		m.cBody += "\n"
	case "backspace":
		if len(m.cBody) > 0 {
			m.cBody = m.cBody[:len(m.cBody)-1]
		}
	default:
		if msg.Type == tea.KeyRunes || msg.String() == " " {
			m.cBody += string(msg.Runes)
		}
	}
	return m, nil
}

func (m *model) View() string {
	switch m.mode {
	case modeGroups:
		return m.viewGroups()
	case modeThreads:
		return m.viewThreads()
	case modeArticle:
		return m.viewArticle()
	case modeCompose:
		return m.viewCompose()
	}
	return ""
}

func (m *model) rows() int {
	r := m.height - 4
	if r < 3 {
		r = 3
	}
	return r
}

func (m *model) frame(header, body, hint string) string {
	status := ""
	if m.status != "" {
		status = "\n" + nMeta.Render(m.status)
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(
		nTitle.Render(header) + "\n\n" + body + status + "\n\n" + nHint.Render(hint))
}

func (m *model) viewGroups() string {
	var b strings.Builder
	for i, g := range m.groups {
		line := fmt.Sprintf("%-24s %5d articles", g.Name, g.High)
		if i == m.gSel {
			line = nSel.Render("› " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	if len(m.groups) == 0 {
		b.WriteString(nMeta.Render("no groups yet"))
	}
	return m.frame("news@ "+m.r.User()+" — newsgroups", b.String(),
		"↑/↓ move · enter open · q quit")
}

func (m *model) viewThreads() string {
	var b strings.Builder
	start, rows := scrollStart(m.tSel, len(m.threads), m.rows())
	for i := start; i < len(m.threads) && i < start+rows; i++ {
		t := m.threads[i]
		line := fmt.Sprintf("%-50s %s", truncate(t.Subject, 50), nFrom.Render(shortFrom(t.From)))
		if i == m.tSel {
			line = nSel.Render("› ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	if len(m.threads) == 0 {
		b.WriteString(nMeta.Render("no articles — press p to post the first one"))
	}
	return m.frame("news@ — "+m.group.Name, b.String(),
		"↑/↓ move · enter read · p post · esc groups")
}

func (m *model) viewArticle() string {
	lines := strings.Split(strings.ReplaceAll(m.article, "\r\n", "\n"), "\n")
	rows := m.rows()
	if m.aScroll > len(lines)-1 {
		m.aScroll = max(0, len(lines)-1)
	}
	end := m.aScroll + rows
	if end > len(lines) {
		end = len(lines)
	}
	body := strings.Join(lines[m.aScroll:end], "\n")
	return m.frame("news@ — "+m.group.Name, body,
		"↑/↓ scroll · r reply · esc back")
}

func (m *model) viewCompose() string {
	subjLabel, bodyLabel := "  Subject: ", "  Body:"
	if m.cField == 0 {
		subjLabel = nSel.Render("› Subject: ")
	} else {
		bodyLabel = nSel.Render("› Body:")
	}
	cursor := ""
	if m.cField == 0 {
		cursor = "█"
	}
	body := subjLabel + m.cSubject + cursor + "\n" + bodyLabel + "\n"
	bodyText := m.cBody
	if m.cField == 1 {
		bodyText += "█"
	}
	body += indent(bodyText)
	header := "compose → " + m.group.Name
	if m.cRefs != "" {
		header = "reply → " + m.group.Name
	}
	return m.frame(header, body, "tab switch field · ctrl+s send · esc cancel")
}

func scrollStart(sel, n, rows int) (int, int) {
	if n <= rows {
		return 0, rows
	}
	start := sel - rows/2
	if start < 0 {
		start = 0
	}
	if start > n-rows {
		start = n - rows
	}
	return start, rows
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// shortFrom renders just the display name (or local part) of a From header.
func shortFrom(from string) string {
	if i := strings.IndexByte(from, '<'); i > 0 {
		return strings.TrimSpace(from[:i])
	}
	if i := strings.IndexByte(from, '@'); i > 0 {
		return from[:i]
	}
	return from
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		b.WriteString("  " + l + "\n")
	}
	return b.String()
}
