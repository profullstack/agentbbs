// Package chat is the agent@ surface: talk to the operator's AI agent (or,
// once the M2 admin console lands, the operator live).
//
// The agent backend is one configurable command, AGENTBBS_AGENT_CMD: each
// user message is piped to its stdin, stdout comes back as the reply. Point
// it at anything — `claude -p`, a logicsrc/commandboard agent, a shell
// script. Unset = a polite "leave a message" mode (messages are persisted
// either way, so the operator can read them later).
package chat

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/store"
)

const historyLines = 200

// Handle runs the chat UI; leaving ends the SSH session.
func Handle(s ssh.Session, st store.Store, user auth.User) error {
	ptyReq, winCh, hasPty := s.Pty()
	if !hasPty {
		_, _ = s.Write([]byte("agent chat needs a terminal (ssh -t)\r\n"))
		return nil
	}
	m := &model{
		st:     st,
		user:   user,
		width:  ptyReq.Window.Width,
		height: ptyReq.Window.Height,
	}
	if msgs, err := st.RecentChats(user.Name, 20); err == nil {
		for _, c := range msgs {
			m.lines = append(m.lines, render(c.Role, c.Text))
		}
		if len(msgs) > 0 {
			m.lines = append(m.lines, cDim.Render("— earlier conversation —"))
		}
	}
	p := tea.NewProgram(m, tea.WithInput(s), tea.WithOutput(s), tea.WithAltScreen())
	go func() {
		for w := range winCh {
			p.Send(tea.WindowSizeMsg{Width: w.Width, Height: w.Height})
		}
	}()
	_, err := p.Run()
	return err
}

var (
	cTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#c084fc"))
	cYou   = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ade80"))
	cAgent = lipgloss.NewStyle().Foreground(lipgloss.Color("#c084fc"))
	cDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	cErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

func render(role, text string) string {
	if role == "user" {
		return cYou.Render("you ") + text
	}
	return cAgent.Render("agent ") + text
}

type replyMsg struct {
	text string
	err  error
}

type model struct {
	st   store.Store
	user auth.User

	lines   []string
	input   string
	waiting bool

	width, height int
}

func (m *model) Init() tea.Cmd { return nil }

// ask pipes the message to the configured agent command.
func (m *model) ask(text string) tea.Cmd {
	return func() tea.Msg {
		cmdline := strings.TrimSpace(os.Getenv("AGENTBBS_AGENT_CMD"))
		if cmdline == "" {
			return replyMsg{text: "The operator's agent isn't wired up right now — your message is saved and a human will read it."}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		parts := strings.Fields(cmdline)
		cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
		cmd.Stdin = strings.NewReader(text)
		out, err := cmd.Output()
		if err != nil {
			return replyMsg{err: err}
		}
		reply := strings.TrimSpace(string(out))
		if reply == "" {
			reply = "(no reply)"
		}
		return replyMsg{text: reply}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case replyMsg:
		m.waiting = false
		if msg.err != nil {
			m.lines = append(m.lines, cErr.Render("agent error: "+msg.err.Error()))
			return m, nil
		}
		_ = m.st.AddChat(m.user.StoreID, m.user.Name, "agent", msg.text)
		for _, l := range strings.Split(msg.text, "\n") {
			m.lines = append(m.lines, render("agent", l))
		}
		if len(m.lines) > historyLines {
			m.lines = m.lines[len(m.lines)-historyLines:]
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			text := strings.TrimSpace(m.input)
			if text == "" || m.waiting {
				return m, nil
			}
			m.input = ""
			m.waiting = true
			_ = m.st.AddChat(m.user.StoreID, m.user.Name, "user", text)
			m.lines = append(m.lines, render("user", text))
			return m, m.ask(text)
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
	prompt := "> " + m.input + "█"
	if m.waiting {
		prompt = cDim.Render("agent is thinking…")
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(
		cTitle.Render("agent@ — talk to profullstack") + cDim.Render("  (esc to leave)") + "\n" +
			body + "\n\n" + prompt)
}
