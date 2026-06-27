// Command agentbbs runs the AgentBBS SSH platform (PRD §4).
//
// SSH routes (by username):
//
//	ssh bbs@host    the BBS hub, guests welcome (play@/guest@ are aliases)
//	ssh <name>@host the hub as a member/agent (SSH key required)
//	ssh join@host   onboarding: registers your key, confirms your email with an
//	                emailed code, then offers $99 Founding Lifetime (CoinPay)
//	ssh pod@host    your personal Linux pod — free for verified members
//	ssh domain@host point your own domain at your homepage (Premium; add/rm/list)
//	ssh <name>@host (from another account) prints a finger card for that member
//	ssh msg@host U  leave member U a message: `ssh msg@host U hi` or pipe stdin
//	ssh passwd@host reset ONE password across git + mail + chat (forgot-password;
//	                key-gated, password@ is an alias). See docs/credentials.md
//	ssh admin@host  the operator admin console ($AGENTBBS_ADMINS only;
//	                sysop@/root@ are aliases)
//	ssh game@host G AgentGames: play game G (e.g. ttt, c4) over NDJSON; rated,
//	                agent-vs-agent (also on wss://host/play). See docs/agentgames.md
//
// Subcommands:
//
//	agentbbs                              serve (default)
//	agentbbs grant-pod NAME MONTHS        manually extend a pod subscription
//	agentbbs map-domain DOMAIN NAME       map a custom domain to a homepage
//	agentbbs unmap-domain DOMAIN NAME     remove a custom-domain mapping
//	agentbbs mint-token NAME             issue a WebSocket API token for NAME
//	agentbbs qrypt-invite NAME           mint a qrypt.chat anonymous invite for NAME
//	agentbbs qrypt-issuer-keygen         print a fresh qrypt issuer seed + public key
//	agentbbs notify-creds [flags]        (re)email verified members their git +
//	                                     mailbox creds/links (preview unless --send)
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	bm "github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
	gossh "golang.org/x/crypto/ssh"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/brand"
	"github.com/profullstack/agentbbs/internal/calls"
	"github.com/profullstack/agentbbs/internal/chat"
	"github.com/profullstack/agentbbs/internal/files"
	"github.com/profullstack/agentbbs/internal/forgejo"
	"github.com/profullstack/agentbbs/internal/games"
	"github.com/profullstack/agentbbs/internal/hub"
	"github.com/profullstack/agentbbs/internal/ircpass"
	"github.com/profullstack/agentbbs/internal/mail"
	"github.com/profullstack/agentbbs/internal/mailbox"
	"github.com/profullstack/agentbbs/internal/mailu"
	"github.com/profullstack/agentbbs/internal/motd"
	"github.com/profullstack/agentbbs/internal/news"
	"github.com/profullstack/agentbbs/internal/payments"
	"github.com/profullstack/agentbbs/internal/plugin"
	"github.com/profullstack/agentbbs/internal/pods"
	"github.com/profullstack/agentbbs/internal/sandbox"
	"github.com/profullstack/agentbbs/internal/sites"
	"github.com/profullstack/agentbbs/internal/store"
	"github.com/profullstack/agentbbs/internal/tor"
	"github.com/profullstack/agentbbs/plugins/about"
	"github.com/profullstack/agentbbs/plugins/agentgames"
	"github.com/profullstack/agentbbs/plugins/arcade"
	"github.com/profullstack/agentbbs/plugins/hello"
	"github.com/profullstack/agentbbs/plugins/members"
	qryptinviteplugin "github.com/profullstack/agentbbs/plugins/qryptinvite"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envInt reads an integer environment variable, falling back to def.
func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

type app struct {
	st         store.Store
	pods       *pods.Manager // nil when no container engine on host
	sites      *sites.Manager
	registry   []plugin.Plugin
	sandbox    *sandbox.Runner
	mail       mail.Config
	mailu      *mailu.Client     // member mailbox provisioning (nil when unconfigured)
	mailDomain string            // email address domain, e.g. bbs.profullstack.com
	mailHost   string            // mail server host (IMAP/SMTP), e.g. mail.profullstack.com
	webmailURL string            // webmail (Roundcube) URL shown to members
	forgejo    forgejo.Config    // AgentGit git.profullstack.com account provisioning
	irc        ircpass.Config    // chat/IRC password reset bridge (privileged helper)
	live       *liveReg          // in-memory live-session registry (admin console)
	files      *files.Service    // SFTP file storage (nil when AGENTBBS_FILES=0)
	gamesReg   *games.Registry   // AgentGames catalog
	mm         *games.Matchmaker // AgentGames matchmaker (agent-vs-agent)
	dataDir    string
	assets     string
	host       string // public hostname used in user-facing messages
	newsAddr   string // loopback NNTP address the news@ reader dials
}

// Version is the agentbbs stack release, surfaced via `agentbbs version` and
// logged at startup. Bump on each release of the bbs.profullstack.com stack.
const Version = "v0.1.0"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("agentbbs " + Version)
		return
	}
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
	if len(os.Args) > 1 && (os.Args[1] == "map-domain" || os.Args[1] == "unmap-domain") {
		domainCmd(st, dataDir, os.Args[1], os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "mint-token" {
		mintToken(st, os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "qrypt-invite" {
		qryptInviteCmd(st, os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "notify-creds" {
		notifyCreds(st, os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "provision-user" {
		provisionUser(st, os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "qrypt-issuer-keygen" {
		qryptIssuerKeygen()
		return
	}

	host := env("AGENTBBS_HOST", "bbs.profullstack.com")
	// Member email addresses are <name>@<addr-domain> (e.g. bbs.profullstack.com).
	// The mail server (IMAP/SMTP/webmail) lives on a dedicated host
	// (mail.profullstack.com); the apex is reserved for corporate mail.
	mailHost := env("AGENTBBS_MAIL_DOMAIN", "mail.profullstack.com")
	mailDomain := env("AGENTBBS_MAIL_ADDR_DOMAIN", host)
	mailuClient := mailu.NewFromEnv()
	if !mailuClient.Configured() {
		mailuClient = nil
	}
	a := &app{
		st:         st,
		sandbox:    sandbox.New(sandbox.Mode(env("AGENTBBS_SANDBOX", "auto"))),
		mail:       mail.ConfigFromEnv(),
		mailu:      mailuClient,
		mailDomain: mailDomain,
		mailHost:   mailHost,
		webmailURL: env("AGENTBBS_WEBMAIL_URL", "https://"+mailHost),
		forgejo:    forgejo.ConfigFromEnv(),
		irc:        ircpass.ConfigFromEnv(),
		live:       newLiveReg(),
		dataDir:    dataDir,
		assets:     env("AGENTBBS_ASSETS", "./assets"),
		host:       host,
	}
	a.gamesReg = games.Catalog()
	a.mm = games.NewMatchmaker(a.gamesReg, a.st,
		time.Duration(envInt("AGENTBBS_GAME_MOVE_TIMEOUT", 15))*time.Second,
		time.Duration(envInt("AGENTBBS_GAME_QUEUE_WAIT", 120))*time.Second)
	a.registry = []plugin.Plugin{arcade.Plugin{}, agentgames.New(a.gamesReg), members.Plugin{}, qryptinviteplugin.Plugin{}, about.Plugin{}, hello.Plugin{}}

	// Files (SFTP): per-user workspaces + a shared public area, reached over the
	// :22 listener via `sftp files@<host>` (docs/files.md). Disable with
	// AGENTBBS_FILES=0. The in-BBS browser is a hub plugin; the operator
	// management TUI is the sftp@ route.
	if env("AGENTBBS_FILES", "1") == "1" {
		fsvc, err := files.New(a.st, files.Config{
			Root:         filepath.Join(dataDir, "files"),
			DefaultQuota: int64(envInt("AGENTBBS_FILES_QUOTA_MB", 1024)) << 20,
		})
		if err != nil {
			log.Fatal("files", "err", err)
		}
		a.files = fsvc
		a.registry = append(a.registry, files.NewPlugin(fsvc))
	}

	// Custom domains: maintain the symlink farm Caddy serves and answer its
	// on-demand-TLS "ask" query so certs are only issued for mapped domains.
	if sm, err := sites.NewManager(st, dataDir); err != nil {
		log.Warn("custom domains disabled", "err", err)
	} else {
		a.sites = sm
		if err := sm.Sync(); err != nil {
			log.Warn("domain symlink sync", "err", err)
		}
		askAddr := env("AGENTBBS_ASK_ADDR", "127.0.0.1:8081")
		go func() {
			log.Info("on-demand-tls ask listening", "addr", askAddr)
			if err := sm.ServeAsk(askAddr); err != nil {
				log.Error("ask server", "err", err)
			}
		}()
	}

	if m, err := pods.Detect(filepath.Join(dataDir, "users")); err == nil {
		a.pods = m
		log.Info("pods enabled", "engine", m.Engine())
	} else {
		log.Warn("pods disabled", "reason", err)
	}
	log.Info("sandbox", "mode", a.sandbox.Mode())

	// Shared Message of the Day from profullstack.com — shown on the hub (and on
	// IRC via Ergo's MOTD). Cached + refreshed in the background so it never
	// blocks session start. Override the source with AGENTBBS_MOTD_URL (empty
	// disables remote fetch).
	motd.Start(context.Background(), env("AGENTBBS_MOTD_URL", motd.DefaultURL), 30*time.Minute)

	// Email confirmation endpoint (the link in the join@ verification mail).
	// Loopback only; Caddy reverse-proxies /verify to it. Separate from the
	// on-demand-TLS ask server above.
	verifyAddr := env("AGENTBBS_HTTP_ADDR", "127.0.0.1:8088")
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/verify", a.handleVerify)
		mux.HandleFunc("/irc-auth", a.handleIRCAuth) // Ergo auth-script: members-only gate
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
		log.Info("verify endpoint listening", "addr", verifyAddr)
		srv := &http.Server{Addr: verifyAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			log.Error("verify server", "err", err)
		}
	}()

	// AgentGames WebSocket endpoint (twin of the game@ SSH route). Loopback;
	// Caddy proxies wss://host/play to it.
	go a.serveGameWS(env("AGENTBBS_GAME_WS_ADDR", "127.0.0.1:8090"))

	// Web file browser (files.<host>): webmail-password login over the same
	// /me + /public storage as SFTP. Loopback; Caddy proxies files.<host> to it.
	a.startFilesWeb()

	// News (NNTP) server: the members-only Usenet network (docs/news.md). The
	// loopback plaintext listener backs the in-BBS news@ reader; the public
	// NNTPS listener (:563, TLS) serves desktop newsreaders and agents. Free for
	// every registered member, like irc@. Disable with AGENTBBS_NEWS=0.
	a.newsAddr = env("AGENTBBS_NEWS_ADDR", news.DefaultAddr)
	if env("AGENTBBS_NEWS", "1") == "1" {
		newsHost := env("AGENTBBS_NEWS_HOST", "news."+strings.TrimPrefix(host, "bbs."))
		ns := news.New(st, newsHost)
		if err := ns.SeedGroups(news.ParseGroups(os.Getenv("AGENTBBS_NEWS_GROUPS"))); err != nil {
			log.Warn("news seed groups", "err", err)
		}
		go func() {
			log.Info("news loopback listening", "addr", a.newsAddr)
			if err := ns.ServeLoopback(context.Background(), a.newsAddr); err != nil {
				log.Error("news loopback", "err", err)
			}
		}()
		if cert, key := os.Getenv("AGENTBBS_NEWS_TLS_CERT"), os.Getenv("AGENTBBS_NEWS_TLS_KEY"); cert != "" && key != "" {
			tlsAddr := env("AGENTBBS_NEWS_TLS_ADDR", ":563")
			go func() {
				log.Info("news NNTPS listening", "addr", tlsAddr, "host", newsHost)
				if err := ns.ServeTLS(context.Background(), tlsAddr, cert, key); err != nil {
					log.Error("news nntps", "err", err)
				}
			}()
		} else {
			log.Warn("news NNTPS disabled (no AGENTBBS_NEWS_TLS_CERT/KEY) — loopback news@ reader still works")
		}
	}

	addr := env("AGENTBBS_ADDR", ":2222")
	opts := []ssh.Option{
		wish.WithAddress(addr),
		wish.WithHostKeyPath(filepath.Join(dataDir, "ssh", "host_ed25519")),
		// Keys are always accepted at the transport layer; identity and
		// authorization are resolved per-route in the session handler.
		wish.WithPublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool { return true }),
		// Keyless interactive auth admits guests (bbs@/play@) only.
		wish.WithKeyboardInteractiveAuth(func(ctx ssh.Context, _ gossh.KeyboardInteractiveChallenge) bool { return true }),
		wish.WithIdleTimeout(30 * time.Minute),
		wish.WithMiddleware(
			a.router(),
			a.track(), // register every session for the admin console
			logging.Middleware(),
		),
	}
	// SFTP rides the same :22 listener as a subsystem; identity is the SSH key,
	// so `sftp files@<host>` works with the member's login key.
	if a.files != nil {
		opts = append(opts, wish.WithSubsystem("sftp", a.files.Subsystem()))
	}
	srv, err := wish.NewServer(opts...)
	if err != nil {
		log.Fatal("server", "err", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Info("agentbbs listening", "addr", addr, "version", Version)
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
// The active-PTY guard applies to hub sessions only; join@ and pod@ check their
// own PTY (both are interactive) so they can return a tailored hint instead of
// activeterm's opaque rejection.
func (a *app) router() wish.Middleware {
	btMw := bm.Middleware(a.teaHandler)
	adminMw := bm.Middleware(a.adminTeaHandler)
	filesAdminMw := bm.Middleware(a.filesAdminTeaHandler)
	return func(next ssh.Handler) ssh.Handler {
		hubHandler := activeterm.Middleware()(btMw(next))
		adminHandler := activeterm.Middleware()(adminMw(next))
		filesAdminHandler := activeterm.Middleware()(filesAdminMw(next))
		return func(s ssh.Session) {
			user := strings.ToLower(s.User())
			code, isVideo := calls.RouteCode(user)
			switch {
			case auth.IsJoinName(user):
				a.handleJoin(s)
			case auth.IsDomainName(user):
				a.handleDomain(s)
			case auth.IsAdminName(user):
				adminHandler(s)
			case auth.IsGameName(user):
				a.handleGame(s)
			case auth.IsPodName(user):
				a.handlePod(s)
			case auth.IsTorURLName(user):
				a.handleTorURL(s)
			case auth.IsTorIRCName(user):
				a.handleTorIRC(s)
			case auth.IsTorName(user):
				a.handleTorCmd(s)
			case auth.IsNewsName(user):
				a.handleNews(s)
			case auth.IsMailName(user):
				a.handleMail(s)
			case auth.IsFilesAdminName(user):
				filesAdminHandler(s)
			case auth.IsMsgName(user):
				a.handleMsg(s)
			case auth.IsPasswdName(user):
				a.handlePasswd(s)
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

// bbsBanner is the ASCII brand mark shown atop the hub menu and the join@ flow.
var bbsBanner = brand.Logo()

var bannerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e11d2a"))

// hubMOTD is the welcome message shown in a box on the hub menu. The body is
// operator-overridable via AGENTBBS_MOTD; it is tailored for guests vs members.
func (a *app) hubMOTD(u auth.User) string {
	body := env("AGENTBBS_MOTD",
		"A terminal BBS for humans & AI agents.\nGames · IRC · News · a Linux pod · your own homepage.")
	var s string
	if u.Kind == auth.Guest {
		s = "You're browsing as a guest.\n" + body +
			"\nssh join@" + a.host + " to claim a username, a pod & a homepage."
	} else {
		welcome := "Welcome back, " + u.Name + "."
		if n, err := a.st.UnreadCount(u.Name); err == nil && n > 0 {
			welcome += fmt.Sprintf("  📬 %d unread — open Members ▸ inbox (i).", n)
		}
		s = welcome + "\n" + body
	}
	// Append the shared daily Message of the Day from profullstack.com, if loaded.
	if m := motd.Current(); m != "" {
		s += "\n\n" + m
	}
	return s
}

// teaHandler builds the hub model for guests, members, and agents.
func (a *app) teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	fp := auth.Fingerprint(s.PublicKey())
	username := strings.ToLower(s.User())

	var u auth.User
	var su store.User
	guest := auth.IsGuestName(username) || fp == ""
	if guest {
		// Keyless or explicitly anonymous → guest. Named accounts require a key.
		if !auth.IsGuestName(username) {
			wish.Println(s, "note: member access requires an SSH key; joining as guest.")
		}
		u = auth.User{Name: "guest", Kind: auth.Guest}
	} else {
		// A key maps to exactly one account: if this key is already
		// registered, that identity wins regardless of the username typed.
		var found bool
		var err error
		su, found, err = a.st.UserByFingerprint(fp)
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
		if su.Banned {
			wish.Fatalln(s, "this account is suspended. Contact an operator if you think this is a mistake.")
			return nil, nil
		}
		if su.Name != username {
			wish.Println(s, "note: this key belongs to "+su.Name+" — signed in as "+su.Name+".")
		}
		// Catch a premium payment that settled since their last visit (silent;
		// provisions their @host email alias on the transition).
		a.ensurePremium(&su)
		u = auth.User{Name: su.Name, Kind: auth.Kind(su.Kind), PubKeyFP: fp, StoreID: su.ID}
		// Backfill the git.profullstack.com account + SSH key on login. Idempotent
		// and off the hot path: members who verified before AgentGit existed (or
		// before their key was registered) get provisioned on their next visit.
		if su.EmailVerified {
			suCopy, key := su, authorizedKey(s)
			go a.provisionGit(&suCopy, key)
		}
	}

	sessID, _ := a.st.RecordSession(u.StoreID, s.User(), remoteIP(s), "hub")
	go func() { <-s.Context().Done(); _ = a.st.EndSession(sessID) }()

	ctx := plugin.Context{Store: a.st, Sandbox: a.sandbox, AssetsDir: a.assets, Host: a.host}
	if pty, _, ok := s.Pty(); ok {
		ctx.Term = pty.Term // ncurses arcade games need the client's TERM
	}
	if u.Kind != auth.Guest {
		ctx.DataDir = filepath.Join(a.dataDir, "users", u.Name)
		_ = os.MkdirAll(filepath.Join(ctx.DataDir, "wads"), 0o755)
		// tilde.town-style web home: served at https://<host>/~<name> by the
		// Caddy front end (see setup.sh). Seed an editable starter page so the
		// URL works the moment a member first signs in.
		seedHomepage(filepath.Join(ctx.DataDir, "public_html"), u.Name, a.host)
	}
	return hub.New(u, ctx, a.enabledPlugins(), a.sessionApps(s, su, guest), bbsBanner, a.hubMOTD(u)), []tea.ProgramOption{tea.WithAltScreen()}
}

// sessionExec adapts a func to tea.ExecCommand so the hub can run a
// terminal-takeover feature (pod shell, IRC, news, mail, Tor) via tea.Exec and
// return to the menu afterwards. The feature reads and writes the ssh.Session
// directly, so the stream hooks are no-ops.
type sessionExec struct{ run func() error }

func (e sessionExec) Run() error          { return e.run() }
func (e sessionExec) SetStdin(io.Reader)  {}
func (e sessionExec) SetStdout(io.Writer) {}
func (e sessionExec) SetStderr(io.Writer) {}

// sessionApps builds the hub's terminal-takeover entries (pod, IRC, news, Tor)
// so a member reaches everything from one `ssh <name>@host` login instead of
// separate `ssh pod@`/`irc@`/`news@`/`tor@` connections (which still work as
// aliases, mainly for bots). Each entry is gated by membership/verification/plan
// and shown locked with a reason when unavailable.
func (a *app) sessionApps(s ssh.Session, su store.User, guest bool) []hub.SessionApp {
	membersOnly := "members only — register first: ssh join@" + a.host
	apps := make([]hub.SessionApp, 0, 4)

	// Pod — free for verified members.
	podLock := ""
	switch {
	case guest:
		podLock = membersOnly
	case env("AGENTBBS_REQUIRE_VERIFIED_EMAIL", "1") != "0" && !su.EmailVerified:
		podLock = "confirm your email first — re-run: ssh join@" + a.host
	case a.pods == nil:
		podLock = "pods are temporarily unavailable on this host"
	}
	apps = append(apps, hub.SessionApp{
		Title:       "Shell",
		Description: "drop straight into a bash shell in your pod",
		Locked:      podLock,
		Cmd:         sessionExec{run: func() error { return a.pods.Exec(s, su.Name, []string{"bash", "-l"}) }},
	})
	apps = append(apps, hub.SessionApp{
		Title:       "Pod",
		Description: "your own Linux pod — attach to its main session",
		Locked:      podLock,
		Cmd:         sessionExec{run: func() error { return a.pods.Attach(s, su.Name) }},
	})

	// IRC is members-only but accessed with an external client (or web) at
	// irc.profullstack.com — not an in-BBS route — so it's not a hub menu item.

	// News — free for any registered member.
	newsLock := ""
	if guest {
		newsLock = membersOnly
	}
	apps = append(apps, hub.SessionApp{
		Title:       "News",
		Description: "members-only Usenet/NNTP discussion",
		Locked:      newsLock,
		Cmd:         sessionExec{run: func() error { return a.runNews(s, su.Name) }},
	})

	// Mail — a free benefit of membership: the AgentMail TUI for your
	// <name>@<mailDomain> mailbox.
	mailLock := ""
	switch {
	case guest:
		mailLock = membersOnly
	case !a.mailEnabled():
		mailLock = "mail is temporarily unavailable on this host"
	}
	apps = append(apps, hub.SessionApp{
		Title:       "Mail",
		Description: "your " + a.mailAddress(su.Name) + " mailbox",
		Locked:      mailLock,
		Cmd: sessionExec{run: func() error {
			// Make sure the mailbox exists before opening it.
			_ = a.ensureMailbox(su)
			c, err := a.mailClientFor(su)
			if err != nil {
				return err
			}
			defer c.Close()
			return mailbox.RunReader(s, c)
		}},
	})

	// Tor — a Founding Lifetime Member perk: a torsocks shell in the pod.
	torLock := ""
	switch {
	case guest:
		torLock = membersOnly
	case !su.Premium:
		torLock = "Founding Lifetime Member feature ($99 one-time) — upgrade: ssh join@" + a.host
	case a.pods == nil:
		torLock = "pods are temporarily unavailable on this host"
	}
	apps = append(apps, hub.SessionApp{
		Title:       "Tor shell",
		Description: "a torsocks shell in your pod (everything over Tor)",
		Locked:      torLock,
		Cmd:         sessionExec{run: func() error { return a.pods.Exec(s, su.Name, tor.Torsocks([]string{"bash", "-l"})) }},
	})

	return apps
}

// readLine reads one line of interactive input from an SSH session that is
// running under a client-allocated PTY. That detail is the whole reason this
// helper exists: when the client requests a PTY (which `ssh join@host` does by
// default) it puts its OWN terminal into raw mode, so it sends raw keystrokes —
// Enter arrives as '\r', not '\n' — and does NO local echo. bufio.ReadString
// ('\n') therefore blocks forever (the '\n' never comes) and the user sees a
// dead prompt. So we read byte-by-byte, accept either '\r' or '\n' as the line
// terminator, handle backspace, and echo printable bytes back ourselves.
func readLine(s ssh.Session, in *bufio.Reader) (string, error) {
	var b []byte
	for {
		c, err := in.ReadByte()
		if err != nil {
			return "", err
		}
		switch c {
		case '\r', '\n':
			wish.Print(s, "\r\n")
			return string(b), nil
		case 0x03, 0x04: // Ctrl-C / Ctrl-D: treat as abort
			return "", io.EOF
		case 0x7f, '\b': // DEL / backspace: erase last char on screen too
			if len(b) > 0 {
				b = b[:len(b)-1]
				wish.Print(s, "\b \b")
			}
		default:
			if c >= 0x20 { // printable byte; ignore other control codes
				b = append(b, c)
				wish.Print(s, string(c))
			}
		}
	}
}

// readSecret reads a line like readLine but echoes '*' for each character instead
// of the character itself, so a password isn't shown on screen. Same raw-PTY
// handling (accept '\r' or '\n', handle backspace, abort on Ctrl-C/Ctrl-D).
func readSecret(s ssh.Session, in *bufio.Reader) (string, error) {
	var b []byte
	for {
		c, err := in.ReadByte()
		if err != nil {
			return "", err
		}
		switch c {
		case '\r', '\n':
			wish.Print(s, "\r\n")
			return string(b), nil
		case 0x03, 0x04: // Ctrl-C / Ctrl-D: treat as abort
			return "", io.EOF
		case 0x7f, '\b':
			if len(b) > 0 {
				b = b[:len(b)-1]
				wish.Print(s, "\b \b")
			}
		default:
			if c >= 0x20 {
				b = append(b, c)
				wish.Print(s, "*")
			}
		}
	}
}

// handleJoin runs onboarding interactively in one SSH session: register the
// visitor's key, confirm their email with a code we email them, then offer the
// $99 Founding Lifetime membership (CoinPay). It then disconnects.
func (a *app) handleJoin(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "join@ needs an SSH public key (try: ssh -i ~/.ssh/id_ed25519 join@"+a.host+")")
		_ = s.Exit(1)
		return
	}
	// Onboarding reads an email and a verification code interactively, so it
	// needs a terminal. Without a PTY the prompts would block forever (e.g. ssh
	// launched with no controlling tty, which delegates prompts to ssh-askpass).
	// Fail fast with a hint instead of hanging.
	if _, _, hasPty := s.Pty(); !hasPty {
		wish.Println(s, "join@ is interactive — reconnect with a terminal: ssh -t join@"+a.host)
		_ = s.Exit(1)
		return
	}
	wish.Println(s, "\n"+bannerStyle.Render(bbsBanner))
	in := bufio.NewReader(s)

	u, found, err := a.st.UserByFingerprint(fp)
	if err != nil {
		wish.Fatalln(s, "registration error: "+err.Error())
		return
	}
	if !found {
		// New key: show the acceptable-use terms and require acceptance before
		// creating the account, then let the visitor pick their own handle (a
		// returning key keeps the name it already chose).
		wish.Println(s, "\n  Welcome to AgentBBS — let's set up your account.")
		if !a.acceptTerms(s, in) {
			wish.Println(s, "\n  You must accept the terms to register — no account was created.")
			_ = s.Exit(1)
			return
		}
		if u, err = a.registerNewMember(s, in, fp); err != nil {
			wish.Fatalln(s, "registration error: "+err.Error())
			return
		}
	}
	_, _ = a.st.RecordSession(u.ID, s.User(), remoteIP(s), "join")

	wish.Println(s, "\n"+strings.Join([]string{
		"  account   " + u.Name,
		"  key       " + fp,
	}, "\n"))

	// 1) email -> emailed code -> enter code. A verified account is a free
	// member: it gets a Docker pod, a mailbox, IRC/news, and a /~name homepage.
	if !u.EmailVerified {
		if !a.verifyEmailInteractive(s, in, &u) {
			_ = s.Exit(1)
			return
		}
		a.notifySignup(u)
	}

	// Every verified member gets a homepage at https://<host>/~<name> and a
	// mailbox at <name>@<mailDomain> (best-effort; mail is a bonus, never a gate).
	seedHomepage(filepath.Join(a.dataDir, "users", u.Name, "public_html"), u.Name, a.host)
	_ = a.ensureMailbox(u)
	// Give them a webmail password so free members can log into webmail. The
	// in-BBS reader uses the gateway master user and needs no password, but
	// Roundcube does. (Re)set on each join@; they can change it in webmail.
	webmailPW := a.setWebmailPassword(u)

	includes := []string{
		"  You're in. One login gets you everything — no other servers to ssh into:",
		"",
		"    ssh " + u.Name + "@" + a.host,
		"",
		"  Inside, free membership includes:",
		"    • your own Linux pod (a full shell)",
		"    • email           " + a.mailAddress(u.Name) + "  (pick “Mail” in the hub)",
		"    • IRC chat + Usenet/news (members-only)",
		"    • the arcade & games",
		"    • your homepage   https://" + a.host + "/~" + u.Name,
	}
	if a.webmailURL != "" && webmailPW != "" {
		includes = append(includes,
			"",
			"  Webmail (read your mail in a browser):",
			"    • url        "+a.webmailURL,
			"    • login      "+a.mailAddress(u.Name),
			"    • password   "+webmailPW+"   (change it in webmail Settings)",
		)
	} else if a.webmailURL != "" {
		includes = append(includes, "    • webmail         "+a.webmailURL)
	}
	wish.Println(s, "\n"+strings.Join(includes, "\n"))

	// 2) Founding Lifetime ($99 one-time): custom domains + Tor shell.
	a.offerPremium(s, in, &u)
	_ = s.Exit(0)
}

// acceptTerms shows the acceptable-use terms and requires the visitor to type
// "agree" before an account is created. AgentBBS is for lawful use only; illegal
// activity is grounds for an immediate, permanent ban. Returns true on acceptance.
func (a *app) acceptTerms(s ssh.Session, in *bufio.Reader) bool {
	wish.Println(s, "\n"+strings.Join([]string{
		"  Terms of use — please read before you join:",
		"    • AgentBBS is for LAWFUL use only. Illegal activity is not permitted",
		"      and will result in an immediate, permanent ban — and may be reported",
		"      to the relevant authorities.",
		"    • Don't abuse the service, other members, or the shared infrastructure,",
		"      and don't use it to harm others.",
		"    • You are responsible for everything you — and any agents you run —",
		"      do here.",
	}, "\n"))
	wish.Print(s, "\n  Type \"agree\" to accept and continue: ")
	line, err := readLine(s, in)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "agree", "i agree", "agreed", "yes", "y":
		return true
	default:
		return false
	}
}

// fpToken derives up to n lowercase alphanumeric characters from an SSH key
// fingerprint, for use in a default username/home-dir. The raw base64
// fingerprint can contain '+' and '/', which are unsafe as a filesystem token,
// so we keep only [a-z0-9].
func fpToken(fp string, n int) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return -1
		}
	}, strings.ToLower(strings.TrimPrefix(fp, "SHA256:")))
	if len(cleaned) > n {
		cleaned = cleaned[:n]
	}
	return cleaned
}

// registerNewMember asks the visitor to choose a username, then creates their
// member account under it. The name is sanitized to the hub/subdomain charset,
// rejected if reserved, and must be free; pressing enter accepts a generated
// member-<fp8> default. The chosen name is also the member's pod home
// (/home/<name>), so it must be a safe shell/filesystem token — the sanitizer
// (and the default below) keep it to lowercase [a-z0-9-]. Returns the user.
func (a *app) registerNewMember(s ssh.Session, in *bufio.Reader, fp string) (store.User, error) {
	def := "member-" + fpToken(fp, 8)
	wish.Println(s, "\n  Pick a username — letters, numbers and dashes, 3–20 chars.")
	wish.Println(s, "  It's your handle for  ssh <name>@"+a.host+"  (your pod home is /home/<name>).")

	for tries := 0; tries < 5; tries++ {
		wish.Print(s, "\n  Username ["+def+"]: ")
		line, err := readLine(s, in)
		if err != nil {
			return store.User{}, err
		}
		raw := strings.TrimSpace(line)
		if raw == "" {
			return a.st.EnsureUser(def, string(auth.Member), fp)
		}
		name, ok := auth.SanitizeUsername(raw)
		switch {
		case !ok && auth.IsReservedName(name):
			wish.Println(s, "  \""+name+"\" is reserved — pick another.")
			continue
		case !ok:
			wish.Println(s, "  needs 3–20 chars of letters, numbers or dashes — try again.")
			continue
		}
		if _, taken, err := a.st.UserByName(name); err != nil {
			return store.User{}, err
		} else if taken {
			wish.Println(s, "  \""+name+"\" is taken — try another.")
			continue
		}
		return a.st.EnsureUser(name, string(auth.Member), fp)
	}
	wish.Println(s, "  Keeping "+def+" for now.")
	return a.st.EnsureUser(def, string(auth.Member), fp)
}

// verifyEmailInteractive collects an email, emails a 6-digit code, and prompts
// the visitor to type it back. It updates *u and returns true once verified.
func (a *app) verifyEmailInteractive(s ssh.Session, in *bufio.Reader, u *store.User) bool {
	var email string
	for tries := 0; tries < 3; tries++ {
		wish.Print(s, "\n  Email: ")
		line, err := readLine(s, in)
		if err != nil {
			return false
		}
		if e := strings.TrimSpace(line); validEmail(e) {
			email = e
			break
		}
		wish.Println(s, "  that doesn't look like an email — try again.")
	}
	if email == "" {
		wish.Println(s, "  No valid email — run  ssh join@"+a.host+"  again when ready.")
		return false
	}

	code := randCode()
	if err := a.st.SetEmailVerification(u.ID, email, code); err != nil {
		log.Error("set verification", "err", err)
		wish.Println(s, "  couldn't save your email; please retry.")
		return false
	}
	switch {
	case a.mail.Configured():
		if err := a.mail.Send(email, "Your AgentBBS confirmation code", verifyCodeEmailBody(u.Name, code)); err != nil {
			log.Error("send code", "err", err)
			wish.Println(s, "  couldn't email the code; please retry or contact an admin.")
			return false
		}
		wish.Println(s, "  Sent a 6-digit code to "+email+".")
	default:
		// No SMTP configured yet: show the code in-session so the box is still
		// usable. Set AGENTBBS_SMTP_* in production so codes are emailed instead.
		log.Warn("smtp not configured — showing join code in session", "email", email)
		wish.Println(s, "  (email isn't configured on this host yet — here is your code)")
		wish.Println(s, "  code: "+code)
	}

	for tries := 0; tries < 3; tries++ {
		wish.Print(s, "  Enter the code: ")
		line, err := readLine(s, in)
		if err != nil {
			return false
		}
		vu, ok, err := a.st.ConfirmEmailCode(u.ID, strings.TrimSpace(line))
		if err != nil {
			log.Error("confirm code", "err", err)
			wish.Println(s, "  verification error; please retry.")
			return false
		}
		if ok {
			*u = vu
			a.provisionGit(u, authorizedKey(s))
			wish.Println(s, "  Email confirmed ✓")
			return true
		}
		wish.Println(s, "  that code didn't match — try again.")
	}
	wish.Println(s, "  Too many attempts — run  ssh join@"+a.host+"  again for a fresh code.")
	return false
}

// ensurePremium upgrades *u to premium if its CoinPay charge has settled. It is
// silent (no session output) so it is safe to call from the hub. Returns the
// current premium state. Email is no longer a premium perk — every verified
// member gets a mailbox (see ensureMailbox) — so this only unlocks custom
// domains and the Tor shell.
func (a *app) ensurePremium(u *store.User) bool {
	if u.Premium {
		return true
	}
	// Verify the CoinPay payment we created for them (if any) has settled.
	if u.PremiumPayID == "" {
		return false
	}
	if paid, checked := payments.VerifyPremium(u.PremiumPayID); !checked || !paid {
		return false
	}
	if err := a.st.GrantPremium(u.ID, u.PremiumPayID); err != nil {
		log.Error("grant premium", "err", err)
		return false
	}
	u.Premium = true
	return true
}

// mailWelcomeEmailBody is the plain-text email sent when a member's @host
// mailbox alias is provisioned: their new address and the webmail link. Used by
// the `notify-creds` backfill command (see notifycreds.go).
func mailWelcomeEmailBody(name, address, webmail string) string {
	b := "Hi " + name + ",\n\n" +
		"Your member mailbox is live:\n\n" +
		"    " + address + "\n\n" +
		"Mail sent there forwards to this address.\n"
	if webmail != "" {
		b += "\nRead and send from the webmail interface here:\n\n" +
			"    " + webmail + "\n"
	}
	b += "\nIf you didn't request this, you can ignore this email.\n"
	return b
}

// showPremiumWelcome prints a premium member's perks: custom domains and the
// in-hub Tor shell. (Email is free for all members — see the join@ summary.)
func (a *app) showPremiumWelcome(s ssh.Session, u store.User) {
	lines := []string{
		"",
		"  ★ Founding Lifetime Member — thanks! Your bonus perks:",
		"",
		"  domains    ssh domain@" + a.host + " add <yourdomain.com>",
		"  tor        pick “Tor shell” in the hub: ssh " + u.Name + "@" + a.host,
		"",
	}
	wish.Println(s, strings.Join(lines, "\n"))
}

// offerPremium pitches the $99 Founding Lifetime membership — custom domains and
// the Tor shell — and only mints a CoinPay charge if the member explicitly opts
// in at the prompt. Showing the pitch must NOT create a payment: minting on
// every join@ produced a $99 invoice for everyone who connected. Non-blocking:
// the member pays out of band and perks unlock on their next connect (or
// re-running join@).
func (a *app) offerPremium(s ssh.Session, in *bufio.Reader, u *store.User) {
	// Maybe they already paid (e.g. re-ran join@ after paying).
	if a.ensurePremium(u) {
		a.showPremiumWelcome(s, *u)
		return
	}

	// Pitch only — no charge is created here.
	wish.Println(s, "\n"+strings.Join([]string{
		"  ★ Founding Lifetime Member — $" + payments.PremiumAmount() + ", one-time",
		"    Only the first " + payments.FoundingCap + " accounts. Pay once, keep it for life.",
		"",
		"  Everything in your free membership stays free — including your",
		"  " + a.mailAddress(u.Name) + " mailbox. Founding adds these bonus",
		"  features, forever:",
		"    • custom domains    point yourdomain.com at your homepage",
		"    • Tor               a “Tor shell” in your pod — everything over Tor",
		"    • locked-in price   founding rate is yours for life — never renew, never pay again",
	}, "\n"))

	// Explicit opt-in. Anything but yes leaves with no payment created.
	wish.Print(s, "\n  Become a Founding member now? Type \"yes\" for a payment address [no]: ")
	line, err := readLine(s, in)
	if err != nil || !isYes(line) {
		wish.Print(s, "\n  No problem — you're a free member. Want it later? Re-run: ssh join@"+a.host+"\n\n")
		return
	}

	ref := payments.PremiumReference(u.PubKeyFP)
	c, ok, err := payments.CreatePremiumCharge(ref)
	if !ok || err != nil {
		if err != nil {
			log.Error("create premium charge", "err", err)
		}
		wish.Print(s, "\n  Payment is temporarily unavailable — please try again shortly.\n\n")
		return
	}
	// Remember the payment id so a later connect can confirm settlement.
	if err := a.st.SetPremiumPayment(u.ID, c.ID); err != nil {
		log.Error("store premium payment id", "err", err)
	}
	amount := "$" + payments.PremiumAmount() + " " + payments.PremiumCurrency()
	if c.CryptoAmount != "" {
		cur := c.Currency
		if cur == "" {
			cur = strings.ToUpper(payments.PremiumBlockchain())
		}
		amount += "  (≈ " + c.CryptoAmount + " " + cur + ")"
	}
	lines := []string{
		"",
		"  amount    " + amount,
		"  send to   " + c.Address,
	}
	if c.QR != "" {
		lines = append(lines, "  qr        "+c.QR)
	}
	lines = append(lines,
		"",
		"  Perks unlock once payment confirms — then re-run: ssh join@"+a.host,
		"",
	)
	wish.Println(s, strings.Join(lines, "\n"))
}

// isYes reports whether a prompt line is an affirmative opt-in.
func isYes(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "yes", "y":
		return true
	default:
		return false
	}
}

// notifySignup emails the operator the details of a newly verified signup.
// No-op when SMTP isn't configured. Subject is "bbs" per the operator's filter.
func (a *app) notifySignup(u store.User) {
	to := env("AGENTBBS_SIGNUP_NOTIFY", "anthony@profullstack.com")
	if !a.mail.Configured() || to == "" {
		return
	}
	body := "New AgentBBS signup\n\n" +
		"  username:  " + u.Name + "\n" +
		"  email:     " + u.Email + "\n" +
		"  key:       " + u.PubKeyFP + "\n" +
		"  homepage:  https://" + a.host + "/~" + u.Name + "\n"
	if err := a.mail.Send(to, "bbs", body); err != nil {
		log.Error("signup notify", "err", err, "to", to)
	}
}

// verifyCodeEmailBody is the plain-text confirmation-code email.
func verifyCodeEmailBody(name, code string) string {
	return "Hi " + name + ",\n\n" +
		"Your AgentBBS confirmation code is:\n\n" +
		"  " + code + "\n\n" +
		"Enter it in your open  ssh join@  session to activate your account.\n" +
		"If you didn't request this, you can ignore this email.\n"
}

// randCode returns a 6-digit numeric confirmation code.
func randCode() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(b[:])%1000000)
}

// validEmail is a deliberately loose check: one @, a dotted domain, no spaces.
func validEmail(e string) bool {
	if len(e) < 3 || len(e) > 254 || strings.ContainsAny(e, " \t\r\n") {
		return false
	}
	at := strings.LastIndexByte(e, '@')
	if at <= 0 || at == len(e)-1 {
		return false
	}
	return strings.Contains(e[at+1:], ".")
}

// handleVerify consumes the email confirmation link.
func (a *app) handleVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	u, ok, err := a.st.VerifyEmail(r.URL.Query().Get("token"))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(verifyPage("Something went wrong", "Please try the link again in a moment.")))
		return
	}
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(verifyPage("Link invalid or expired",
			"Run <code>ssh join@"+a.host+"</code> to get a fresh confirmation link.")))
		return
	}
	a.provisionGit(&u, "") // web flow: no SSH session key; key is added on next BBS login
	_, _ = w.Write([]byte(verifyPage("Email confirmed ✓",
		"Welcome, "+u.Name+". Your account is active — <code>ssh "+u.Name+"@"+a.host+"</code>.")))
}

// provisionGit ensures a verified member has a git.profullstack.com account on
// the AgentGit Forgejo backend. Every verified member gets one — free and paid
// alike; plan only affects quotas, enforced by AgentGit, not account existence.
// Failures are logged but never block BBS verification, and it is a no-op when
// Forgejo is unconfigured.
func (a *app) provisionGit(u *store.User, pubKey string) {
	if u == nil || !a.forgejo.Configured() || u.Name == "" || u.Email == "" {
		return
	}
	created, password, err := a.forgejo.EnsureUser(u.Name, u.Email)
	if err != nil {
		log.Error("forgejo provision", "user", u.Name, "err", err)
		return
	}
	if !created {
		return
	}
	log.Info("provisioned git account", "user", u.Name, "host", a.forgejo.BaseURL)
	// Register the BBS SSH key so the member can push with the same key they sign
	// in with. No-op when called without a session key (e.g. the web verify flow).
	if pubKey != "" {
		if added, err := a.forgejo.EnsureKey(u.Name, "agentbbs", pubKey); err != nil {
			log.Error("forgejo ssh key", "user", u.Name, "err", err)
		} else if added {
			log.Info("registered git ssh key", "user", u.Name)
		}
	}
	// Email the verified address their web sign-in link + one-time password so
	// they can log in to the Forgejo UI and create repositories. Best-effort:
	// the account already exists, so a mail failure must not block anything.
	if a.mail.Configured() {
		if err := a.mail.Send(u.Email, "Your git.profullstack.com account is ready",
			gitWelcomeEmailBody(u.Name, password, a.forgejo.LoginURL())); err != nil {
			log.Error("git welcome email", "user", u.Name, "err", err)
		}
	}
}

// gitWelcomeEmailBody is the plain-text email sent when a member's AgentGit
// (Forgejo) account is created or reset — on provisioning and by the
// notify-creds ops command: web login link, username, and the one-time password
// they must change on first sign-in.
func gitWelcomeEmailBody(name, password, loginURL string) string {
	return "Hi " + name + ",\n\n" +
		"Your git account is ready. Sign in to the web interface here:\n\n" +
		"    " + loginURL + "\n\n" +
		"    username:  " + name + "\n" +
		"    password:  " + password + "\n\n" +
		"You'll be asked to set a new password the first time you sign in.\n" +
		"After that, click the \"+\" (top right) → \"New Repository\" to create repos.\n\n" +
		"Pushing over git uses your registered SSH key — no password needed.\n\n" +
		"If you didn't request this, you can ignore this email.\n"
}

// authorizedKey renders the session's public key as a single authorized_keys
// line, or "" when the session has no key (guests / keyboard-interactive).
func authorizedKey(s ssh.Session) string {
	pk := s.PublicKey()
	if pk == nil {
		return ""
	}
	return strings.TrimSpace(string(gossh.MarshalAuthorizedKey(pk)))
}

// verifyPage renders the minimal confirmation result page.
func verifyPage(title, body string) string {
	return "<!doctype html><meta charset=utf-8><title>" + title + "</title>" +
		"<style>body{background:#000;color:#33ff66;font:16px/1.6 monospace;max-width:40rem;margin:5rem auto;padding:0 1rem}code{color:#60a5fa}</style>" +
		"<h1>" + title + "</h1><p>" + body + "</p>"
}

// handleDomain is the custom-domain self-service route. It is non-interactive
// and driven by the SSH command, mirroring join@:
//
//	ssh domain@host                 list your domains + usage
//	ssh domain@host add example.com  point a domain at your homepage
//	ssh domain@host rm  example.com  remove one
//
// Members CNAME (or A-record) their domain at the BBS host; Caddy issues a
// cert on first hit and serves their public_html. Requires a registered key.
func (a *app) handleDomain(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "domain@ needs your registered SSH key. New here? ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	u, found, err := a.st.UserByFingerprint(fp)
	if err != nil || !found {
		wish.Println(s, "key not registered — run: ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	// Custom domains are a Founding Lifetime perk ($99 one-time). ensurePremium also
	// catches a payment that settled since their last visit.
	if !a.ensurePremium(&u) {
		wish.Println(s, strings.Join([]string{
			"",
			"  Custom domains are a Premium feature (" + payments.PremiumPriceLabel + ", one-time).",
			"  Upgrade:  ssh join@" + a.host,
			"",
		}, "\n"))
		_ = s.Exit(1)
		return
	}
	if a.sites == nil {
		wish.Println(s, "custom domains are temporarily unavailable on this host.")
		_ = s.Exit(1)
		return
	}
	_, _ = a.st.RecordSession(u.ID, s.User(), remoteIP(s), "domain")

	args := s.Command()
	action := ""
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	switch {
	case action == "add" && len(args) >= 2:
		domain, err := a.sites.Add(args[1], u.Name)
		switch {
		case errors.Is(err, sites.ErrInvalidDomain):
			wish.Println(s, "not a valid domain: "+args[1])
			_ = s.Exit(1)
		case errors.Is(err, store.ErrDomainTaken):
			wish.Println(s, domain+" is already mapped to another account.")
			_ = s.Exit(1)
		case err != nil:
			wish.Println(s, "could not map domain: "+err.Error())
			_ = s.Exit(1)
		default:
			wish.Println(s, strings.Join([]string{
				"",
				"  Mapped " + domain + " → ~" + u.Name + "",
				"",
				"  Point your DNS at this host, then visit https://" + domain + ":",
				"    CNAME   " + domain + "  ->  " + a.host,
				"    (apex)  A      " + domain + "  ->  <this host's IPv4>",
				"",
				"  HTTPS is issued automatically on the first request.",
				"  Edit your page in your pod: ~/public_html/index.html",
				"",
			}, "\n"))
			_ = s.Exit(0)
		}
	case (action == "rm" || action == "remove" || action == "del") && len(args) >= 2:
		domain, err := a.sites.Remove(args[1], u.Name)
		if err != nil {
			wish.Println(s, "could not remove domain: "+err.Error())
			_ = s.Exit(1)
			return
		}
		wish.Println(s, "removed "+domain)
		_ = s.Exit(0)
	default:
		domains, _ := a.sites.List(u.Name)
		lines := []string{"", "  Custom domains for ~" + u.Name + ":"}
		if len(domains) == 0 {
			lines = append(lines, "    (none yet)")
		}
		for _, d := range domains {
			lines = append(lines, "    https://"+d)
		}
		lines = append(lines,
			"",
			"  Usage:",
			"    ssh domain@"+a.host+" add <domain>   point a domain at ~"+u.Name,
			"    ssh domain@"+a.host+" rm  <domain>   remove one",
			"",
		)
		wish.Println(s, strings.Join(lines, "\n"))
		_ = s.Exit(0)
	}
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
	if u.Banned {
		wish.Println(s, "this account is suspended.")
		_ = s.Exit(1)
		return
	}
	// Pods are a FREE member benefit — the only gate is a confirmed email, so
	// every registered member gets their own Docker pod (set
	// AGENTBBS_REQUIRE_VERIFIED_EMAIL=0 to drop even that on a dev host).
	if env("AGENTBBS_REQUIRE_VERIFIED_EMAIL", "1") != "0" && !u.EmailVerified {
		wish.Println(s, "  Confirm your email first — run: ssh join@"+a.host+"  (we email you a code to enter).")
		_ = s.Exit(1)
		return
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

// torMember resolves the caller's key to a premium member for the tor routes,
// printing a reason and returning ok=false otherwise. It records the session.
func (a *app) torMember(s ssh.Session, route string) (store.User, bool) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, route+"@ needs your registered SSH key. New here? ssh join@"+a.host)
		_ = s.Exit(1)
		return store.User{}, false
	}
	u, found, err := a.st.UserByFingerprint(fp)
	if err != nil || !found {
		wish.Println(s, "key not registered — run: ssh join@"+a.host)
		_ = s.Exit(1)
		return store.User{}, false
	}
	if u.Banned {
		wish.Println(s, "this account is suspended.")
		_ = s.Exit(1)
		return store.User{}, false
	}
	if !a.ensurePremium(&u) {
		wish.Println(s, "  "+route+" is a Founding Lifetime Member feature ($99 one-time, lifetime). Upgrade: ssh join@"+a.host)
		_ = s.Exit(1)
		return store.User{}, false
	}
	_, _ = a.st.RecordSession(u.ID, s.User(), remoteIP(s), route)
	return u, true
}

// handleTorURL fetches a single URL over Tor and writes the body back. One-shot,
// host-side, and constrained (timeout + size cap, http/https only). Premium.
func (a *app) handleTorURL(s ssh.Session) {
	u, ok := a.torMember(s, "tor-url")
	if !ok {
		return
	}
	args := s.Command()
	if len(args) == 0 {
		wish.Println(s, "usage: ssh tor-url@"+a.host+" <http(s)-url>   (e.g. an .onion address)")
		_ = s.Exit(1)
		return
	}
	url := args[0]
	log.Info("tor-url fetch", "user", u.Name, "url", url)
	body, err := tor.FetchURL(s.Context(), url)
	if err != nil {
		wish.Println(s, "  "+err.Error())
		_ = s.Exit(1)
		return
	}
	_, _ = s.Write(body)
	_ = s.Exit(0)
}

// handleTorIRC opens an interactive IRC-over-Tor session inside the member's
// pod (sandboxed). Premium; requires a PTY.
func (a *app) handleTorIRC(s ssh.Session) {
	u, ok := a.torMember(s, "tor-irc")
	if !ok {
		return
	}
	args := s.Command()
	if len(args) == 0 || !validIRCServer(args[0]) {
		wish.Println(s, "usage: ssh -t tor-irc@"+a.host+" <server[:port]>   (e.g. an .onion IRC server)")
		_ = s.Exit(1)
		return
	}
	if a.pods == nil {
		wish.Println(s, "pods are temporarily unavailable on this host.")
		_ = s.Exit(1)
		return
	}
	log.Info("tor-irc connect", "user", u.Name, "server", args[0])
	if err := a.pods.Exec(s, u.Name, tor.IRCArgv(args[0])); err != nil {
		wish.Println(s, "tor-irc error: "+err.Error())
		_ = s.Exit(1)
	}
}

// handleIRCAuth is the loopback endpoint Ergo's auth-script calls to gate the
// IRC network on BBS membership — the single user-level source of truth. It
// answers {member, premium} for an account name: only members may connect, and
// premium (lifetime-paid) drives paid IRC features. Loopback only (shares the
// /verify server), so only the on-box Ergo can reach it. There is no in-BBS
// irc@ route: members use an external client (or web) at irc.profullstack.com.
func (a *app) handleIRCAuth(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("account"))
	w.Header().Set("Content-Type", "application/json")
	u, found, err := a.st.UserByName(name)
	if err != nil || !found || u.Banned {
		_, _ = io.WriteString(w, `{"member":false,"premium":false}`)
		return
	}
	// u.Premium is the stored lifetime flag; we don't run the (network) CoinPay
	// settlement check on a per-connect auth hook — that happens at join@/hub.
	if u.Premium {
		_, _ = io.WriteString(w, `{"member":true,"premium":true}`)
		return
	}
	_, _ = io.WriteString(w, `{"member":true,"premium":false}`)
}

// handleNews drops a member into the BBS's own (members-only) Usenet/NNTP server
// using an in-process newsreader: it authenticates to the loopback NNTP listener
// as the member and runs a Bubble Tea TUI to browse groups, read, and post. Free
// for any registered member; needs a PTY. External newsreaders and agents reach
// the same server over NNTPS at news.<host>:563.
func (a *app) handleNews(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "news@ needs your registered SSH key. New here? ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	u, found, err := a.st.UserByFingerprint(fp)
	if err != nil || !found {
		wish.Println(s, "the news server is members-only — register first: ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	if u.Banned {
		wish.Println(s, "this account is suspended.")
		_ = s.Exit(1)
		return
	}
	sessID, _ := a.st.RecordSession(u.ID, s.User(), remoteIP(s), "news")
	defer func() { _ = a.st.EndSession(sessID) }()

	if err := a.runNews(s, u.Name); err != nil {
		wish.Println(s, "news: "+err.Error())
		_ = s.Exit(1)
	}
}

// runNews runs the in-process newsreader TUI for name on the session. Shared by
// the news@ route and the hub's News entry.
func (a *app) runNews(s ssh.Session, name string) error {
	addr := a.newsAddr
	if addr == "" {
		addr = news.DefaultAddr
	}
	log.Info("news connect", "user", name, "addr", addr)
	return news.RunReader(s, addr, name)
}

// mailAddress is a member's email address, e.g. alice@bbs.profullstack.com.
func (a *app) mailAddress(name string) string { return name + "@" + a.mailDomain }

// mailEnabled reports whether member mailboxes can be provisioned (Mailu admin
// API configured). When false the address is still shown but not created.
func (a *app) mailEnabled() bool { return a.mailu.Configured() }

// ensureMailbox provisions the member's <name>@<mailDomain> mailbox on Mailu if
// it doesn't already exist. Idempotent and best-effort: it logs and returns the
// error but callers treat mail as a bonus that shouldn't block onboarding. A
// no-op when Mailu isn't configured.
func (a *app) ensureMailbox(u store.User) error {
	if !a.mailEnabled() || u.Name == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.mailu.EnsureUser(ctx, u.Name, a.mailDomain); err != nil {
		log.Error("provision mailbox", "err", err, "address", a.mailAddress(u.Name))
		return err
	}
	return nil
}

// setWebmailPassword sets (and returns) a fresh webmail password for the member
// so free members can log into webmail. Best-effort: returns "" when Mailu isn't
// configured or the API call fails. The in-BBS reader doesn't use this (it goes
// through the gateway master user); only webmail needs a member password.
func (a *app) setWebmailPassword(u store.User) string {
	if !a.mailEnabled() || u.Name == "" {
		return ""
	}
	pw := readablePassword()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.mailu.SetPassword(ctx, u.Name, a.mailDomain, pw); err != nil {
		log.Error("set webmail password", "err", err, "address", a.mailAddress(u.Name))
		return ""
	}
	return pw
}

// readablePassword returns a 16-char password from an unambiguous alphabet (no
// 0/O/1/l/I) — easy to read off a terminal once and type into webmail.
func readablePassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzACDEFGHJKLMNPQRSTUVWXYZ23456789"
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a hex token; correctness over readability.
		var f [12]byte
		_, _ = rand.Read(f[:])
		return hex.EncodeToString(f[:])
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:])
}

// mailClientFor builds an AgentMail client for a member, connecting to the
// self-hosted Mailu backend. IMAP uses Dovecot master-user auth (login
// "<addr>*<master>") so the BBS gateway can open any member's mailbox with one
// secret; SMTP defaults to the co-located relay (no auth). The client stamps
// outgoing mail with the member's <name>@<mailDomain> address. Returns an error
// if the IMAP connection/login fails.
func (a *app) mailClientFor(su store.User) (*mailbox.Client, error) {
	// Mailu keys mailboxes by full address, so the IMAP login (and the master
	// login "<addr>*<master>") must use the address, not the bare handle.
	login := a.mailAddress(su.Name)
	if master := os.Getenv("AGENTBBS_MAIL_MASTER_USER"); master != "" {
		login = a.mailAddress(su.Name) + "*" + master
	}
	cfg := mailbox.IMAPConfig{
		IMAPAddr: env("AGENTBBS_MAIL_IMAP_ADDR", a.mailHost+":993"),
		SMTPAddr: env("AGENTBBS_MAIL_SMTP_ADDR", "127.0.0.1:25"),
		// Dial the loopback relay but verify STARTTLS against the mail host, whose
		// certificate it presents (the relay's cert is never for 127.0.0.1). This
		// avoids the /etc/hosts loopback hack the transactional sender needs.
		SMTPServerName: env("AGENTBBS_MAIL_SMTP_SERVERNAME", a.mailHost),
		Username:       login,
		Password:       os.Getenv("AGENTBBS_MAIL_MASTER_PASS"),
		// Mailu's front nginx pre-authenticates against its user DB before
		// proxying, which rejects the "<addr>*master" master login. The gateway
		// therefore talks to Dovecot directly over loopback (plaintext, on-host)
		// when AGENTBBS_MAIL_IMAP_PLAINTEXT=1. See docs/mail.md.
		Plaintext: os.Getenv("AGENTBBS_MAIL_IMAP_PLAINTEXT") == "1",
		// SMTPUser/SMTPPass left empty: submit via the trusted local relay.
	}
	tr, err := mailbox.NewIMAPTransport(cfg)
	if err != nil {
		return nil, err
	}
	return mailbox.NewClient(tr, mailbox.Identity{Name: su.Name, Paid: su.Premium}, a.mailDomain, 50), nil
}

// handleMail routes a Founding Lifetime member into AgentMail: an interactive
// TUI when a PTY is present and no command is given, or the JSON bot mode
// (ssh mail@host <cmd>, or no PTY) for agents.
func (a *app) handleMail(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "mail@ needs your registered SSH key. New here? ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	u, found, err := a.st.UserByFingerprint(fp)
	if err != nil || !found {
		wish.Println(s, "key not registered — run: ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	if u.Banned {
		wish.Println(s, "this account is suspended.")
		_ = s.Exit(1)
		return
	}
	if !u.EmailVerified {
		wish.Println(s, "  verify your email first: ssh -t join@"+a.host)
		_ = s.Exit(1)
		return
	}
	if !a.mailEnabled() {
		wish.Println(s, "  mail is temporarily unavailable on this host.")
		_ = s.Exit(1)
		return
	}
	// Mail is a free benefit of membership — make sure the mailbox exists.
	_ = a.ensureMailbox(u)
	sessID, _ := a.st.RecordSession(u.ID, s.User(), remoteIP(s), "mail")
	defer func() { _ = a.st.EndSession(sessID) }()

	c, err := a.mailClientFor(u)
	if err != nil {
		wish.Println(s, "mail: "+err.Error())
		_ = s.Exit(1)
		return
	}
	defer c.Close()

	args := s.Command()
	_, _, hasPty := s.Pty()
	if len(args) > 0 || !hasPty {
		// Agent/bot mode: JSON in, JSON out.
		if err := mailbox.RunBot(s.Context(), c, args, s, s); err != nil {
			_ = s.Exit(1)
		}
		return
	}
	if err := mailbox.RunReader(s, c); err != nil {
		wish.Println(s, "mail: "+err.Error())
		_ = s.Exit(1)
	}
}

// handleTorCmd runs an arbitrary command through Tor (torsocks) inside the
// member's pod, never on the host. Premium; requires a PTY.
func (a *app) handleTorCmd(s ssh.Session) {
	u, ok := a.torMember(s, "tor")
	if !ok {
		return
	}
	args := s.Command()
	if len(args) == 0 {
		wish.Println(s, "usage: ssh -t tor@"+a.host+" <command...>   (runs in your pod, over Tor)")
		_ = s.Exit(1)
		return
	}
	if a.pods == nil {
		wish.Println(s, "pods are temporarily unavailable on this host.")
		_ = s.Exit(1)
		return
	}
	log.Info("tor cmd", "user", u.Name, "argv", strings.Join(args, " "))
	if err := a.pods.Exec(s, u.Name, tor.Torsocks(args)); err != nil {
		wish.Println(s, "tor error: "+err.Error())
		_ = s.Exit(1)
	}
}

// validIRCServer accepts host or host:port with a sane charset (no shell/space).
func validIRCServer(s string) bool {
	host := s
	if i := strings.LastIndex(s, ":"); i > 0 {
		port := s[i+1:]
		host = s[:i]
		if port == "" || len(port) > 5 {
			return false
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	if host == "" || len(host) > 255 {
		return false
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return true
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

// handleMsg is the member-to-member messaging route: `ssh msg@host <user> [text]`
// leaves a note in <user>'s BBS inbox. The body is the remaining args, or stdin
// when none are given (so `echo hi | ssh msg@host bob` works). Members only;
// the recipient reads it in the hub's Members ▸ inbox.
func (a *app) handleMsg(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "msg@ needs your registered SSH key. New here? ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	from, found, err := a.st.UserByFingerprint(fp)
	if err != nil || !found {
		wish.Println(s, "key not registered — run: ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	args := s.Command()
	if len(args) == 0 {
		wish.Println(s, "usage: ssh msg@"+a.host+" <user> [message]   (or pipe the message on stdin)")
		_ = s.Exit(1)
		return
	}
	to := strings.ToLower(args[0])
	recipient, ok, err := a.st.UserByName(to)
	if err != nil || !ok {
		wish.Println(s, "no member named "+to+" — check the spelling (ssh "+to+"@"+a.host+" to finger).")
		_ = s.Exit(1)
		return
	}
	if recipient.Name == from.Name {
		wish.Println(s, "you can't message yourself.")
		_ = s.Exit(1)
		return
	}
	body := strings.TrimSpace(strings.Join(args[1:], " "))
	if body == "" {
		// No inline text — read the message from stdin (piped, or typed then ^D).
		b, _ := io.ReadAll(io.LimitReader(s, 64*1024))
		body = strings.TrimSpace(string(b))
	}
	if body == "" {
		wish.Println(s, "empty message — nothing sent.")
		_ = s.Exit(1)
		return
	}
	if err := a.st.SendMessage(from.Name, recipient.Name, body); err != nil {
		wish.Println(s, "could not send: "+err.Error())
		_ = s.Exit(1)
		return
	}
	_, _ = a.st.RecordSession(from.ID, s.User(), remoteIP(s), "msg")
	wish.Println(s, "✓ message left for "+recipient.Name+" — they'll see it in Members ▸ inbox.")
	_ = s.Exit(0)
}

// handlePasswd is the self-service "reset my password everywhere" route. It is
// gated by the caller's registered SSH key (so it doubles as the forgot-password
// path — no old password needed) and sets ONE new password across every service
// that has its own credential: git (Forgejo), mail (Mailu webmail), and chat
// (IRC + The Lounge). Git push and BBS/SSH access are unaffected — those use the
// member's key, not a password.
//
//	ssh passwd@host         interactive: type a new password (twice), applied everywhere
//	ssh passwd@host < file  non-interactive: read the new password from stdin
//	echo | ssh passwd@host  empty stdin / no PTY: a strong password is generated for you
func (a *app) handlePasswd(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "passwd@ needs your registered SSH key. New here? ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	u, found, err := a.st.UserByFingerprint(fp)
	if err != nil || !found {
		wish.Println(s, "key not registered — run: ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	if u.Banned {
		wish.Println(s, "this account is suspended.")
		_ = s.Exit(1)
		return
	}

	pw, generated, err := a.readNewPassword(s)
	if err != nil {
		wish.Println(s, "password reset cancelled.")
		_ = s.Exit(1)
		return
	}

	wish.Println(s, "")
	wish.Println(s, "Setting your password across services…")

	type result struct{ label, detail string }
	var ok, failed []result

	// git (Forgejo): make sure the account exists, then set the chosen password.
	if a.forgejo.Configured() {
		if _, _, e := a.forgejo.EnsureUser(u.Name, u.Email); e != nil {
			failed = append(failed, result{"git ", e.Error()})
		} else if e := a.forgejo.SetPassword(u.Name, pw); e != nil {
			failed = append(failed, result{"git ", e.Error()})
		} else {
			ok = append(ok, result{"git ", a.forgejo.LoginURL() + "  (username: " + u.Name + ")"})
		}
	}

	// mail (Mailu webmail): ensure the mailbox exists, then set its password.
	if a.mailEnabled() {
		if e := a.ensureMailbox(u); e != nil {
			failed = append(failed, result{"mail", e.Error()})
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			e := a.mailu.SetPassword(ctx, u.Name, a.mailDomain, pw)
			cancel()
			if e != nil {
				failed = append(failed, result{"mail", e.Error()})
			} else {
				ok = append(ok, result{"mail", a.webmailURL + "  (" + a.mailAddress(u.Name) + ")"})
			}
		}
	}

	// chat (IRC + The Lounge): set via the privileged helper.
	if a.irc.Configured() {
		if e := a.irc.SetPassword(u.Name, pw); e != nil {
			failed = append(failed, result{"chat", e.Error()})
		} else {
			ok = append(ok, result{"chat", "SASL on irc." + rootDomain(a.host) + " / web — account: " + u.Name})
		}
	}

	wish.Println(s, "")
	if generated {
		wish.Println(s, "Your new password (save it now — it isn't shown again):")
		wish.Println(s, "    "+pw)
		wish.Println(s, "")
	}
	for _, r := range ok {
		wish.Println(s, "  ✓ "+r.label+"  "+r.detail)
	}
	for _, r := range failed {
		wish.Println(s, "  ✗ "+r.label+"  "+r.detail)
	}
	if len(ok) == 0 && len(failed) == 0 {
		wish.Println(s, "  (no password-backed services are configured on this server)")
	}

	_, _ = a.st.RecordSession(u.ID, s.User(), remoteIP(s), "passwd")

	// Best-effort confirmation email (never contains the password). Skipped when
	// the member has no verified address or SMTP isn't configured.
	if u.EmailVerified && u.Email != "" && a.mail.Configured() {
		_ = a.mail.Send(u.Email, "Your "+rootDomain(a.host)+" password was changed",
			passwdChangedEmailBody(u.Name, len(ok), remoteIP(s)))
	}

	if len(failed) > 0 {
		_ = s.Exit(1)
		return
	}
	_ = s.Exit(0)
}

// readNewPassword obtains the member's new password. With a PTY it prompts twice
// (masked) and requires the two entries to match and meet a minimum length. With
// no PTY it reads the password from stdin; if stdin is empty it generates a strong
// one and returns generated=true so the caller shows it to the member.
func (a *app) readNewPassword(s ssh.Session) (pw string, generated bool, err error) {
	const minLen = 8
	_, _, isPTY := s.Pty()
	if !isPTY {
		b, _ := io.ReadAll(io.LimitReader(s, 4096))
		piped := strings.TrimSpace(string(b))
		if piped == "" {
			gen, e := randPassword()
			return gen, true, e
		}
		if len(piped) < minLen {
			return "", false, fmt.Errorf("password too short")
		}
		return piped, false, nil
	}

	in := bufio.NewReader(s)
	for {
		wish.Print(s, "New password (min "+fmt.Sprint(minLen)+" chars, blank to generate one): ")
		first, e := readSecret(s, in)
		if e != nil {
			return "", false, e
		}
		if first == "" {
			gen, e := randPassword()
			return gen, true, e
		}
		if len(first) < minLen {
			wish.Println(s, "  too short — try again.")
			continue
		}
		wish.Print(s, "Confirm new password: ")
		second, e := readSecret(s, in)
		if e != nil {
			return "", false, e
		}
		if first != second {
			wish.Println(s, "  passwords didn't match — try again.")
			continue
		}
		return first, false, nil
	}
}

// randPassword returns a strong URL-safe-ish random password (24 hex chars).
func randPassword() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// rootDomain strips the first label off a host (bbs.profullstack.com →
// profullstack.com) so user-facing copy can name the shared apex.
func rootDomain(host string) string {
	if i := strings.IndexByte(host, '.'); i >= 0 && strings.Contains(host[i+1:], ".") {
		return host[i+1:]
	}
	return host
}

// passwdChangedEmailBody is the security-notice email sent after a successful
// reset. It deliberately never includes the password.
func passwdChangedEmailBody(name string, services int, ip string) string {
	return "Hi " + name + ",\n\n" +
		"Your password was just changed across your account services (git, mail, chat)" +
		" via ssh passwd@.\n\n" +
		fmt.Sprintf("    services updated: %d\n", services) +
		"    request IP:       " + ip + "\n\n" +
		"If this wasn't you, your SSH key may be compromised — rotate it and contact the operator.\n\n" +
		"Note: git push and BBS/SSH login use your SSH key, not this password.\n"
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

// domainCmd is the ops side of custom domains: `agentbbs map-domain <domain>
// <user>` / `unmap-domain <domain> <user>`, mirroring grant-pod.
func domainCmd(st store.Store, dataDir, cmd string, args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: agentbbs %s <domain> <username>\n", cmd)
		os.Exit(2)
	}
	sm, err := sites.NewManager(st, dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sites:", err)
		os.Exit(1)
	}
	domain, user := args[0], strings.ToLower(args[1])
	if cmd == "unmap-domain" {
		d, err := sm.Remove(domain, user)
		if err != nil {
			fmt.Fprintln(os.Stderr, "unmap:", err)
			os.Exit(1)
		}
		fmt.Printf("unmapped %s from %s\n", d, user)
		return
	}
	d, err := sm.Add(domain, user)
	if err != nil {
		fmt.Fprintln(os.Stderr, "map:", err)
		os.Exit(1)
	}
	fmt.Printf("mapped %s -> ~%s\n", d, user)
}

// seedHomepage creates a member's public_html (served at /~name by the Caddy
// front end) with a starter index.html, but never clobbers an edit they made.
func seedHomepage(dir, name, host string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	index := filepath.Join(dir, "index.html")
	if _, err := os.Stat(index); err == nil {
		return // user already has a homepage; leave it alone
	}
	page := "<!doctype html>\n<meta charset=utf-8>\n" +
		"<title>~" + name + "</title>\n" +
		"<style>body{background:#000;color:#33ff66;font:16px/1.5 monospace;max-width:42rem;margin:4rem auto;padding:0 1rem}a{color:#60a5fa}</style>\n" +
		"<h1>~" + name + "</h1>\n" +
		"<p>This is " + name + "'s corner of AgentBBS.</p>\n" +
		"<p>Edit <code>~/public_html/index.html</code> in your pod (<code>ssh pod@" + host + "</code>) to make it yours.</p>\n"
	_ = os.WriteFile(index, []byte(page), 0o644)
}

func remoteIP(s ssh.Session) string {
	if host, _, err := net.SplitHostPort(s.RemoteAddr().String()); err == nil {
		return host
	}
	return s.RemoteAddr().String()
}
