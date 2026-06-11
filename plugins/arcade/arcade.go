// Package arcade is the flagship plugin (PRD §5.1): classic terminal games.
// DOOM runs as a sandboxed external binary (doom-ascii + Freedoom); built-in
// TUI games (snake) feed the global leaderboards.
package arcade

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
)

type Plugin struct{}

func (Plugin) ID() string          { return "arcade" }
func (Plugin) Title() string       { return "Arcade" }
func (Plugin) Description() string { return "DOOM (ASCII), snake, leaderboards" }
func (Plugin) RequiresAuth() bool  { return false }

func (Plugin) New(user auth.User, ctx plugin.Context) tea.Model {
	return newMenu(user, ctx)
}

// entry is one row in the arcade menu.
type entry struct {
	label string
	desc  string
	run   func(m *menu) (tea.Model, tea.Cmd)
}

type menu struct {
	user    auth.User
	ctx     plugin.Context
	entries []entry
	cursor  int
	width   int
	height  int
	note    string
	child   tea.Model // snake / leaderboard take over here
}

var (
	tStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fbbf24"))
	dStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	cStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24"))
	eStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

func newMenu(user auth.User, ctx plugin.Context) *menu {
	m := &menu{user: user, ctx: ctx}
	for _, wad := range findWADs(ctx, user) {
		wad := wad
		m.entries = append(m.entries, entry{
			label: "DOOM — " + filepath.Base(wad),
			desc:  "doom-ascii in a sandbox (24-bit color terminal recommended)",
			run:   func(m *menu) (tea.Model, tea.Cmd) { return m, m.launchDoom(wad) },
		})
	}
	if len(m.entries) == 0 {
		m.entries = append(m.entries, entry{
			label: "DOOM — not installed",
			desc:  "run scripts/fetch-assets.sh on the host to build doom-ascii + Freedoom",
			run:   func(m *menu) (tea.Model, tea.Cmd) { m.note = "assets missing on host"; return m, nil },
		})
	}
	m.entries = append(m.entries,
		entry{
			label: "Snake",
			desc:  "built-in; high scores hit the global leaderboard",
			run: func(m *menu) (tea.Model, tea.Cmd) {
				m.child = newSnake(m.user, m.ctx, m.width, m.height)
				return m, m.child.Init()
			},
		},
		entry{
			label: "Leaderboard",
			desc:  "global top scores",
			run: func(m *menu) (tea.Model, tea.Cmd) {
				m.child = newBoard(m.ctx)
				return m, m.child.Init()
			},
		},
	)
	return m
}

// findWADs lists platform WADs plus the member's own ~/wads (PRD §5.1, §9.1).
func findWADs(ctx plugin.Context, user auth.User) []string {
	if doomBin(ctx) == "" {
		return nil
	}
	var out []string
	dirs := []string{filepath.Join(ctx.AssetsDir, "wads")}
	if user.Kind != auth.Guest && ctx.DataDir != "" {
		dirs = append(dirs, filepath.Join(ctx.DataDir, "wads"))
	}
	for _, dir := range dirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.wad"))
		matchesUpper, _ := filepath.Glob(filepath.Join(dir, "*.WAD"))
		out = append(out, append(matches, matchesUpper...)...)
	}
	return out
}

func doomBin(ctx plugin.Context) string {
	p := filepath.Join(ctx.AssetsDir, "bin", "doom_ascii")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// launchDoom suspends the TUI and bridges the session to a sandboxed
// doom-ascii on a real PTY. Savegames land in the per-user work dir.
func (m *menu) launchDoom(wad string) tea.Cmd {
	bin := doomBin(m.ctx)
	work := m.ctx.DataDir
	if work == "" { // guests: throwaway saves
		work, _ = os.MkdirTemp("", "agentbbs-guest-doom-")
	} else {
		work = filepath.Join(work, "doom", strings.TrimSuffix(filepath.Base(wad), filepath.Ext(wad)))
		_ = os.MkdirAll(work, 0o755)
	}
	cmd := m.ctx.Sandbox.Command(work, bin, "-iwad", wad)
	return tea.Exec(newPtyExec(cmd, m.width, m.height), func(err error) tea.Msg {
		return doomDoneMsg{err: err}
	})
}

type doomDoneMsg struct{ err error }

func (m *menu) Init() tea.Cmd { return nil }

func (m *menu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
	}
	if m.child != nil {
		if _, ok := msg.(backMsg); ok {
			m.child = nil
			return m, nil
		}
		next, cmd := m.child.Update(msg)
		m.child = next
		return m, cmd
	}
	switch msg := msg.(type) {
	case doomDoneMsg:
		if msg.err != nil {
			m.note = "doom exited: " + msg.err.Error()
		}
		return m, nil
	case tea.KeyMsg:
		m.note = ""
		switch msg.String() {
		case "q", "esc":
			return m, plugin.Exit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "enter":
			return m.entries[m.cursor].run(m)
		}
	}
	return m, nil
}

func (m *menu) View() string {
	if m.child != nil {
		return m.child.View()
	}
	s := tStyle.Render("Arcade") + "\n\n"
	for i, e := range m.entries {
		cur := "  "
		if i == m.cursor {
			cur = cStyle.Render("> ")
		}
		s += fmt.Sprintf("%s%s\n  %s\n", cur, e.label, dStyle.Render(e.desc))
	}
	s += "\n" + dStyle.Render("↑/↓ move · enter play · q back to hub")
	if m.note != "" {
		s += "\n" + eStyle.Render(m.note)
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(s)
}

// backMsg returns from a child (snake/leaderboard) to the arcade menu.
type backMsg struct{}

func back() tea.Msg { return backMsg{} }
