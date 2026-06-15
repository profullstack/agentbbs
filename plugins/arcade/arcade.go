// Package arcade is the flagship plugin (PRD §5.1): classic terminal games.
// DOOM and the 80s arcade classics (Space Invaders, Pac-Man, Tetris, Moon
// Patrol) run as sandboxed external binaries on a real PTY; built-in TUI games
// (snake) feed the global leaderboards.
package arcade

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
	"github.com/profullstack/agentbbs/internal/ui"
)

type Plugin struct{}

func (Plugin) ID() string          { return "arcade" }
func (Plugin) Title() string       { return "Arcade" }
func (Plugin) Description() string { return "DOOM, Space Invaders, Pac-Man, Tetris, snake & leaderboards" }
func (Plugin) RequiresAuth() bool  { return false }

func (Plugin) New(user auth.User, ctx plugin.Context) tea.Model {
	return newMenu(user, ctx)
}

// extGame is an 80s arcade classic launched as a sandboxed subprocess on a real
// PTY — the doom-ascii pattern, generalized. The binary is resolved from the
// platform assets dir first, then the host PATH and the well-known distro game
// dirs, so either `scripts/fetch-assets.sh --arcade` (distro install) or a
// hand-built binary dropped in assets/bin makes the game appear in the menu.
type extGame struct {
	id    string   // stable id; also the per-user save subdir under arcade/
	label string   // menu label
	desc  string   // one-line menu description
	bins  []string // candidate binary names (first that resolves wins)
	args  []string // launch args (most need none)
}

// extGames is the arcade catalog of external classics, in menu order.
var extGames = []extGame{
	{id: "invaders", label: "Space Invaders", desc: "nInvaders — shoot the descending alien fleet", bins: []string{"ninvaders", "nInvaders"}},
	{id: "pacman", label: "Pac-Man", desc: "pacman4console — clear the maze, dodge the ghosts", bins: []string{"pacman4console"}},
	{id: "tetris", label: "Tetris", desc: "tint — stack the falling tetrominoes", bins: []string{"tint", "vitetris", "tetris"}},
	{id: "moonpatrol", label: "Moon Patrol", desc: "moon-buggy — jump the craters across the lunar surface", bins: []string{"moon-buggy"}},
}

// gameDirs are the well-known locations distro packages drop game binaries.
// Debian/Ubuntu put them in /usr/games, which is usually off the daemon's PATH,
// so we probe these explicitly in addition to exec.LookPath.
var gameDirs = []string{"/usr/games", "/usr/local/games", "/usr/local/bin", "/usr/bin"}

// entry is one row in the arcade menu.
type entry struct {
	section string
	label   string
	desc    string
	run     func(m *menu) (tea.Model, tea.Cmd)
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

var theme = ui.New(ui.Gold)

func newMenu(user auth.User, ctx plugin.Context) *menu {
	m := &menu{user: user, ctx: ctx}

	// --- DOOM (per WAD) ---
	for _, wad := range findWADs(ctx, user) {
		wad := wad
		m.entries = append(m.entries, entry{
			section: "DOOM",
			label:   "DOOM — " + filepath.Base(wad),
			desc:    "doom-ascii in a sandbox (24-bit color terminal recommended)",
			run:     func(m *menu) (tea.Model, tea.Cmd) { return m, m.launchDoom(wad) },
		})
	}
	if doomBin(ctx) == "" {
		m.entries = append(m.entries, entry{
			section: "DOOM",
			label:   "DOOM — not installed",
			desc:    "run scripts/fetch-assets.sh on the host to build doom-ascii + Freedoom",
			run:     func(m *menu) (tea.Model, tea.Cmd) { m.note = "assets missing on host"; return m, nil },
		})
	}

	// --- arcade classics (external binaries) ---
	var arcadeFound bool
	for _, g := range extGames {
		g := g
		if resolveBin(ctx, g.bins) == "" {
			continue
		}
		arcadeFound = true
		m.entries = append(m.entries, entry{
			section: "ARCADE",
			label:   g.label,
			desc:    g.desc,
			run:     func(m *menu) (tea.Model, tea.Cmd) { return m, m.launchExt(g) },
		})
	}
	if !arcadeFound {
		m.entries = append(m.entries, entry{
			section: "ARCADE",
			label:   "Arcade classics — not installed",
			desc:    "run scripts/fetch-assets.sh --arcade on the host (Space Invaders, Pac-Man, Tetris, Moon Patrol)",
			run:     func(m *menu) (tea.Model, tea.Cmd) { m.note = "arcade binaries missing on host"; return m, nil },
		})
	}

	// --- built-in (leaderboard-backed) ---
	m.entries = append(m.entries,
		entry{
			section: "BUILT-IN",
			label:   "Snake",
			desc:    "built-in; high scores hit the global leaderboard",
			run: func(m *menu) (tea.Model, tea.Cmd) {
				m.child = newSnake(m.user, m.ctx, m.width, m.height)
				return m, m.child.Init()
			},
		},
		entry{
			section: "BUILT-IN",
			label:   "Leaderboard",
			desc:    "global top scores",
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

// resolveBin finds the first candidate binary that exists: bundled in the
// platform assets dir, on PATH, or in a well-known distro game dir.
func resolveBin(ctx plugin.Context, names []string) string {
	for _, n := range names {
		if p := filepath.Join(ctx.AssetsDir, "bin", n); isExec(p) {
			return p
		}
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
		for _, d := range gameDirs {
			if p := filepath.Join(d, n); isExec(p) {
				return p
			}
		}
	}
	return ""
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// workDir returns the writable per-game save dir: a stable path for members,
// a throwaway temp dir for guests.
func (m *menu) workDir(sub string) string {
	if m.ctx.DataDir == "" {
		d, _ := os.MkdirTemp("", "agentbbs-guest-"+strings.ReplaceAll(sub, "/", "-")+"-")
		return d
	}
	d := filepath.Join(m.ctx.DataDir, "arcade", sub)
	_ = os.MkdirAll(d, 0o755)
	return d
}

// launchDoom suspends the TUI and bridges the session to a sandboxed
// doom-ascii on a real PTY. Savegames land in the per-user work dir.
func (m *menu) launchDoom(wad string) tea.Cmd {
	bin := doomBin(m.ctx)
	work := m.workDir(filepath.Join("doom", strings.TrimSuffix(filepath.Base(wad), filepath.Ext(wad))))
	cmd := m.ctx.Sandbox.Command(work, bin, "-iwad", wad)
	return tea.Exec(newPtyExec(cmd, m.width, m.height), func(err error) tea.Msg {
		return gameDoneMsg{name: "DOOM", err: err}
	})
}

// launchExt suspends the TUI and bridges the session to a sandboxed arcade
// classic on a real PTY (the generalized doom path).
func (m *menu) launchExt(g extGame) tea.Cmd {
	bin := resolveBin(m.ctx, g.bins)
	if bin == "" { // raced with an uninstall; surface rather than exec ""
		m.note = g.label + " is no longer installed on the host"
		return nil
	}
	work := m.workDir(g.id)
	cmd := m.ctx.Sandbox.Command(work, bin, g.args...)
	return tea.Exec(newPtyExec(cmd, m.width, m.height), func(err error) tea.Msg {
		return gameDoneMsg{name: g.label, err: err}
	})
}

type gameDoneMsg struct {
	name string
	err  error
}

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
	case gameDoneMsg:
		if msg.err != nil {
			m.note = msg.name + " exited: " + msg.err.Error()
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
	s := theme.Title("Arcade") + ui.Dim.Render("  ·  classic terminal games, sandboxed") + "\n\n"
	prevSection := ""
	for i, e := range m.entries {
		if e.section != prevSection {
			if prevSection != "" {
				s += "\n"
			}
			s += theme.Section(e.section) + "\n"
			prevSection = e.section
		}
		s += theme.MenuItem(i == m.cursor, e.label, "", e.desc)
	}
	s += "\n" + ui.KeyBar("↑/↓ move · enter play · q back to hub")
	if m.note != "" {
		s += "\n" + ui.Danger.Render(m.note)
	}
	return ui.Frame.Render(s)
}

// backMsg returns from a child (snake/leaderboard) to the arcade menu.
type backMsg struct{}

func back() tea.Msg { return backMsg{} }
