package files

import (
	"fmt"
	"path"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
	"github.com/profullstack/agentbbs/internal/store"
)

// viewMax caps how much of a file the in-BBS viewer loads.
const viewMax = 64 << 10

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	dirStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#60a5fa")).Bold(true)
	selStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("#4ade80"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f87171"))
)

// NewPlugin returns the in-BBS file browser plugin bound to svc.
func NewPlugin(svc *Service) plugin.Plugin { return browserPlugin{svc: svc} }

type browserPlugin struct{ svc *Service }

func (browserPlugin) ID() string          { return "files" }
func (browserPlugin) Title() string       { return "Files" }
func (browserPlugin) Description() string { return "Your SFTP workspace + the shared public area" }
func (browserPlugin) RequiresAuth() bool  { return true }

func (p browserPlugin) New(user auth.User, _ plugin.Context) tea.Model {
	sess, su, err := p.svc.OpenFor(user.Name)
	m := browser{svc: p.svc, sess: sess, user: su, cwd: "/", host: "bbs.profullstack.com"}
	if err != nil {
		m.fatal = "could not open your workspace: " + err.Error()
		return m
	}
	m.reload()
	return m
}

type mode int

const (
	modeBrowse mode = iota
	modeView
	modeInput
	modeConfirm
)

type browser struct {
	svc   *Service
	sess  *session
	user  store.User
	host  string
	cwd   string
	items []Entry
	sel   int
	msg   string
	fatal string

	mode    mode
	prompt  string       // input prompt label
	input   string       // typed text
	onInput func(string) // committed-input callback
	confirm string       // confirmation question
	onYes   func()       // confirmed-action callback

	viewName string
	viewBody string
}

func (m *browser) reload() {
	items, err := m.sess.entries(m.cwd)
	if err != nil {
		m.msg = warnStyle.Render("error: " + err.Error())
		m.items = nil
		return
	}
	m.items = items
	if m.sel >= len(items) {
		m.sel = len(items) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

func (m browser) Init() tea.Cmd { return nil }

func (m browser) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.fatal != "" {
		return m, plugin.Exit
	}
	switch m.mode {
	case modeView:
		m.mode = modeBrowse
		return m, nil
	case modeInput:
		return m.updateInput(key)
	case modeConfirm:
		return m.updateConfirm(key)
	default:
		return m.updateBrowse(key)
	}
}

func (m browser) updateBrowse(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q", "esc":
		return m, plugin.Exit
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < len(m.items)-1 {
			m.sel++
		}
	case "left", "h", "backspace":
		if m.cwd != "/" {
			m.cwd = path.Dir(strings.TrimRight(m.cwd, "/"))
			m.sel = 0
			m.msg = ""
			m.reload()
		}
	case "enter", "right", "l":
		return m.open()
	case "n":
		if m.sess.canWrite(m.childPath("x")) {
			m.startInput("new directory name: ", func(name string) {
				if name = clean(name); name == "" {
					return
				}
				if err := m.sess.mkdir(m.childPath(name)); err != nil {
					m.msg = warnStyle.Render("mkdir: " + err.Error())
				} else {
					m.msg = "created " + name + "/"
				}
				m.reload()
			})
		} else {
			m.msg = warnStyle.Render("this area is read-only")
		}
	case "r":
		if cur := m.current(); cur != nil && m.sess.canWrite(m.childPath(cur.Name)) {
			old := cur.Name
			m.startInput("rename "+old+" to: ", func(name string) {
				if name = clean(name); name == "" {
					return
				}
				if err := m.sess.rename(m.childPath(old), m.childPath(name)); err != nil {
					m.msg = warnStyle.Render("rename: " + err.Error())
				} else {
					m.msg = "renamed to " + name
				}
				m.reload()
			})
		} else {
			m.msg = warnStyle.Render("nothing to rename here")
		}
	case "d":
		if cur := m.current(); cur != nil && m.sess.canWrite(m.childPath(cur.Name)) {
			name := cur.Name
			m.startConfirm("delete "+name+"? (y/n)", func() {
				if err := m.sess.remove(m.childPath(name)); err != nil {
					m.msg = warnStyle.Render("delete: " + err.Error())
				} else {
					m.msg = "deleted " + name
				}
				m.reload()
			})
		} else {
			m.msg = warnStyle.Render("nothing to delete here")
		}
	}
	return m, nil
}

func (m browser) open() (tea.Model, tea.Cmd) {
	cur := m.current()
	if cur == nil {
		return m, nil
	}
	target := m.childPath(cur.Name)
	if cur.IsDir {
		m.cwd = target
		m.sel = 0
		m.msg = ""
		m.reload()
		return m, nil
	}
	body, truncated, err := m.sess.readFile(target, viewMax)
	if err != nil {
		m.msg = warnStyle.Render("open: " + err.Error())
		return m, nil
	}
	m.viewName = cur.Name
	m.viewBody = string(body)
	if truncated {
		m.viewBody += "\n\n" + dimStyle.Render("… (truncated; download the full file over SFTP)")
	}
	m.mode = modeView
	return m, nil
}

func (m browser) updateInput(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEnter:
		m.mode = modeBrowse
		if m.onInput != nil {
			m.onInput(m.input)
		}
		m.input = ""
	case tea.KeyEsc:
		m.mode = modeBrowse
		m.input = ""
	case tea.KeyBackspace:
		if m.input != "" {
			m.input = m.input[:len(m.input)-1]
		}
	case tea.KeyRunes, tea.KeySpace:
		m.input += string(key.Runes)
	}
	return m, nil
}

func (m browser) updateConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "y", "Y":
		m.mode = modeBrowse
		if m.onYes != nil {
			m.onYes()
		}
	case "n", "N", "esc":
		m.mode = modeBrowse
		m.msg = "cancelled"
	}
	return m, nil
}

func (m *browser) startInput(prompt string, cb func(string)) {
	m.mode = modeInput
	m.prompt = prompt
	m.input = ""
	m.onInput = cb
}

func (m *browser) startConfirm(q string, cb func()) {
	m.mode = modeConfirm
	m.confirm = q
	m.onYes = cb
}

func (m browser) current() *Entry {
	if m.sel < 0 || m.sel >= len(m.items) {
		return nil
	}
	return &m.items[m.sel]
}

func (m browser) childPath(name string) string {
	return path.Join(m.cwd, name)
}

func (m browser) View() string {
	if m.fatal != "" {
		return lipgloss.NewStyle().Padding(1, 2).Render(warnStyle.Render(m.fatal) + "\n\npress any key to return")
	}
	if m.mode == modeView {
		return lipgloss.NewStyle().Padding(1, 2).Render(
			titleStyle.Render("Files · "+m.viewName) + "\n\n" + m.viewBody +
				"\n\n" + dimStyle.Render("press any key to go back"))
	}

	var b strings.Builder
	usage, _ := m.svc.Usage(m.user)
	b.WriteString(titleStyle.Render("Files") + "  " + dimStyle.Render(m.cwd) + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("workspace: %s / %s  (%d%% used)  ·  transfer: sftp files@%s\n\n",
		humanBytes(usage.Bytes), humanBytes(usage.Quota), pct(usage.Bytes, usage.Quota), m.host)))

	if len(m.items) == 0 {
		b.WriteString(dimStyle.Render("  (empty)\n"))
	}
	for i, e := range m.items {
		name := e.Name
		meta := humanBytes(e.Size)
		if e.IsDir {
			name += "/"
			meta = "dir"
		}
		line := fmt.Sprintf("  %-28s %8s", name, meta)
		switch {
		case i == m.sel:
			b.WriteString(selStyle.Render(line))
		case e.IsDir:
			b.WriteString(dirStyle.Render(line))
		default:
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	switch m.mode {
	case modeInput:
		b.WriteString(titleStyle.Render(m.prompt) + m.input + "_\n")
	case modeConfirm:
		b.WriteString(warnStyle.Render(m.confirm) + "\n")
	default:
		if m.msg != "" {
			b.WriteString(m.msg + "\n")
		}
		b.WriteString(dimStyle.Render("↑/↓ move · enter open · n new dir · r rename · d delete · ←/bksp up · q quit"))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

// clean trims a user-typed file/dir name to a single safe path segment.
func clean(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "")
	if s == "." || s == ".." {
		return ""
	}
	return s
}

func pct(used, quota int64) int {
	if quota <= 0 {
		return 0
	}
	return int(used * 100 / quota)
}

// humanBytes formats a byte count compactly (e.g. 1.5G).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}
