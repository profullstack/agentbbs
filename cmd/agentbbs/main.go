// Command agentbbs runs the AgentBBS SSH platform (PRD §4).
//
// SSH routes (by username):
//
//	ssh bbs@host    the BBS hub, guests welcome (play@/guest@ are aliases)
//	ssh <name>@host the hub as a member/agent (SSH key required)
//	ssh join@host   onboarding: registers your key, prints instructions,
//	                and disconnects — no session
//	ssh pod@host    your personal Linux pod (paid membership, $1/mo via coinpay)
//
// Subcommands:
//
//	agentbbs                 serve (default)
//	agentbbs grant-pod NAME MONTHS   manually extend a pod subscription
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	bm "github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
	gossh "golang.org/x/crypto/ssh"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/calls"
	"github.com/profullstack/agentbbs/internal/chat"
	"github.com/profullstack/agentbbs/internal/hub"
	"github.com/profullstack/agentbbs/internal/payments"
	"github.com/profullstack/agentbbs/internal/plugin"
	"github.com/profullstack/agentbbs/internal/pods"
	"github.com/profullstack/agentbbs/internal/sandbox"
	"github.com/profullstack/agentbbs/internal/store"
	"github.com/profullstack/agentbbs/plugins/about"
	"github.com/profullstack/agentbbs/plugins/arcade"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type app struct {
	st       store.Store
	pods     *pods.Manager // nil when no container engine on host
	registry []plugin.Plugin
	sandbox  *sandbox.Runner
	dataDir  string
	assets   string
	host     string // public hostname used in user-facing messages
}

func main() {
	dataDir := env("AGENTBBS_DATA", "./data")
	_ = os.MkdirAll(filepath.Join(dataDir, "users"), 0o755)

	st, err := store.Open(filepath.Join(dataDir, "agentbbs.db"))
	if err != nil {
		log.Fatal("store", "err", err)
	}
	defer st.Close()

	if len(os.Args) > 1 && os.Args[1] == "grant-pod" {
		grantPod(st, os.Args[2:])
		return
	}

	a := &app{
		st:      st,
		sandbox: sandbox.New(sandbox.Mode(env("AGENTBBS_SANDBOX", "auto"))),
		dataDir: dataDir,
		assets:  env("AGENTBBS_ASSETS", "./assets"),
		host:    env("AGENTBBS_HOST", "profullstack.com"),
	}
	a.registry = []plugin.Plugin{arcade.Plugin{}, about.Plugin{}}
	if m, err := pods.Detect(); err == nil {
		a.pods = m
		log.Info("pods enabled", "engine", m.Engine())
	} else {
		log.Warn("pods disabled", "reason", err)
	}
	log.Info("sandbox", "mode", a.sandbox.Mode())

	addr := env("AGENTBBS_ADDR", ":2222")
	srv, err := wish.NewServer(
		wish.WithAddress(addr),
		wish.WithHostKeyPath(filepath.Join(dataDir, "ssh", "host_ed25519")),
		// Keys are always accepted at the transport layer; identity and
		// authorization are resolved per-route in the session handler.
		wish.WithPublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool { return true }),
		// Keyless interactive auth admits guests (bbs@/play@) only.
		wish.WithKeyboardInteractiveAuth(func(ctx ssh.Context, _ gossh.KeyboardInteractiveChallenge) bool { return true }),
		wish.WithIdleTimeout(30*time.Minute),
		wish.WithMiddleware(
			a.router(),
			logging.Middleware(),
		),
	)
	if err != nil {
		log.Fatal("server", "err", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Info("agentbbs listening", "addr", addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("serve", "err", err)
			done <- syscall.SIGTERM
		}
	}()
	<-done
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// router dispatches a session by username (PRD §4.4 + pods addendum).
// The active-PTY guard applies to hub sessions only: join@ must work without
// a terminal (it prints and disconnects), and pod@ checks its PTY itself.
func (a *app) router() wish.Middleware {
	btMw := bm.Middleware(a.teaHandler)
	return func(next ssh.Handler) ssh.Handler {
		hubHandler := activeterm.Middleware()(btMw(next))
		return func(s ssh.Session) {
			user := strings.ToLower(s.User())
			code, isVideo := calls.RouteCode(user)
			switch {
			case auth.IsJoinName(user):
				a.handleJoin(s)
			case auth.IsPodName(user):
				a.handlePod(s)
			case isVideo:
				a.handleVideo(s, code)
			case user == "agent":
				a.handleChat(s)
			case a.handleFinger(s, user):
				// fingered an existing account that isn't the caller's; done.
			default:
				hubHandler(s)
			}
		}
	}
}

// teaHandler builds the hub model for guests, members, and agents.
func (a *app) teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	fp := auth.Fingerprint(s.PublicKey())
	username := strings.ToLower(s.User())

	var u auth.User
	if auth.IsGuestName(username) || fp == "" {
		// Keyless or explicitly anonymous → guest. Named accounts require a key.
		if !auth.IsGuestName(username) {
			wish.Println(s, "note: member access requires an SSH key; joining as guest.")
		}
		u = auth.User{Name: "guest", Kind: auth.Guest}
	} else {
		// A key maps to exactly one account: if this key is already
		// registered, that identity wins regardless of the username typed.
		su, found, err := a.st.UserByFingerprint(fp)
		if err == nil && !found {
			su, err = a.st.EnsureUser(username, string(auth.KindFor(username)), fp)
		}
		if errors.Is(err, store.ErrKeyMismatch) {
			wish.Fatalln(s, "that username is registered with a different SSH key.")
			return nil, nil
		} else if err != nil {
			wish.Fatalln(s, "account error: "+err.Error())
			return nil, nil
		}
		if su.Name != username {
			wish.Println(s, "note: this key belongs to "+su.Name+" — signed in as "+su.Name+".")
		}
		u = auth.User{Name: su.Name, Kind: auth.Kind(su.Kind), PubKeyFP: fp, StoreID: su.ID}
	}

	sessID, _ := a.st.RecordSession(u.StoreID, s.User(), remoteIP(s), "hub")
	go func() { <-s.Context().Done(); _ = a.st.EndSession(sessID) }()

	ctx := plugin.Context{Store: a.st, Sandbox: a.sandbox, AssetsDir: a.assets}
	if u.Kind != auth.Guest {
		ctx.DataDir = filepath.Join(a.dataDir, "users", u.Name)
		_ = os.MkdirAll(filepath.Join(ctx.DataDir, "wads"), 0o755)
	}
	return hub.New(u, ctx, a.registry), []tea.ProgramOption{tea.WithAltScreen()}
}

// handleJoin registers the visitor's key, prints instructions, disconnects
// ("it just shows the message and kicks them off the server").
func (a *app) handleJoin(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "join@ needs an SSH public key (try: ssh -i ~/.ssh/id_ed25519 join@"+a.host+")")
		_ = s.Exit(1)
		return
	}
	u, found, err := a.st.UserByFingerprint(fp)
	if err == nil && !found {
		name := "member-" + strings.ToLower(strings.TrimPrefix(fp, "SHA256:"))[:8]
		u, err = a.st.EnsureUser(name, string(auth.Member), fp)
	}
	if err != nil {
		wish.Fatalln(s, "registration error: "+err.Error())
		return
	}
	_, _ = a.st.RecordSession(u.ID, s.User(), remoteIP(s), "join")

	ref := payments.Reference("pod", fp)
	wish.Println(s, strings.Join([]string{
		"",
		"  Welcome to AgentBBS — you're registered.",
		"",
		"  account   " + u.Name,
		"  key       " + fp,
		"",
		"  BBS hub        ssh " + u.Name + "@" + a.host,
		"  Guest hub      ssh bbs@" + a.host,
		"",
		"  Personal pod (" + payments.PodPriceLabel + ", via CoinPay):",
		"    1. pay:    " + payments.PayCommand(ref),
		"    2. enter:  ssh pod@" + a.host,
		"",
	}, "\n"))
	_ = s.Exit(0)
}

// handlePod admits paid members into their personal container.
func (a *app) handlePod(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "pod@ needs your registered SSH key. New here? ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	u, found, err := a.st.UserByFingerprint(fp)
	if err != nil || !found {
		wish.Println(s, "key not registered — run: ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}

	until, ok, _ := a.st.PodPaidUntil(u.ID)
	if !ok || time.Now().After(until) {
		// One verification attempt against the coinpay CLI before refusing.
		ref := payments.Reference("pod", fp)
		if paid, checked := payments.Verify(ref); checked && paid {
			_ = a.st.GrantPod(u.ID, time.Now().Add(payments.PodTerm), ref)
		} else {
			wish.Println(s, strings.Join([]string{
				"",
				"  Pod membership required (" + payments.PodPriceLabel + ").",
				"",
				"  pay:   " + payments.PayCommand(ref),
				"  then:  ssh pod@" + a.host,
				"",
			}, "\n"))
			_ = s.Exit(1)
			return
		}
	}

	if a.pods == nil {
		wish.Println(s, "pods are temporarily unavailable on this host.")
		_ = s.Exit(1)
		return
	}
	sessID, _ := a.st.RecordSession(u.ID, s.User(), remoteIP(s), "pod")
	defer func() { _ = a.st.EndSession(sessID) }()
	if err := a.pods.Attach(s, u.Name); err != nil {
		wish.Println(s, "pod error: "+err.Error())
		_ = s.Exit(1)
	}
}

// handleVideo joins a PairUX call rendered as ASCII (docs/video.md).
// `video@` prompts for a code; `video-<code>@` joins directly. Codes are
// minted by PairUX — starting a call requires already having one.
func (a *app) handleVideo(s ssh.Session, code string) {
	identity := "ssh-guest"
	if fp := auth.Fingerprint(s.PublicKey()); fp != "" {
		if u, found, _ := a.st.UserByFingerprint(fp); found {
			identity = "ssh-" + u.Name
		}
	}
	sessID, _ := a.st.RecordSession(0, s.User(), remoteIP(s), "video")
	defer func() { _ = a.st.EndSession(sessID) }()
	if err := calls.Handle(s, code, identity); err != nil {
		wish.Println(s, "video: "+err.Error())
	}
}

// handleChat is the agent@ surface: talk to the operator's agent.
func (a *app) handleChat(s ssh.Session) {
	u := auth.User{Name: "guest-" + remoteIP(s), Kind: auth.Guest}
	if fp := auth.Fingerprint(s.PublicKey()); fp != "" {
		if su, found, _ := a.st.UserByFingerprint(fp); found {
			u = auth.User{Name: su.Name, Kind: auth.Kind(su.Kind), PubKeyFP: fp, StoreID: su.ID}
		}
	}
	sessID, _ := a.st.RecordSession(u.StoreID, s.User(), remoteIP(s), "agent")
	defer func() { _ = a.st.EndSession(sessID) }()
	if err := chat.Handle(s, a.st, u); err != nil {
		wish.Println(s, "chat: "+err.Error())
	}
}

// handleFinger prints a classic finger card when someone ssh's to an
// existing account name that isn't their own (e.g. ssh anthony@host).
// Returns false when the route should fall through to the hub.
func (a *app) handleFinger(s ssh.Session, username string) bool {
	if auth.IsGuestName(username) {
		return false
	}
	u, found, err := a.st.UserByName(username)
	if err != nil || !found {
		return false // unclaimed name → hub (claim flow)
	}
	if fp := auth.Fingerprint(s.PublicKey()); fp != "" && fp == u.PubKeyFP {
		return false // it's them → hub
	}

	lastSeen := "never"
	if t, ok, _ := a.st.LastSeen(u.ID); ok {
		lastSeen = t.Local().Format("2006-01-02 15:04 MST")
	}
	plan := "no plan."
	for _, p := range []string{
		filepath.Join(a.dataDir, "users", u.Name, ".plan"),
		filepath.Join(a.dataDir, "users", u.Name, "plan.txt"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			plan = strings.TrimSpace(string(b))
			break
		}
	}
	_, _ = a.st.RecordSession(0, s.User(), remoteIP(s), "finger")
	wish.Println(s, strings.Join([]string{
		"",
		"  Login: " + u.Name + "    Kind: " + u.Kind,
		"  Member since: " + u.CreatedAt.Format("2006-01-02") + "    Last seen: " + lastSeen,
		"  Plan:",
		"    " + strings.ReplaceAll(plan, "\n", "\n    "),
		"",
	}, "\n"))
	_ = s.Exit(0)
	return true
}

func grantPod(st store.Store, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agentbbs grant-pod <username> <months>")
		os.Exit(2)
	}
	months, err := strconv.Atoi(args[1])
	if err != nil || months < 1 {
		fmt.Fprintln(os.Stderr, "months must be a positive integer")
		os.Exit(2)
	}
	u, err := st.EnsureUser(strings.ToLower(args[0]), string(auth.Member), "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "user:", err)
		os.Exit(1)
	}
	until := time.Now().Add(time.Duration(months) * payments.PodTerm)
	if err := st.GrantPod(u.ID, until, "manual"); err != nil {
		fmt.Fprintln(os.Stderr, "grant:", err)
		os.Exit(1)
	}
	fmt.Printf("pod granted to %s until %s\n", u.Name, until.Format(time.RFC3339))
}

func remoteIP(s ssh.Session) string {
	if host, _, err := net.SplitHostPort(s.RemoteAddr().String()); err == nil {
		return host
	}
	return s.RemoteAddr().String()
}
