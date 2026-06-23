// Package agentgames is the in-BBS face of AgentGames (PRD §5.2): humans browse
// the agent-vs-agent ELO ladders, watch logged match replays, and play the
// games against a bot for practice. Rated agent matches happen over the game@
// protocol (internal/games); this plugin is read-only for the ladder/replays
// and off-ladder for practice.
package agentgames

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/games"
	"github.com/profullstack/agentbbs/internal/plugin"
	"github.com/profullstack/agentbbs/internal/store"
	"github.com/profullstack/agentbbs/internal/ui"
)

// Plugin is the AgentGames hub entry.
type Plugin struct{ reg *games.Registry }

// New builds the plugin over the shared game catalog.
func New(reg *games.Registry) Plugin { return Plugin{reg: reg} }

func (Plugin) ID() string          { return "agentgames" }
func (Plugin) Title() string       { return "AgentGames" }
func (Plugin) Description() string { return "Agent ladders, match replays, practice vs a bot" }
func (Plugin) RequiresAuth() bool  { return false }

func (p Plugin) New(user auth.User, ctx plugin.Context) tea.Model {
	return &model{
		user: user,
		st:   ctx.Store,
		reg:  p.reg,
		bot:  games.GreedyBot{R: rand.New(rand.NewSource(time.Now().UnixNano()))},
	}
}

type screen int

const (
	scGames screen = iota // pick a game
	scMenu                // pick an action for the game
	scLadder
	scReplays
	scReplay
	scPlay
)

var (
	title  = lipgloss.NewStyle().Bold(true).Foreground(ui.Green)
	dim    = ui.Dim
	cursor = lipgloss.NewStyle().Bold(true).Foreground(ui.Green)
	head   = lipgloss.NewStyle().Bold(true).Foreground(ui.Blue)
	warn   = ui.Danger
	frame  = ui.Frame
)

var actions = []string{"Ladder", "Replays", "Play vs bot"}

type model struct {
	user auth.User
	st   store.Store
	reg  *games.Registry
	bot  games.Bot

	screen screen
	cursor int
	note   string

	game games.Game // selected

	ladder  []store.RatingRow
	matches []store.MatchRow

	// replay
	replay store.MatchRow
	ply    int

	// practice
	state  games.State
	human  int
	legal  []string
	over   bool
	winner int
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	m.note = ""
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	}
	switch m.screen {
	case scGames:
		return m.updateGames(key)
	case scMenu:
		return m.updateMenu(key)
	case scLadder, scReplays:
		return m.updateList(key)
	case scReplay:
		return m.updateReplay(key)
	case scPlay:
		return m.updatePlay(key)
	}
	return m, nil
}

func (m *model) updateGames(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	all := m.reg.All()
	switch k.String() {
	case "q", "esc":
		return m, plugin.Exit
	case "up", "k":
		m.up()
	case "down", "j":
		if m.cursor < len(all)-1 {
			m.cursor++
		}
	case "enter", "right", "l":
		if len(all) == 0 {
			return m, nil
		}
		m.game = all[m.cursor]
		m.screen, m.cursor = scMenu, 0
	}
	return m, nil
}

func (m *model) updateMenu(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q":
		return m, plugin.Exit
	case "esc", "left", "h":
		m.screen, m.cursor = scGames, 0
	case "up", "k":
		m.up()
	case "down", "j":
		if m.cursor < len(actions)-1 {
			m.cursor++
		}
	case "enter", "right", "l":
		switch m.cursor {
		case 0:
			m.ladder, _ = m.st.TopRatings(m.game.ID(), 25)
			m.screen, m.cursor = scLadder, 0
		case 1:
			m.matches, _ = m.st.RecentMatches(m.game.ID(), 25)
			m.screen, m.cursor = scReplays, 0
		case 2:
			m.startPractice()
		}
	}
	return m, nil
}

func (m *model) updateList(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.ladder)
	if m.screen == scReplays {
		n = len(m.matches)
	}
	switch k.String() {
	case "q":
		return m, plugin.Exit
	case "esc", "left", "h":
		m.screen, m.cursor = scMenu, 0
	case "up", "k":
		m.up()
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "enter", "right", "l":
		if m.screen == scReplays && m.cursor < len(m.matches) {
			m.replay = m.matches[m.cursor]
			m.ply = 0
			m.screen = scReplay
		}
	}
	return m, nil
}

func (m *model) updateReplay(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q":
		return m, plugin.Exit
	case "esc", "left", "h":
		m.screen = scReplays
	case "right", "l", " ":
		if m.ply < len(m.replay.Moves) {
			m.ply++
		}
	case "up", "k":
		if m.ply > 0 {
			m.ply--
		}
	case "home", "g":
		m.ply = 0
	case "end", "G":
		m.ply = len(m.replay.Moves)
	}
	return m, nil
}

func (m *model) updatePlay(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q":
		return m, plugin.Exit
	case "esc", "left", "h":
		m.screen, m.cursor = scMenu, 0
	case "r":
		m.startPractice()
	case "up", "k":
		m.up()
	case "down", "j":
		if m.cursor < len(m.legal)-1 {
			m.cursor++
		}
	case "enter", " ":
		m.applyHumanMove()
	}
	return m, nil
}

func (m *model) up() {
	if m.cursor > 0 {
		m.cursor--
	}
}

func (m *model) startPractice() {
	m.state = m.game.Start()
	m.human = 0 // human is X and moves first
	m.over, m.winner = false, 0
	m.refreshLegal()
	m.screen, m.cursor = scPlay, 0
}

func (m *model) refreshLegal() {
	if m.over, m.winner = m.state.Terminal(); m.over {
		m.legal = nil
		return
	}
	m.legal = m.state.Legal()
	if m.cursor >= len(m.legal) {
		m.cursor = 0
	}
}

func (m *model) applyHumanMove() {
	if m.over || m.state.ToMove() != m.human || m.cursor >= len(m.legal) {
		return
	}
	ns, err := m.state.Apply(m.legal[m.cursor])
	if err != nil {
		m.note = warn.Render("illegal move")
		return
	}
	m.state = ns
	// Bot replies until it is the human's turn again or the game ends.
	for {
		if over, _ := m.state.Terminal(); over {
			break
		}
		if m.state.ToMove() == m.human {
			break
		}
		mv := m.bot.Move(m.state)
		if ns, err := m.state.Apply(mv); err == nil {
			m.state = ns
		} else {
			break
		}
	}
	m.cursor = 0
	m.refreshLegal()
}

func (m *model) View() string {
	var body, help string
	switch m.screen {
	case scGames:
		body, help = m.viewGames()
	case scMenu:
		body, help = m.viewMenu()
	case scLadder:
		body, help = m.viewLadder()
	case scReplays:
		body, help = m.viewReplays()
	case scReplay:
		body, help = m.viewReplay()
	case scPlay:
		body, help = m.viewPlay()
	}
	out := title.Render("AgentGames") + "\n\n" + body + "\n" + ui.KeyBar(help)
	if m.note != "" {
		out += "\n" + m.note
	}
	return frame.Render(out)
}

func (m *model) viewGames() (string, string) {
	var b strings.Builder
	b.WriteString(head.Render("Pick a game") + "\n\n")
	for i, g := range m.reg.All() {
		b.WriteString(m.row(i) + g.Title() + "  " + dim.Render("("+g.ID()+")") + "\n")
	}
	return b.String(), "↑/↓ move · enter select · q quit"
}

func (m *model) viewMenu() (string, string) {
	var b strings.Builder
	b.WriteString(head.Render(m.game.Title()) + "\n\n")
	for i, a := range actions {
		b.WriteString(m.row(i) + a + "\n")
	}
	return b.String(), "↑/↓ move · enter select · esc back · q quit"
}

func (m *model) viewLadder() (string, string) {
	var b strings.Builder
	b.WriteString(head.Render(m.game.Title()+" — ladder") + "\n\n")
	if len(m.ladder) == 0 {
		b.WriteString(dim.Render("  no rated matches yet — agents play via ssh game@\n"))
	}
	for i, r := range m.ladder {
		fmt.Fprintf(&b, "  %2d. %-20s %4.0f  %s\n", i+1, r.User, r.Rating, dim.Render(fmt.Sprintf("%d games", r.Played)))
	}
	return b.String(), "esc back · q quit"
}

func (m *model) viewReplays() (string, string) {
	var b strings.Builder
	b.WriteString(head.Render(m.game.Title()+" — replays") + "\n\n")
	if len(m.matches) == 0 {
		b.WriteString(dim.Render("  no matches recorded yet\n"))
	}
	for i, mt := range m.matches {
		fmt.Fprintf(&b, "%s%s vs %s  %s\n", m.row(i), mt.P0, mt.P1, dim.Render(outcomeLabel(mt)))
	}
	return b.String(), "↑/↓ move · enter watch · esc back · q quit"
}

func (m *model) viewReplay() (string, string) {
	g, ok := m.reg.Get(m.replay.Game)
	if !ok {
		return warn.Render("unknown game " + m.replay.Game), "esc back"
	}
	s := g.Start()
	for i := 0; i < m.ply && i < len(m.replay.Moves); i++ {
		if ns, err := s.Apply(m.replay.Moves[i].Move); err == nil {
			s = ns
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s vs %s   %s\n\n", m.replay.P0, m.replay.P1, dim.Render(outcomeLabel(m.replay)))
	b.WriteString(s.Render() + "\n\n")
	fmt.Fprintf(&b, "%s\n", dim.Render(fmt.Sprintf("ply %d/%d", m.ply, len(m.replay.Moves))))
	if m.ply >= len(m.replay.Moves) {
		b.WriteString(resultLine(m.replay.Winner, m.replay.P0, m.replay.P1, m.replay.Reason))
	}
	return b.String(), "→/space next · ↑ prev · g start · G end · esc back"
}

func (m *model) viewPlay() (string, string) {
	var b strings.Builder
	b.WriteString(head.Render(m.game.Title()+" — practice (you are X)") + "\n\n")
	b.WriteString(m.state.Render() + "\n\n")
	if m.over {
		b.WriteString(resultLine(m.winner, "you", m.bot.Name(), ""))
		return b.String(), "r play again · esc back · q quit"
	}
	if m.state.ToMove() == m.human {
		b.WriteString("your move:\n")
		for i, mv := range m.legal {
			b.WriteString(m.row(i) + mv + "\n")
		}
	} else {
		b.WriteString(dim.Render("(bot thinking…)\n"))
	}
	return b.String(), "↑/↓ pick · enter play · r restart · esc back"
}

func (m *model) row(i int) string {
	if i == m.cursor {
		return cursor.Render("❯ ")
	}
	return "  "
}

func outcomeLabel(mt store.MatchRow) string {
	switch mt.Winner {
	case games.Draw:
		return "draw"
	case 0:
		return mt.P0 + " won"
	default:
		return mt.P1 + " won"
	}
}

func resultLine(winner int, p0, p1, reason string) string {
	var s string
	switch winner {
	case games.Draw:
		s = "Draw."
	case 0:
		s = p0 + " wins."
	default:
		s = p1 + " wins."
	}
	if reason != "" {
		s += " (" + reason + ")"
	}
	return cursor.Render(s)
}
