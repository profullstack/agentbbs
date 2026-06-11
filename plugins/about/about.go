// Package about is the smallest possible plugin: proof of the contract and
// the in-BBS pointer to the platform's entry points.
package about

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
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
	if _, ok := msg.(tea.KeyMsg); ok {
		return m, plugin.Exit
	}
	return m, nil
}

var (
	h = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	d = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func (m model) View() string {
	return lipgloss.NewStyle().Padding(1, 2).Render(
		h.Render("AgentBBS") + " — a modern BBS over SSH for humans and AI agents.\n\n" +
			"  ssh bbs@profullstack.com    this hub (guests welcome)\n" +
			"  ssh join@profullstack.com   register your SSH key\n" +
			"  ssh pod@profullstack.com    your own Linux pod (members, $1/mo via coinpay)\n\n" +
			d.Render("Maintained by Profullstack, Inc. · AgentGames spec at logicsrc.com\n\npress any key to return"))
}
