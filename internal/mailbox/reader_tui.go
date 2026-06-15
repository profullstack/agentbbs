package mailbox

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
)

var (
	mhTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	mhDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	mhCursor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	mhUnseen = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e2e8f0"))
	mhFlag   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	mhErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	mhFrame  = lipgloss.NewStyle().Padding(1, 2)
)

// RunReader runs the interactive mail TUI for a member on the session. Used by
// the hub "Mail" entry and the ssh mail@ route (with a PTY).
func RunReader(s ssh.Session, c *Client) error {
	m := readerModel{c: c, ctx: s.Context(), mailbox: Inbox, status: "loading…"}
	p := tea.NewProgram(m, tea.WithInput(s), tea.WithOutput(s), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type readerMode int

const (
	modeList readerMode = iota
	modeMessage
)

type readerModel struct {
	c       *Client
	ctx     context.Context
	mailbox string

	mode    readerMode
	rows    []MessageSummary
	cursor  int
	current Message
	status  string
	errText string
	width   int
	height  int
}

type rowsMsg struct{ rows []MessageSummary }
type openedMsg struct{ msg Message }
type actionDoneMsg struct{ status string }
type errMsg struct{ err error }

func (m readerModel) Init() tea.Cmd { return m.loadInbox() }

func (m readerModel) loadInbox() tea.Cmd {
	return func() tea.Msg {
		rows, err := m.c.List(m.ctx, m.mailbox, 0)
		if err != nil {
			return errMsg{err}
		}
		return rowsMsg{rows}
	}
}

func (m readerModel) open(uid uint32) tea.Cmd {
	return func() tea.Msg {
		msg, ok, err := m.c.Read(m.ctx, m.mailbox, uid, false)
		if err != nil {
			return errMsg{err}
		}
		if !ok {
			return errMsg{notFound(m.mailbox, uid)}
		}
		return openedMsg{msg}
	}
}

func (m readerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case rowsMsg:
		m.rows = msg.rows
		if m.cursor >= len(m.rows) {
			m.cursor = max(0, len(m.rows)-1)
		}
		m.status = fmt.Sprintf("%s — %d message(s)", m.mailbox, len(m.rows))
	case openedMsg:
		m.current = msg.msg
		m.mode = modeMessage
	case actionDoneMsg:
		m.status = msg.status
		return m, m.loadInbox()
	case errMsg:
		m.errText = msg.err.Error()
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m readerModel) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.errText = ""
	if m.mode == modeMessage {
		switch k.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "b", "esc", "left", "h":
			m.mode = modeList
			return m, m.loadInbox()
		case "f":
			uid := m.current.UID
			flagged := !m.current.Flagged
			return m, func() tea.Msg {
				if err := m.c.Flag(m.ctx, m.mailbox, uid, flagged); err != nil {
					return errMsg{err}
				}
				return actionDoneMsg{status: flagState(flagged)}
			}
		case "x", "d":
			uid := m.current.UID
			m.mode = modeList
			return m, func() tea.Msg {
				if err := m.c.Delete(m.ctx, m.mailbox, uid); err != nil {
					return errMsg{err}
				}
				return actionDoneMsg{status: "deleted"}
			}
		}
		return m, nil
	}

	switch k.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "r":
		m.status = "refreshing…"
		return m, m.loadInbox()
	case "enter", "l", "right":
		if len(m.rows) > 0 {
			return m, m.open(m.rows[m.cursor].UID)
		}
	}
	return m, nil
}

func (m readerModel) View() string {
	var b strings.Builder
	b.WriteString(mhTitle.Render("AgentMail") + mhDim.Render("  ·  "+m.c.Address()) + "\n\n")
	if m.mode == modeMessage {
		b.WriteString(m.viewMessage())
	} else {
		b.WriteString(m.viewList())
	}
	if m.errText != "" {
		b.WriteString("\n" + mhErr.Render(m.errText))
	}
	return mhFrame.Render(b.String())
}

func (m readerModel) viewList() string {
	var b strings.Builder
	if len(m.rows) == 0 {
		b.WriteString(mhDim.Render("(no messages)") + "\n")
	}
	for i, r := range m.rows {
		cur := "  "
		if i == m.cursor {
			cur = mhCursor.Render("❯ ")
		}
		marker := " "
		if !r.Seen {
			marker = "●"
		}
		flag := " "
		if r.Flagged {
			flag = mhFlag.Render("⚑")
		}
		from := r.From.Name
		if from == "" {
			from = r.From.Address
		}
		line := fmt.Sprintf("%s%s%s %-22.22s %s", cur, marker, flag, from, r.Subject)
		if !r.Seen {
			line = mhUnseen.Render(line)
		}
		b.WriteString(line + "\n")
		b.WriteString("    " + mhDim.Render(r.Date.Format("2006-01-02 15:04")+"  ·  "+r.Snippet) + "\n")
	}
	b.WriteString("\n" + mhDim.Render(m.status))
	b.WriteString("\n" + mhDim.Render("↑/↓ move · enter open · r refresh · q quit"))
	return b.String()
}

func (m readerModel) viewMessage() string {
	msg := m.current
	var b strings.Builder
	b.WriteString(mhDim.Render("From:    ") + FormatAddress(msg.From) + "\n")
	b.WriteString(mhDim.Render("To:      ") + joinAddrs(msg.To) + "\n")
	if len(msg.CC) > 0 {
		b.WriteString(mhDim.Render("Cc:      ") + joinAddrs(msg.CC) + "\n")
	}
	b.WriteString(mhDim.Render("Date:    ") + msg.Date.Format("2006-01-02 15:04") + "\n")
	b.WriteString(mhDim.Render("Subject: ") + mhUnseen.Render(msg.Subject) + "\n")
	if len(msg.Attachments) > 0 {
		names := make([]string, len(msg.Attachments))
		for i, a := range msg.Attachments {
			names[i] = a.Filename
		}
		b.WriteString(mhDim.Render("Attach:  ") + strings.Join(names, ", ") + "\n")
	}
	b.WriteString("\n" + msg.Text + "\n")
	b.WriteString("\n" + mhDim.Render("b back · f flag · x delete · q quit"))
	return b.String()
}

func joinAddrs(list []Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = FormatAddress(a)
	}
	return strings.Join(parts, ", ")
}

func flagState(on bool) string {
	if on {
		return "flagged"
	}
	return "unflagged"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
