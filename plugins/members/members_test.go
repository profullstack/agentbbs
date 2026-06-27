package members

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg {
	switch s {
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func newListModel() *model {
	return &model{
		state:  stList,
		marked: map[string]bool{},
		people: []person{{name: "alice"}, {name: "bob"}, {name: "carol"}},
	}
}

func TestRecipientsSingleVsGroup(t *testing.T) {
	m := newListModel()

	// No selection, single target → just that one.
	m.target = "bob"
	if got := m.recipients(); len(got) != 1 || got[0] != "bob" {
		t.Fatalf("single recipients = %v", got)
	}

	// A group selection overrides the single target, in directory order.
	m.marked = map[string]bool{"carol": true, "alice": true}
	got := m.recipients()
	if len(got) != 2 || got[0] != "alice" || got[1] != "carol" {
		t.Fatalf("group recipients = %v (want alice,carol in list order)", got)
	}
}

func TestSpaceTogglesAndSelectAll(t *testing.T) {
	m := newListModel()

	// Space marks the member under the cursor.
	m.cursor = 1 // bob
	m.handleKey(key(" "))
	if !m.marked["bob"] || len(m.marked) != 1 {
		t.Fatalf("after space: marked = %v", m.marked)
	}
	// Space again unmarks.
	m.handleKey(key(" "))
	if len(m.marked) != 0 {
		t.Fatalf("after second space: marked = %v", m.marked)
	}

	// 'a' selects everyone; 'a' again clears.
	m.handleKey(key("a"))
	if len(m.marked) != 3 {
		t.Fatalf("after select-all: marked = %v", m.marked)
	}
	m.handleKey(key("a"))
	if len(m.marked) != 0 {
		t.Fatalf("after clear-all: marked = %v", m.marked)
	}
}

func TestGroupComposeOpensWithSelection(t *testing.T) {
	m := newListModel()
	m.marked = map[string]bool{"alice": true, "bob": true}
	m.handleKey(key("m"))
	if m.state != stCompose {
		t.Fatalf("m with a group should open compose, state = %v", m.state)
	}
	if lbl := m.recipientLabel(); lbl != "alice, bob" {
		t.Fatalf("recipient label = %q", lbl)
	}
}
