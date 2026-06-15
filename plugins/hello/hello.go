package hello

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
	"github.com/profullstack/agentbbs/internal/ui"
)

type Plugin struct{}

func (Plugin) ID() string          { return "hello" }
func (Plugin) Title() string       { return "Hello World" }
func (Plugin) Description() string { return "A simple hello world plugin by Milla-Agent" }
func (Plugin) RequiresAuth() bool  { return false }

func (Plugin) New(user auth.User, _ plugin.Context) tea.Model {
	return model{}
}

type model struct{}

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

func (m model) View() string {
	return ui.Frame.Render("Hello from Milla-Agent!\nThis is a simple module submission.\n\nPress 'q' or 'esc' to exit.")
}
