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
	modeCompose
)

// composeState holds an in-progress draft. Editing is intentionally simple
// (append + backspace at the end of each field), matching the byte-by-byte
// readLine philosophy in cmd/agentbbs — a full cursor editor is overkill for a
// BBS compose box.
type composeState struct {
	to, cc, subject, body string
	focus                 int // 0=to 1=cc 2=subject 3=body
	inReplyTo             string
	sending               bool
}

const composeFields = 4

type readerModel struct {
	c       *Client
	ctx     context.Context
	mailbox string

	mode    readerMode
	rows    []MessageSummary
	cursor  int
	current Message
	compose composeState
	status  string
	errText string
	width   int
	height  int
}

type rowsMsg struct{ rows []MessageSummary }
type openedMsg struct{ msg Message }
type actionDoneMsg struct{ status string }
type sentMsg struct{ status string }
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
	case sentMsg:
		m.compose = composeState{}
		m.mode = modeList
		m.status = msg.status
		return m, m.loadInbox()
	case errMsg:
		m.compose.sending = false
		m.errText = msg.err.Error()
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m readerModel) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.errText = ""
	if m.mode == modeCompose {
		return m.onComposeKey(k)
	}
	if m.mode == modeMessage {
		switch k.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "b", "esc", "left", "h":
			m.mode = modeList
			return m, m.loadInbox()
		case "r":
			m.startReply(false)
			return m, nil
		case "a":
			m.startReply(true)
			return m, nil
		case "c":
			m.startCompose()
			return m, nil
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
	case "c":
		m.startCompose()
		return m, nil
	case "enter", "l", "right":
		if len(m.rows) > 0 {
			return m, m.open(m.rows[m.cursor].UID)
		}
	}
	return m, nil
}

// startCompose opens a blank compose form.
func (m *readerModel) startCompose() {
	m.compose = composeState{focus: 0}
	m.mode = modeCompose
}

// startReply opens a compose form pre-filled from the open message. replyAll
// also carries the other recipients into Cc (minus the member's own address).
func (m *readerModel) startReply(replyAll bool) {
	orig := m.current
	to := orig.From
	if orig.ReplyTo != nil {
		to = *orig.ReplyTo
	}
	self := strings.ToLower(m.c.Address())
	var cc []string
	if replyAll {
		for _, a := range append(append([]Address{}, orig.To...), orig.CC...) {
			la := strings.ToLower(a.Address)
			if la != self && la != strings.ToLower(to.Address) {
				cc = append(cc, a.Address)
			}
		}
	}
	subject := orig.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	m.compose = composeState{
		to:        to.Address,
		cc:        strings.Join(cc, ", "),
		subject:   subject,
		body:      quoteBody(orig),
		focus:     3, // land in the body to type the reply
		inReplyTo: orig.MessageID,
	}
	m.mode = modeCompose
}

// onComposeKey edits the focused field. Tab/shift-tab cycle fields; ctrl+d sends;
// esc cancels. enter inserts a newline in the body and advances otherwise.
func (m readerModel) onComposeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.compose.sending {
		return m, nil // ignore input while the send is in flight
	}
	field := m.composeField()
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeList
		return m, m.loadInbox()
	case "ctrl+d":
		m.compose.sending = true
		m.status = "sending…"
		return m, m.sendCompose()
	case "tab", "down":
		m.compose.focus = (m.compose.focus + 1) % composeFields
		return m, nil
	case "shift+tab", "up":
		m.compose.focus = (m.compose.focus + composeFields - 1) % composeFields
		return m, nil
	case "enter":
		if m.compose.focus == 3 {
			*field += "\n"
		} else {
			m.compose.focus++
		}
		return m, nil
	case "backspace":
		if r := []rune(*field); len(r) > 0 {
			*field = string(r[:len(r)-1])
		}
		return m, nil
	default:
		if s := k.String(); len([]rune(s)) == 1 {
			*field += s
		} else if k.Type == tea.KeySpace {
			*field += " "
		}
		return m, nil
	}
}

// composeField returns a pointer to the currently focused field's text.
func (m *readerModel) composeField() *string {
	switch m.compose.focus {
	case 0:
		return &m.compose.to
	case 1:
		return &m.compose.cc
	case 2:
		return &m.compose.subject
	default:
		return &m.compose.body
	}
}

// sendCompose builds a Draft from the form and sends it via the client.
func (m readerModel) sendCompose() tea.Cmd {
	cs := m.compose
	return func() tea.Msg {
		d := Draft{
			To:        parseAddrList(cs.to),
			CC:        parseAddrList(cs.cc),
			Subject:   cs.subject,
			Text:      cs.body,
			InReplyTo: cs.inReplyTo,
		}
		if _, err := m.c.Send(m.ctx, d); err != nil {
			return errMsg{err}
		}
		return sentMsg{status: "sent → " + cs.to}
	}
}

// parseAddrList splits a comma-separated header value into addresses.
func parseAddrList(raw string) []Address {
	var out []Address
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, ParseAddress(p))
		}
	}
	return out
}

// quoteBody renders the original message as a quoted reply body.
func quoteBody(orig Message) string {
	var b strings.Builder
	b.WriteString("\n\nOn " + orig.Date.Format("2006-01-02 15:04") + ", " + FormatAddress(orig.From) + " wrote:\n")
	for _, line := range strings.Split(orig.Text, "\n") {
		b.WriteString("> " + line + "\n")
	}
	return b.String()
}

func (m readerModel) View() string {
	var b strings.Builder
	b.WriteString(mhTitle.Render("AgentMail") + mhDim.Render("  ·  "+m.c.Address()) + "\n\n")
	switch m.mode {
	case modeMessage:
		b.WriteString(m.viewMessage())
	case modeCompose:
		b.WriteString(m.viewCompose())
	default:
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
	b.WriteString("\n" + mhDim.Render("↑/↓ move · enter open · c compose · r refresh · q quit"))
	return b.String()
}

func (m readerModel) viewCompose() string {
	cs := m.compose
	var b strings.Builder
	title := "Compose"
	if cs.inReplyTo != "" {
		title = "Reply"
	}
	b.WriteString(mhUnseen.Render(title) + "\n\n")
	field := func(idx int, label, val string) {
		caret := "  "
		lbl := mhDim.Render(label)
		if cs.focus == idx {
			caret = mhCursor.Render("❯ ")
			lbl = mhTitle.Render(label)
			val += "▏"
		}
		b.WriteString(caret + lbl + val + "\n")
	}
	field(0, "To:      ", cs.to)
	field(1, "Cc:      ", cs.cc)
	field(2, "Subject: ", cs.subject)
	b.WriteString("\n")
	bodyLabel := mhDim.Render("Body:")
	if cs.focus == 3 {
		bodyLabel = mhTitle.Render("Body:")
	}
	b.WriteString(bodyLabel + "\n")
	body := cs.body
	if cs.focus == 3 {
		body += "▏"
	}
	b.WriteString(body + "\n")
	b.WriteString("\n" + mhDim.Render(m.status))
	b.WriteString("\n" + mhDim.Render("tab/↑↓ field · enter newline(body) · ctrl+d send · esc cancel"))
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
	b.WriteString("\n" + mhDim.Render("b back · r reply · a reply-all · c compose · f flag · x delete · q quit"))
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
