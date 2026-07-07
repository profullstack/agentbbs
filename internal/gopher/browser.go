package gopher

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"

	"github.com/profullstack/agentbbs/internal/ui"
)

// RunBrowser drives the hedgehog gopher browser over the SSH session: an
// in-process gopher client that resolves selectors with authed=true (so the
// member sees members-only content their SSH key entitles them to). This is the
// authenticated counterpart to the public RFC-1436 listener — same wire
// semantics, carried over SSH instead of port 70. member is the connecting
// member's name. Requires a PTY.
func RunBrowser(s ssh.Session, srv *Server, member string) error {
	ptyReq, winCh, hasPty := s.Pty()
	if !hasPty {
		_, _ = s.Write([]byte("gopher needs a terminal (ssh -t gopher@<host>)\r\n"))
		return nil
	}
	m := &browser{srv: srv, member: member, width: ptyReq.Window.Width, height: ptyReq.Window.Height}
	m.load("") // start at the root menu
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
	gTheme = ui.New(ui.Blue)
	gSel   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1020")).Background(ui.Blue)
)

type browser struct {
	srv    *Server
	member string

	sel   string // current selector
	isTxt bool   // current view is a text document (vs a menu)
	items []Item // menu rows (info + links) for the current menu
	links []int  // indices into items that are selectable
	cur   int    // cursor position within links
	lines []string
	top   int // first visible text line

	stack  []string // selector history for back navigation
	status string
	width  int
	height int
}

// load resolves sel on the authenticated surface and swaps the view to it.
func (b *browser) load(sel string) {
	r := b.srv.Resolve(sel, true, b.member)
	b.sel = sel
	b.status = ""
	switch r.Kind {
	case KindMenu:
		b.isTxt = false
		b.items = r.Menu.Items
		b.links = b.links[:0]
		for i, it := range b.items {
			if it.Type != TypeInfo {
				b.links = append(b.links, i)
			}
		}
		b.cur = 0
	case KindText:
		b.isTxt = true
		b.lines = strings.Split(strings.ReplaceAll(r.Text, "\r\n", "\n"), "\n")
		b.top = 0
	case KindBinary:
		b.status = "(binary file — not shown)"
	default: // KindError
		b.status = r.Text
	}
}

func (b *browser) Init() tea.Cmd { return nil }

func (b *browser) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		b.width, b.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return b, tea.Quit
		case "up", "k":
			if b.isTxt {
				if b.top > 0 {
					b.top--
				}
			} else if b.cur > 0 {
				b.cur--
			}
		case "down", "j":
			if b.isTxt {
				if b.top < b.maxTop() {
					b.top++
				}
			} else if b.cur < len(b.links)-1 {
				b.cur++
			}
		case "pgdown", " ":
			if b.isTxt {
				b.top = min(b.top+b.bodyRows(), b.maxTop())
			}
		case "pgup":
			if b.isTxt {
				b.top = max(b.top-b.bodyRows(), 0)
			}
		case "enter", "right", "l":
			if !b.isTxt {
				b.open()
			}
		case "backspace", "left", "h", "esc":
			b.back()
		case "g", "home":
			if b.isTxt {
				b.top = 0
			} else {
				b.cur = 0
			}
		}
	}
	return b, nil
}

// open follows the currently selected menu link.
func (b *browser) open() {
	if len(b.links) == 0 {
		return
	}
	it := b.items[b.links[b.cur]]
	if strings.HasPrefix(it.Selector, "URL:") {
		b.status = "external: " + strings.TrimPrefix(it.Selector, "URL:")
		return
	}
	b.stack = append(b.stack, b.sel)
	b.load(it.Selector)
}

// back returns to the previous selector.
func (b *browser) back() {
	if len(b.stack) == 0 {
		return
	}
	prev := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	b.load(prev)
}

func (b *browser) bodyRows() int {
	r := b.height - 4 // header + blank + keybar + margin
	if r < 1 {
		return 1
	}
	return r
}

func (b *browser) maxTop() int {
	m := len(b.lines) - b.bodyRows()
	if m < 0 {
		return 0
	}
	return m
}

func (b *browser) View() string {
	var sb strings.Builder
	loc := b.sel
	if loc == "" {
		loc = "/"
	}
	sb.WriteString(gTheme.Title("gopher://"+trimBBS(b.srv.host)) + ui.Dim.Render("  "+loc) + "\n\n")

	rows := b.bodyRows()
	if b.isTxt {
		end := min(b.top+rows, len(b.lines))
		for _, ln := range b.lines[b.top:end] {
			sb.WriteString(truncate(ln, b.width) + "\n")
		}
		sb.WriteString("\n" + keyBar("↑/↓ scroll · space page · ⌫ back · q quit"))
		return ui.Frame.Render(sb.String())
	}

	// menu view
	shown := 0
	for i, it := range b.items {
		if shown >= rows {
			break
		}
		shown++
		if it.Type == TypeInfo {
			sb.WriteString(ui.Dim.Render(truncate(it.Display, b.width)) + "\n")
			continue
		}
		line := gopherGlyph(it.Type) + " " + it.Display
		if b.links[b.cur] == i {
			sb.WriteString(gSel.Render("› "+truncate(line, b.width-2)) + "\n")
		} else {
			sb.WriteString("  " + truncate(line, b.width-2) + "\n")
		}
	}
	if b.status != "" {
		sb.WriteString("\n" + ui.Danger.Render(b.status) + "\n")
	}
	sb.WriteString("\n" + keyBar("↑/↓ move · ⏎ open · ⌫ back · q quit"))
	return ui.Frame.Render(sb.String())
}

// gopherGlyph is a one-rune hint of a menu item's type.
func gopherGlyph(t byte) string {
	switch t {
	case TypeMenu:
		return "▸"
	case TypeText:
		return "▪"
	case TypeImage:
		return "▨"
	case TypeHTML:
		return "↗"
	case TypeBinary:
		return "⬦"
	default:
		return " "
	}
}

func keyBar(s string) string { return ui.Dim.Render(s) }

func truncate(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	if len(r) > w {
		return string(r[:w])
	}
	return s
}
