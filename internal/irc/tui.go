package irc

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
)

const historyLines = 500

// DefaultChannel is joined automatically when a member enters via irc@.
const DefaultChannel = "#lobby"

var (
	cTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#c084fc"))
	cNick  = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ade80"))
	cSelf  = lipgloss.NewStyle().Foreground(lipgloss.Color("#38bdf8"))
	cSys   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	cNote  = lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24"))
	cErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// Run drives the IRC TUI over the SSH session until the member leaves; leaving
// ends the session (irc@ is a dedicated route, like agent@). canCreate gates the
// /create command on premium membership.
func Run(s ssh.Session, c *Client, canCreate bool) error {
	ptyReq, winCh, hasPty := s.Pty()
	if !hasPty {
		_, _ = s.Write([]byte("irc needs a terminal (ssh -t irc@<host>)\r\n"))
		return nil
	}
	m := &model{
		c:         c,
		channel:   DefaultChannel,
		canCreate: canCreate,
		width:     ptyReq.Window.Width,
		height:    ptyReq.Window.Height,
	}
	m.lines = append(m.lines,
		cSys.Render(fmt.Sprintf("connected as %s — joined %s. /help for commands, esc to leave.", c.Nick(), DefaultChannel)))
	p := tea.NewProgram(m, tea.WithInput(s), tea.WithOutput(s), tea.WithAltScreen())
	go func() {
		for w := range winCh {
			p.Send(tea.WindowSizeMsg{Width: w.Width, Height: w.Height})
		}
	}()
	_, err := p.Run()
	_ = c.Close()
	return err
}

// waitEvent blocks on the next connection event and delivers it to the model.
func waitEvent(c *Client) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-c.Events()
		if !ok {
			return Event{Kind: EvClosed, Text: "disconnected"}
		}
		return e
	}
}

type model struct {
	c         *Client
	channel   string // current conversation target for typed lines
	canCreate bool   // premium members may /create (register) new channels
	lines     []string
	input     string

	width, height int
}

func (m *model) Init() tea.Cmd { return waitEvent(m.c) }

func (m *model) push(line string) {
	m.lines = append(m.lines, line)
	if len(m.lines) > historyLines {
		m.lines = m.lines[len(m.lines)-historyLines:]
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case Event:
		return m.handleEvent(msg)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			return m.handleInput()
		case "backspace":
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		default:
			if msg.Type == tea.KeyRunes || msg.String() == " " {
				m.input += string(msg.Runes)
			}
		}
	}
	return m, nil
}

func (m *model) handleEvent(e Event) (tea.Model, tea.Cmd) {
	switch e.Kind {
	case EvMessage:
		where := ""
		if !strings.HasPrefix(e.Channel, "#") { // a direct message to us
			where = cNote.Render("[dm] ")
		}
		m.push(where + cNick.Render(e.Nick) + " " + e.Text)
	case EvNotice:
		m.push(cNote.Render("-"+e.Nick+"- ") + e.Text)
	case EvJoin:
		m.push(cSys.Render(fmt.Sprintf("→ %s joined %s", e.Nick, e.Channel)))
	case EvPart:
		m.push(cSys.Render(fmt.Sprintf("← %s left %s %s", e.Nick, e.Channel, e.Text)))
	case EvQuit:
		m.push(cSys.Render(fmt.Sprintf("← %s quit (%s)", e.Nick, e.Text)))
	case EvNick:
		m.push(cSys.Render(fmt.Sprintf("* %s is now %s", e.Nick, e.Text)))
	case EvSystem:
		if e.Text != "" {
			m.push(cSys.Render(e.Text))
		}
	case EvClosed:
		m.push(cErr.Render("* connection closed: " + e.Text))
		return m, tea.Quit
	}
	return m, waitEvent(m.c)
}

func (m *model) handleInput() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input)
	m.input = ""
	if text == "" {
		return m, nil
	}
	if strings.HasPrefix(text, "/") {
		return m, m.command(text)
	}
	// plain line → message to the current channel
	if err := m.c.Privmsg(m.channel, text); err != nil {
		m.push(cErr.Render("send failed: " + err.Error()))
		return m, nil
	}
	m.push(cSelf.Render(m.c.Nick()) + " " + text)
	return m, nil
}

// command handles the small set of /-commands the TUI models; anything else is
// passed through raw so power users can drive the server directly.
func (m *model) command(text string) tea.Cmd {
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	switch cmd {
	case "/help":
		m.push(cSys.Render("commands: /join #chan  /part [#chan]  /create #chan  /msg <nick> <text>  /me <action>  /names  /nick <name>  /quit"))
	case "/create":
		if !m.canCreate {
			m.push(cErr.Render("creating channels is a Founding Lifetime Member perk — upgrade with: ssh join@ (the BBS). You can still /join existing channels."))
			break
		}
		ch := arg
		if ch == "" {
			m.push(cErr.Render("usage: /create #channel"))
			break
		}
		if !strings.HasPrefix(ch, "#") {
			ch = "#" + ch
		}
		// operator-only-creation is off, so joining a fresh channel creates it and
		// ops the creator; registering it with ChanServ makes it persist with you
		// as founder. (Both run in order on this connection.)
		_ = m.c.Join(ch)
		_ = m.c.Privmsg("ChanServ", "REGISTER "+ch)
		m.channel = ch
		m.push(cSys.Render("created " + ch + " — you're the founder. Share the name so members can /join it."))
	case "/join":
		if arg == "" {
			m.push(cErr.Render("usage: /join #channel"))
			break
		}
		ch := arg
		if !strings.HasPrefix(ch, "#") {
			ch = "#" + ch
		}
		_ = m.c.Join(ch)
		m.channel = ch
		m.push(cSys.Render("joining " + ch))
	case "/part":
		ch := m.channel
		if arg != "" {
			ch = arg
		}
		_ = m.c.Part(ch)
		m.push(cSys.Render("leaving " + ch))
	case "/msg":
		f := strings.SplitN(arg, " ", 2)
		if len(f) < 2 {
			m.push(cErr.Render("usage: /msg <nick> <text>"))
			break
		}
		_ = m.c.Privmsg(f[0], f[1])
		m.push(cSelf.Render(m.c.Nick()) + " " + cNote.Render("→"+f[0]+" ") + f[1])
	case "/me":
		if arg == "" {
			break
		}
		_ = m.c.Privmsg(m.channel, "\x01ACTION "+arg+"\x01")
		m.push(cSelf.Render("* "+m.c.Nick()) + " " + arg)
	case "/names":
		_ = m.c.Raw("NAMES " + m.channel)
	case "/nick":
		if arg == "" {
			m.push(cErr.Render("usage: /nick <name>"))
			break
		}
		_ = m.c.Raw("NICK " + arg)
	case "/quit":
		return tea.Quit
	default:
		_ = m.c.Raw(strings.TrimPrefix(text, "/"))
		m.push(cSys.Render("» " + strings.TrimPrefix(text, "/")))
	}
	return nil
}

func (m *model) View() string {
	rows := m.height - 4
	if rows < 3 {
		rows = 3
	}
	start := 0
	if len(m.lines) > rows {
		start = len(m.lines) - rows
	}
	body := strings.Join(m.lines[start:], "\n")
	header := cTitle.Render("irc@ — "+m.channel) + cSys.Render("  ("+m.c.Nick()+" · esc to leave)")
	prompt := cSys.Render(m.channel+" ") + "› " + m.input + "█"
	return lipgloss.NewStyle().Padding(0, 1).Render(header + "\n" + body + "\n\n" + prompt)
}
