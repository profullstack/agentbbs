// Package plugin defines the AgentBBS plugin contract (PRD §4.3).
//
// A plugin is one interface implementation plus one registration in the hub.
// Plugins return control to the hub by emitting ExitMsg, never by quitting
// the session.
package plugin

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/sandbox"
	"github.com/profullstack/agentbbs/internal/store"
)

// Context carries the shared services a plugin may use.
type Context struct {
	Store   store.Store
	Sandbox *sandbox.Runner
	// DataDir is the per-user persistent directory (members/agents only;
	// empty for guests).
	DataDir string
	// AssetsDir is the read-only platform assets tree (wads, binaries).
	AssetsDir string
	// Host is the BBS hostname (e.g. bbs.profullstack.com), for building
	// member homepage URLs (https://Host/~name) and similar links.
	Host string
}

// Plugin is the only integration point between a feature and the hub.
type Plugin interface {
	// ID is a stable unique identifier, e.g. "arcade".
	ID() string
	// Title is the hub menu label.
	Title() string
	// Description is a one-line summary shown in the menu.
	Description() string
	// RequiresAuth reports whether guests are admitted.
	RequiresAuth() bool
	// New returns a fresh Bubble Tea model for one session.
	New(user auth.User, ctx Context) tea.Model
}

// ExitMsg is emitted by a plugin model to hand the session back to the hub.
type ExitMsg struct{}

// Exit is a convenience command for plugins.
func Exit() tea.Msg { return ExitMsg{} }
