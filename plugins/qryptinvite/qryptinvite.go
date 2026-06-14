// Package qryptinvite is the hub plugin that lets an authenticated member mint
// a single-use qrypt.chat anonymous invite. AgentBBS is the trusted issuer: the
// plugin signs a token with the operator's Ed25519 key, records it against the
// member's per-account quota, and prints the token + redeem URL. The separate
// qrypt.chat app verifies the signature and burns the jti on redeem.
//
// See internal/qryptinvite for the token format and docs/qrypt-invites.md.
package qryptinvite

import (
	"errors"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/plugin"
	qi "github.com/profullstack/agentbbs/internal/qryptinvite"
	"github.com/profullstack/agentbbs/internal/store"
)

// Plugin is the hub registration. It admits members only (guests have no
// account to quota against).
type Plugin struct{}

func (Plugin) ID() string          { return "qrypt-invite" }
func (Plugin) Title() string       { return "qrypt.chat invite" }
func (Plugin) Description() string { return "Mint an anonymous qrypt.chat signup invite" }
func (Plugin) RequiresAuth() bool  { return true }

func (Plugin) New(user auth.User, ctx plugin.Context) tea.Model {
	cfg := qi.ConfigFromEnv()
	m := model{user: user, store: ctx.Store, cfg: cfg}
	m.issue() // do the work once up front; the view just reports the result
	return m
}

type model struct {
	user  auth.User
	store store.Store
	cfg   qi.Config
	body  string // rendered result, ready to display
}

// issue runs the full flow: check quota, mint, record, build the output.
func (m *model) issue() {
	if m.user.Kind == auth.Guest || m.user.Name == "" {
		m.body = errStyle.Render("Sign in with your SSH key to mint an invite.")
		return
	}
	priv, err := m.cfg.PrivateKey()
	if err != nil {
		m.body = errStyle.Render("Invites are not configured on this host yet.\n") +
			dStyle.Render("  ("+err.Error()+")")
		return
	}
	used, err := m.store.QryptInviteCount(m.user.Name)
	if err != nil {
		m.body = errStyle.Render("Couldn't read your invite count: " + err.Error())
		return
	}
	if m.cfg.Quota > 0 && used >= m.cfg.Quota {
		m.body = errStyle.Render("You've used all your invites ") +
			dStyle.Render("("+strconv.Itoa(used)+"/"+strconv.Itoa(m.cfg.Quota)+"). Ask an operator for more.")
		return
	}

	token, jti, err := qi.Mint(m.cfg.IssuerID, priv, m.cfg.TTL)
	if err != nil {
		m.body = errStyle.Render("Mint failed: " + err.Error())
		return
	}
	if err := m.store.RecordQryptInvite(m.user.Name, jti, m.cfg.Quota); err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			m.body = errStyle.Render("You've used all your invites. Ask an operator for more.")
			return
		}
		m.body = errStyle.Render("Couldn't record the invite: " + err.Error())
		return
	}

	remaining := m.cfg.Quota - (used + 1)
	m.body = hStyle.Render("Your qrypt.chat anonymous invite") + "\n\n" +
		"  Redeem at:\n" +
		urlStyle.Render("  "+m.cfg.RedeemURLFor(token)) + "\n\n" +
		dStyle.Render("  Token (same thing, if you'd rather paste it):") + "\n" +
		"  " + token + "\n\n" +
		dStyle.Render("  Single-use · expires in "+m.cfg.TTL.String()+" · invites left: "+strconv.Itoa(max(remaining, 0)))
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		return m, plugin.Exit
	}
	return m, nil
}

func (m model) View() string {
	return lipgloss.NewStyle().Padding(1, 2).Render(
		m.body + "\n\n" + dStyle.Render("press any key to return"))
}

var (
	hStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	dStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	urlStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#60a5fa"))
)
