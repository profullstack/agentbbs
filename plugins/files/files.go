package files

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
)

type Plugin struct{}

func (Plugin) ID() string          { return "files" }
func (Plugin) Title() string       { return "Files" }
func (Plugin) Description() string { return "Browse your personal pod files" }
func (Plugin) RequiresAuth() bool  { return true }

func (Plugin) New(user auth.User, ctx plugin.Context) tea.Model {
	return model{user: user, dataDir: ctx.DataDir}
}

type model struct {
	user    auth.User
	dataDir string
	files   []os.DirEntry
	cursor  int
	err     error
}

func (m model) Init() tea.Cmd {
	return func() tea.Msg {
		if m.dataDir == "" {
			return fmt.Errorf("no data directory configured")
		}
		files, err := os.ReadDir(m.dataDir)
		if err != nil {
			return err
		}
		return files
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case error:
		m.err = msg
		return m, nil
	case []os.DirEntry:
		m.files = msg
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.files)-1 {
				m.cursor++
			}
		case "q", "esc", "ctrl+c":
			return m, plugin.Exit
		}
	}
	return m, nil
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	itemStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	selStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
)

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error reading files: %v\n\npress any key to return", m.err)
	}
	s := titleStyle.Render("Files in your pod workspace:") + "\n\n"
	if len(m.files) == 0 {
		s += "  (no files found)\n"
	}
	for i, file := range m.files {
		cur := "  "
		style := itemStyle
		if i == m.cursor {
			cur = "❯ "
			style = selStyle
		}
		icon := "📄 "
		if file.IsDir() {
			icon = "📁 "
		}
		s += fmt.Sprintf("%s%s%s\n", cur, icon, style.Render(file.Name()))
	}
	s += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("↑/↓ move · q back")
	return lipgloss.NewStyle().Padding(1, 2).Render(s)
}
