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
//	ssh admin@host  the operator admin console ($AGENTBBS_ADMINS only)
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
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
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
	"github.com/profullstack/agentbbs/internal/forgejo"
	"github.com/profullstack/agentbbs/internal/forwardemail"
	"github.com/profullstack/agentbbs/internal/games"
	"github.com/profullstack/agentbbs/internal/hub"
	"github.com/profullstack/agentbbs/internal/irc"
	"github.com/profullstack/agentbbs/internal/mail"
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
	st       store.Store
	pods     *pods.Manager // nil when no container engine on host
	sites    *sites.Manager
	registry []plugin.Plugin
	sandbox  *sandbox.Runner
	mail     mail.Config
	fe       forwardemail.Config // premium @bbs email provisioning
	forgejo  forgejo.Config      // AgentGit git.profullstack.com account provisioning
	live     *liveReg            // in-memory live-session registry (admin console)
	gamesReg *games.Registry     // AgentGames catalog
	mm       *games.Matchmaker   // AgentGames matchmaker (agent-vs-agent)
	dataDir  string
	assets   string
	host     string // public hostname used in user-facing messages
	newsAddr string // loopback NNTP address the news@ reader dials
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
	if len(os.Args) > 1 && os.Args[1] == "qrypt-issuer-keygen" {
		qryptIssuerKeygen()
		return
	}

	host := env("AGENTBBS_HOST", "bbs.profullstack.com")
	fe := forwardemail.ConfigFromEnv()
	if fe.Domain == "" {
		fe.Domain = host // personal addresses live on the BBS host by default
	}
	a := &app{
		st:      st,
		sandbox: sandbox.New(sandbox.Mode(env("AGENTBBS_SANDBOX", "auto"))),
		mail:    mail.ConfigFromEnv(),
		fe:      fe,
		forgejo: forgejo.ConfigFromEnv(),
		live:    newLiveReg(),
		dataDir: dataDir,
		assets:  env("AGENTBBS_ASSETS", "./assets"),
		host:    host,
	}
	a.gamesReg = games.Catalog()
	a.mm = games.NewMatchmaker(a.gamesReg, a.st,
		time.Duration(envInt("AGENTBBS_GAME_MOVE_TIMEOUT", 15))*time.Second,
		time.Duration(envInt("AGENTBBS_GAME_QUEUE_WAIT", 120))*time.Second)
	a.registry = []plugin.Plugin{arcade.Plugin{}, agentgames.New(a.gamesReg), qryptinviteplugin.Plugin{}, about.Plugin{}}

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

	if m, err := pods.Detect(); err == nil {
		a.pods = m
		log.Info("pods enabled", "engine", m.Engine())
	} else {
		log.Warn("pods disabled", "reason", err)
	}
	log.Info("sandbox", "mode", a.sandbox.Mode())

	// Email confirmation endpoint (the link in the join@ verification mail).
	// Loopback only; Caddy reverse-proxies /verify to it. Separate from the
	// on-demand-TLS ask server above.
	verifyAddr := env("AGENTBBS_HTTP_ADDR", "127.0.0.1:8088")
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/verify", a.handleVerify)
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
			a.track(), // register every session for the admin console
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
// The active-PTY guard applies to hub sessions only; join@ and pod@ check their
// own PTY (both are interactive) so they can return a tailored hint instead of
// activeterm's opaque rejection.
func (a *app) router() wish.Middleware {
	btMw := bm.Middleware(a.teaHandler)
	adminMw := bm.Middleware(a.adminTeaHandler)
	return func(next ssh.Handler) ssh.Handler {
		hubHandler := activeterm.Middleware()(btMw(next))
		adminHandler := activeterm.Middleware()(adminMw(next))
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
			case auth.IsIRCName(user):
				a.handleIRC(s)
			case auth.IsNewsName(user):
				a.handleNews(s)
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
	}

	sessID, _ := a.st.RecordSession(u.StoreID, s.User(), remoteIP(s), "hub")
	go func() { <-s.Context().Done(); _ = a.st.EndSession(sessID) }()

	ctx := plugin.Context{Store: a.st, Sandbox: a.sandbox, AssetsDir: a.assets}
	if u.Kind != auth.Guest {
		ctx.DataDir = filepath.Join(a.dataDir, "users", u.Name)
		_ = os.MkdirAll(filepath.Join(ctx.DataDir, "wads"), 0o755)
		// tilde.town-style web home: served at https://<host>/~<name> by the
		// Caddy front end (see setup.sh). Seed an editable starter page so the
		// URL works the moment a member first signs in.
		seedHomepage(filepath.Join(ctx.DataDir, "public_html"), u.Name, a.host)
	}
	return hub.New(u, ctx, a.enabledPlugins()), []tea.ProgramOption{tea.WithAltScreen()}
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
	// member: it gets a Docker pod (ssh pod@) and a /~name homepage.
	if !u.EmailVerified {
		if !a.verifyEmailInteractive(s, in, &u) {
			_ = s.Exit(1)
			return
		}
		a.notifySignup(u)
	}

	// Every verified member gets a homepage at https://<host>/~<name>.
	seedHomepage(filepath.Join(a.dataDir, "users", u.Name, "public_html"), u.Name, a.host)

	wish.Println(s, "\n"+strings.Join([]string{
		"  You're in — free membership includes:",
		"    pod       ssh pod@" + a.host + "        your own Linux pod",
		"    hub       ssh " + u.Name + "@" + a.host,
		"    homepage  https://" + a.host + "/~" + u.Name,
	}, "\n"))

	// 2) Founding Lifetime ($99 one-time): personal @host email + custom domains.
	a.offerPremium(s, &u)
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

// registerNewMember asks the visitor to choose a username, then creates their
// member account under it. The name is sanitized to the hub/subdomain charset,
// rejected if reserved, and must be free; pressing enter accepts a generated
// member-<fp8> default. Returns the created user.
func (a *app) registerNewMember(s ssh.Session, in *bufio.Reader, fp string) (store.User, error) {
	def := "member-" + strings.ToLower(strings.TrimPrefix(fp, "SHA256:"))[:8]
	wish.Println(s, "\n  Pick a username — letters, numbers and dashes, 3–20 chars.")
	wish.Println(s, "  It's your handle for  ssh <name>@"+a.host+"  and  https://"+a.host+"/~<name>.")

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
			a.provisionGit(u)
			wish.Println(s, "  Email confirmed ✓")
			return true
		}
		wish.Println(s, "  that code didn't match — try again.")
	}
	wish.Println(s, "  Too many attempts — run  ssh join@"+a.host+"  again for a fresh code.")
	return false
}

// ensurePremium upgrades *u to premium if its CoinPay charge has settled,
// provisioning the member's @host email alias on the transition. It is silent
// (no session output) so it is safe to call from the hub. Returns the current
// premium state.
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
	// Create their <name>@host alias forwarding to the email they verified.
	if a.fe.Configured() && u.Email != "" {
		if err := a.fe.CreateAlias(u.Name, u.Email); err != nil {
			log.Error("forwardemail alias", "err", err, "alias", a.fe.Address(u.Name))
		}
	}
	return true
}

// showPremiumWelcome prints a premium member's perks: their personal email,
// where it forwards, the webmail URL, and custom domains.
func (a *app) showPremiumWelcome(s ssh.Session, u store.User) {
	lines := []string{
		"",
		"  ★ Founding Lifetime Member — thanks! Your perks:",
		"",
		"  email      " + a.fe.Address(u.Name),
		"  forwards   " + u.Email,
	}
	if url := a.fe.WebmailURL(); url != "" {
		lines = append(lines, "  webmail    "+url)
	}
	lines = append(lines,
		"  domains    ssh domain@"+a.host+" add <yourdomain.com>",
		"",
	)
	wish.Println(s, strings.Join(lines, "\n"))
}

// offerPremium pitches the $99 Founding Lifetime membership — a personal @host email and
// custom domains. When CoinPay can mint a charge in-session it shows the exact
// amount and deposit address; otherwise it falls back to a pay command.
// Non-blocking: the member pays out of band and perks unlock on their next
// connect (or re-running join@).
func (a *app) offerPremium(s ssh.Session, u *store.User) {
	// Maybe they already paid (e.g. re-ran join@ after paying).
	if a.ensurePremium(u) {
		a.showPremiumWelcome(s, *u)
		return
	}
	ref := payments.PremiumReference(u.PubKeyFP)

	lines := []string{
		"",
		"  ★ Founding Lifetime Member — $" + payments.PremiumAmount() + ", one-time",
		"    Only the first " + payments.FoundingCap + " accounts. Pay once, keep it for life.",
		"",
		"  Everything in your free membership stays free — founding adds these",
		"  bonus features, forever:",
		"    • your own email   " + a.fe.Address(u.Name) + " (forwards to you, webmail included)",
		"    • custom domains   point yourdomain.com at your homepage",
		"    • Tor access       ssh tor@" + a.host + " — fetch URLs & join IRC over Tor",
		"    • locked-in price  founding rate is yours for life — never renew, never pay again",
		"",
	}
	if c, ok, err := payments.CreatePremiumCharge(ref); ok && err == nil {
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
		lines = append(lines,
			"  amount    "+amount,
			"  send to   "+c.Address,
		)
		if c.QR != "" {
			lines = append(lines, "  qr        "+c.QR)
		}
	} else {
		if err != nil {
			log.Error("create premium charge", "err", err)
		}
		lines = append(lines, "  Payment is temporarily unavailable — please try again shortly.")
	}
	lines = append(lines,
		"",
		"  Perks unlock once payment confirms — then re-run: ssh join@"+a.host,
		"",
	)
	wish.Println(s, strings.Join(lines, "\n"))
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
	a.provisionGit(&u)
	_, _ = w.Write([]byte(verifyPage("Email confirmed ✓",
		"Welcome, "+u.Name+". Your account is active — <code>ssh "+u.Name+"@"+a.host+"</code>.")))
}

// provisionGit ensures a verified member has a git.profullstack.com account on
// the AgentGit Forgejo backend. Every verified member gets one — free and paid
// alike; plan only affects quotas, enforced by AgentGit, not account existence.
// Failures are logged but never block BBS verification, and it is a no-op when
// Forgejo is unconfigured.
func (a *app) provisionGit(u *store.User) {
	if u == nil || !a.forgejo.Configured() || u.Name == "" || u.Email == "" {
		return
	}
	created, err := a.forgejo.EnsureUser(u.Name, u.Email)
	if err != nil {
		log.Error("forgejo provision", "user", u.Name, "err", err)
		return
	}
	if created {
		log.Info("provisioned git account", "user", u.Name, "host", a.forgejo.BaseURL)
	}
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

// handleIRC drops a member into the BBS's own (members-only) IRC network using
// an in-process client: it authenticates to Ergo over SASL as the member and
// runs a Bubble Tea TUI. Free for any registered member; needs a PTY. Distinct
// from tor-irc@ (a client for remote servers over Tor).
func (a *app) handleIRC(s ssh.Session) {
	fp := auth.Fingerprint(s.PublicKey())
	if fp == "" {
		wish.Println(s, "irc@ needs your registered SSH key. New here? ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	u, found, err := a.st.UserByFingerprint(fp)
	if err != nil || !found {
		wish.Println(s, "the IRC network is members-only — register first: ssh join@"+a.host)
		_ = s.Exit(1)
		return
	}
	if u.Banned {
		wish.Println(s, "this account is suspended.")
		_ = s.Exit(1)
		return
	}
	sessID, _ := a.st.RecordSession(u.ID, s.User(), remoteIP(s), "irc")
	defer func() { _ = a.st.EndSession(sessID) }()

	addr := strings.TrimSpace(os.Getenv("AGENTBBS_IRC_ADDR"))
	if addr == "" {
		addr = irc.DefaultAddr
	}
	log.Info("irc connect", "user", u.Name, "addr", addr)
	c, err := irc.Dial(s.Context(), addr, u.Name)
	if err != nil {
		wish.Println(s, "irc: "+err.Error())
		_ = s.Exit(1)
		return
	}
	_ = c.Join(irc.DefaultChannel)
	if err := irc.Run(s, c); err != nil {
		wish.Println(s, "irc: "+err.Error())
	}
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

	addr := a.newsAddr
	if addr == "" {
		addr = news.DefaultAddr
	}
	log.Info("news connect", "user", u.Name, "addr", addr)
	if err := news.RunReader(s, addr, u.Name); err != nil {
		wish.Println(s, "news: "+err.Error())
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
