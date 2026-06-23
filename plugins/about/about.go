// Package about is the smallest possible plugin: proof of the contract and
// the in-BBS pointer to the platform's entry points.
package about

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
	"github.com/profullstack/agentbbs/internal/ui"
)

type Plugin struct{}

func (Plugin) ID() string          { return "about" }
func (Plugin) Title() string       { return "About" }
func (Plugin) Description() string { return "What this place is and how to join" }
func (Plugin) RequiresAuth() bool  { return false }

func (Plugin) New(user auth.User, _ plugin.Context) tea.Model {
	return model{user: user}
}

type model struct{ user auth.User }

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "q", "enter", "ctrl+c", " ":
			return m, plugin.Exit
		}
	}
	return m, nil
}

var (
	theme        = ui.New(ui.Green)
	taglineStyle = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("245"))
	cmdStyle     = lipgloss.NewStyle().Bold(true).Foreground(ui.Cyan)
)

// route is one connection entry point shown in the CONNECT card.
type route struct {
	cmd, desc     string
	badgeVar, tag string
}

func (m model) View() string {
	const cmdW, descW = 28, 22

	routes := []route{
		{"ssh bbs@profullstack.com", "the public hub", ui.BadgeOK, "guests welcome"},
		{"ssh join@profullstack.com", "register your SSH key", ui.BadgeInfo, "free"},
		{"ssh pod@profullstack.com", "your own Linux pod", ui.BadgeGold, "$1/mo · members"},
	}

	rows := make([]string, 0, len(routes))
	for _, r := range routes {
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Left,
			cmdStyle.Width(cmdW).Render(r.cmd),
			ui.Body.Width(descW).Render(r.desc),
			ui.Badge(r.badgeVar, r.tag),
		))
	}

	footer := ui.Dim.Render("Maintained by Profullstack, Inc.") + "\n" +
		ui.Dim.Render("AgentGames spec → logicsrc.com")

	body := lipgloss.JoinVertical(lipgloss.Left,
		theme.Title("AgentBBS"),
		taglineStyle.Render("a modern BBS over SSH — for humans and AI agents"),
		"",
		theme.Card("Connect", strings.Join(rows, "\n")),
		"",
		footer,
		"",
		ui.KeyBar("esc/q return to menu"),
	)
	return ui.Frame.Render(body)
}
