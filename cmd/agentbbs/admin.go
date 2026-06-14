package main

import (
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"

	"github.com/profullstack/agentbbs/internal/admin"
	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/calls"
	"github.com/profullstack/agentbbs/internal/plugin"
)

// liveReg is the in-memory registry of currently-connected SSH sessions. The
// DB sessions table is the historical audit trail; this is the live view the
// admin console lists and can disconnect (PRD §6 "terminate live sessions").
type liveReg struct {
	mu   sync.Mutex
	next int64
	m    map[int64]liveEntry
}

type liveEntry struct {
	s     ssh.Session
	user  string
	route string
	start time.Time
}

func newLiveReg() *liveReg { return &liveReg{m: map[int64]liveEntry{}} }

func (r *liveReg) add(s ssh.Session) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id := r.next
	r.m[id] = liveEntry{s: s, user: s.User(), route: routeLabel(s.User()), start: time.Now()}
	return id
}

func (r *liveReg) remove(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, id)
}

// idFor returns the registry id of an active session, or 0 if absent.
func (r *liveReg) idFor(s ssh.Session) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, e := range r.m {
		if e.s == s {
			return id
		}
	}
	return 0
}

// List implements admin.LiveSessions, newest connection first.
func (r *liveReg) List() []admin.Live {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]admin.Live, 0, len(r.m))
	for id, e := range r.m {
		out = append(out, admin.Live{ID: id, User: e.user, Remote: remoteIP(e.s), Route: e.route, Start: e.start})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// Kill closes a live session by id. Returns false if it is already gone.
func (r *liveReg) Kill(id int64) bool {
	r.mu.Lock()
	e, ok := r.m[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	_ = e.s.Close()
	return true
}

// track registers every connection in the live registry for its lifetime.
func (a *app) track() wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			id := a.live.add(s)
			defer a.live.remove(id)
			next(s)
		}
	}
}

// routeLabel classifies an SSH username into the route it dispatches to, for
// display in the admin sessions view.
func routeLabel(user string) string {
	user = strings.ToLower(user)
	switch {
	case auth.IsJoinName(user):
		return "join"
	case auth.IsDomainName(user):
		return "domain"
	case auth.IsPodName(user):
		return "pod"
	case auth.IsAdminName(user):
		return "admin"
	case user == "agent":
		return "agent"
	}
	if _, isVideo := calls.RouteCode(user); isVideo {
		return "video"
	}
	return "hub"
}

// adminTeaHandler builds the admin console for an authorized operator. It
// re-resolves identity here (not just at the route) so a direct invocation is
// still safe: non-admins get a one-line notice and disconnect.
func (a *app) adminTeaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	fp := auth.Fingerprint(s.PublicKey())
	var name string
	if fp != "" {
		if u, found, _ := a.st.UserByFingerprint(fp); found {
			name = u.Name
		}
	}
	if name == "" || !auth.IsAdmin(name) {
		wish.Println(s, "admin@ is restricted to operators.")
		_ = s.Exit(1)
		return nil, nil
	}
	u := auth.User{Name: name, Kind: auth.Member, PubKeyFP: fp}
	sessID, _ := a.st.RecordSession(0, s.User(), remoteIP(s), "admin")
	go func() { <-s.Context().Done(); _ = a.st.EndSession(sessID) }()

	env := admin.Env{
		Host:           a.host,
		Sandbox:        string(a.sandbox.Mode()),
		MailConfigured: a.mail.Configured(),
		Admins:         sortedKeys(auth.Admins()),
	}
	if a.pods != nil {
		env.PodsEngine = a.pods.Engine()
	}
	for _, p := range a.registry {
		env.Plugins = append(env.Plugins, admin.PluginInfo{ID: p.ID(), Title: p.Title()})
	}
	m := admin.New(u, a.st, a.live, a.live.idFor(s), env)
	return m, []tea.ProgramOption{tea.WithAltScreen()}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// enabledPlugins is a.registry minus any plugin an admin has switched off.
func (a *app) enabledPlugins() []plugin.Plugin {
	disabled, err := a.st.DisabledPlugins()
	if err != nil || len(disabled) == 0 {
		return a.registry
	}
	out := make([]plugin.Plugin, 0, len(a.registry))
	for _, p := range a.registry {
		if !disabled[p.ID()] {
			out = append(out, p)
		}
	}
	return out
}
