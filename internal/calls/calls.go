// Package calls renders PairUX video calls as truecolor ASCII in the
// terminal: `ssh video-<code>@host` joins directly, `ssh video@host` prompts
// for a code. Codes are minted by PairUX — the SSH surface never creates
// calls, it only joins existing ones.
package calls

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"

	"github.com/profullstack/agentbbs/internal/ascii"
)

// RouteCode extracts the call code from an SSH username: "video" → "",
// "video-abc123" → "abc123". Second return is false when the username is
// not a video route at all.
func RouteCode(username string) (string, bool) {
	u := strings.ToLower(username)
	if u == "video" {
		return "", true
	}
	if strings.HasPrefix(u, "video-") && len(u) > len("video-") {
		return u[len("video-"):], true
	}
	return "", false
}

// Handle runs the call UI on the session. Standalone surface: leaving the
// call ends the SSH session.
func Handle(s ssh.Session, code, identity string) error {
	ptyReq, winCh, hasPty := s.Pty()
	if !hasPty {
		_, _ = s.Write([]byte("video calls need a terminal (ssh -t)\r\n"))
		return nil
	}
	w, h := ptyReq.Window.Width, ptyReq.Window.Height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	m := &model{
		code:     code,
		identity: identity,
		cfg:      ConfigFromEnv(),
		width:    w,
		height:   h,
	}
	p := tea.NewProgram(m,
		tea.WithInput(s), tea.WithOutput(s), tea.WithAltScreen())

	go func() {
		for w := range winCh {
			p.Send(tea.WindowSizeMsg{Width: w.Width, Height: w.Height})
		}
	}()
	_, err := p.Run()
	if m.sess != nil {
		m.sess.Close()
	}
	return err
}

type frameMsg string
type statusMsg string
type joinedMsg struct {
	sess *session
	err  error
}

type model struct {
	code     string
	identity string
	cfg      Config

	input  string // code entry buffer
	sess   *session
	frame  string
	status string
	errMsg string

	width, height int
	pw            int // pixel width locked at join time (decoder output width)
	joining       bool
}

var (
	vTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#60a5fa"))
	vDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	vErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

func (m *model) Init() tea.Cmd {
	if m.code != "" {
		return m.join()
	}
	return nil
}

// join connects in the background and pumps frames/status into the program.
func (m *model) join() tea.Cmd {
	m.joining = true
	code := m.code
	pw, ph := ascii.FitEven(m.width, m.height)
	m.pw = pw // decoder output is locked to this width for the session
	cfg, id := m.cfg, m.identity
	return func() tea.Msg {
		sess, err := join(cfg, code, id, pw, ph)
		return joinedMsg{sess: sess, err: err}
	}
}

// nextFrame renders one decoded frame at the locked decoder width.
func (m *model) nextFrame() tea.Cmd {
	sess, pw := m.sess, m.pw
	return func() tea.Msg {
		buf, ok := <-sess.Frames
		if !ok {
			return statusMsg("stream ended")
		}
		ph := (len(buf) / 3) / pw
		return frameMsg(ascii.FrameRGB(buf, pw, ph))
	}
}

func (m *model) pump() tea.Cmd {
	sess := m.sess
	return tea.Batch(
		m.nextFrame(),
		func() tea.Msg { return statusMsg(<-sess.Status) },
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case joinedMsg:
		m.joining = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.sess = msg.sess
		return m, m.pump()
	case frameMsg:
		m.frame = string(msg)
		return m, m.nextFrame()
	case statusMsg:
		m.status = string(msg)
		if m.sess != nil {
			return m, func() tea.Msg { return statusMsg(<-m.sess.Status) }
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.sess == nil && m.code == "" && msg.String() == "q" {
				break // let people type codes containing q
			}
			return m, tea.Quit
		case "esc":
			return m, tea.Quit
		}
		// Code entry mode.
		if m.sess == nil && !m.joining && m.code == "" {
			switch msg.String() {
			case "enter":
				if strings.TrimSpace(m.input) != "" {
					m.code = strings.TrimSpace(strings.ToLower(m.input))
					return m, m.join()
				}
			case "backspace":
				if len(m.input) > 0 {
					m.input = m.input[:len(m.input)-1]
				}
			default:
				if len(msg.String()) == 1 && len(m.input) < 64 {
					m.input += msg.String()
				}
			}
		}
	}
	return m, nil
}

func (m *model) View() string {
	switch {
	case m.errMsg != "":
		return lipgloss.NewStyle().Padding(1, 2).Render(
			vTitle.Render("PairUX video") + "\n\n" +
				vErr.Render(m.errMsg) + "\n\n" +
				vDim.Render("esc to leave"))
	case m.sess == nil && !m.joining:
		return lipgloss.NewStyle().Padding(1, 2).Render(
			vTitle.Render("PairUX video") + "\n\n" +
				"Enter your call code (from pairux.com):\n\n" +
				"  > " + m.input + "█\n\n" +
				vDim.Render("enter join · esc leave — don't have a code? create the call in PairUX first"))
	case m.joining:
		return lipgloss.NewStyle().Padding(1, 2).Render(
			vTitle.Render("PairUX video") + "\n\n  joining " + m.code + "…")
	case m.frame == "":
		return lipgloss.NewStyle().Padding(1, 2).Render(
			vTitle.Render("PairUX video") + "\n\n  " + m.status + "\n\n" +
				vDim.Render("esc to leave"))
	default:
		return m.frame + "\r\n" + vDim.Render(" "+m.code+" · "+m.status+" · esc to leave")
	}
}
