// Package mailclients launches third-party terminal email clients — himalaya
// (https://github.com/pimalaya/himalaya) and meli (https://meli-email.org) —
// against a member's AgentBBS mailbox, as alternatives to the built-in
// AgentMail reader (internal/mailbox).
//
// Like the built-in client, these run ON THE HOST, not inside the member's pod:
// AgentBBS mailboxes are opened with a single Dovecot master-user secret and
// submitted through the loopback relay, and neither of those must ever leak into
// a pod (see docs/mail.md). We generate a throwaway per-session config that
// points the client at exactly the same IMAP/SMTP paths the built-in client
// uses, attach the client to the SSH PTY, and delete the config on exit.
//
// Client config schemas drift between releases, so every field the default
// template renders is also overridable: set AGENTBBS_HIMALAYA_CONFIG_TEMPLATE /
// AGENTBBS_MELI_CONFIG_TEMPLATE to a template path to replace the built-in
// config wholesale, and AGENTBBS_HIMALAYA_ARGS / AGENTBBS_MELI_ARGS to change
// the argv. Placeholders available to a template are documented on Backend.
package mailclients

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/ssh"
	"github.com/creack/pty"
)

// Backend is the resolved connection the launched client is pointed at. It
// mirrors mailbox.IMAPConfig: the login is the Dovecot master-user login
// ("<addr>*<master>"), the password is the master secret, and SMTP is the
// unauthenticated loopback relay. The caller builds this from the same env the
// built-in client reads so all three clients behave identically.
//
// Template placeholders (for AGENTBBS_*_CONFIG_TEMPLATE overrides):
//
//	{{name}}            bare handle, e.g. alice
//	{{address}}         full address, e.g. alice@bbs.profullstack.com
//	{{display_name}}    display name (defaults to {{name}})
//	{{login}}           IMAP login (master-user form)
//	{{password}}        IMAP/SMTP password (master secret)
//	{{imap_host}}       IMAP host
//	{{imap_port}}       IMAP port
//	{{imap_encryption}} "none" when plaintext, else "tls"
//	{{smtp_host}}       SMTP host
//	{{smtp_port}}       SMTP port
//	{{smtp_encryption}} "none" when the relay is loopback/plaintext, else "start-tls"
type Backend struct {
	Name        string // bare handle
	Address     string // full email address
	DisplayName string // defaults to Name when empty

	IMAPHost      string
	IMAPPort      int
	IMAPPlaintext bool // dial IMAP without TLS (loopback → Dovecot master login)

	SMTPHost string
	SMTPPort int
	// SMTPPlaintext is true for the unauthenticated loopback relay (no STARTTLS).
	SMTPPlaintext bool

	Login    string // IMAP login (may be "<addr>*<master>")
	Password string // master secret
}

func (b Backend) display() string {
	if b.DisplayName != "" {
		return b.DisplayName
	}
	return b.Name
}

func (b Backend) imapEncryption() string {
	if b.IMAPPlaintext {
		return "none"
	}
	return "tls"
}

func (b Backend) smtpEncryption() string {
	if b.SMTPPlaintext {
		return "none"
	}
	return "start-tls"
}

// placeholders returns the template substitution map.
func (b Backend) placeholders() map[string]string {
	return map[string]string{
		"{{name}}":            b.Name,
		"{{address}}":         b.Address,
		"{{display_name}}":    b.display(),
		"{{login}}":           b.Login,
		"{{password}}":        b.Password,
		"{{imap_host}}":       b.IMAPHost,
		"{{imap_port}}":       strconv.Itoa(b.IMAPPort),
		"{{imap_encryption}}": b.imapEncryption(),
		"{{smtp_host}}":       b.SMTPHost,
		"{{smtp_port}}":       strconv.Itoa(b.SMTPPort),
		"{{smtp_encryption}}": b.smtpEncryption(),
	}
}

func render(tmpl string, ph map[string]string) string {
	out := tmpl
	for k, v := range ph {
		out = strings.ReplaceAll(out, k, v)
	}
	return out
}

// Client identifies one of the supported third-party clients.
type Client struct {
	name      string // "himalaya" | "meli"
	title     string // display title, e.g. "Himalaya"
	binEnv    string // env var overriding the binary path
	binName   string // default binary name looked up on PATH
	argsEnv   string // env var overriding the argv (space-split)
	tmplEnv   string // env var overriding the config template
	configRel string // config path relative to the session HOME (XDG_CONFIG_HOME)
	defArgs   func(cfg string) []string
	template  string
}

// Himalaya is the pimalaya CLI email client.
var Himalaya = Client{
	name:      "himalaya",
	title:     "Himalaya",
	binEnv:    "AGENTBBS_HIMALAYA_BIN",
	binName:   "himalaya",
	argsEnv:   "AGENTBBS_HIMALAYA_ARGS",
	tmplEnv:   "AGENTBBS_HIMALAYA_CONFIG_TEMPLATE",
	configRel: ".config/himalaya/config.toml",
	defArgs:   func(cfg string) []string { return []string{"-c", cfg} },
	template:  himalayaTemplate,
}

// Meli is the meli-email.org full-screen TUI email client.
var Meli = Client{
	name:      "meli",
	title:     "Meli",
	binEnv:    "AGENTBBS_MELI_BIN",
	binName:   "meli",
	argsEnv:   "AGENTBBS_MELI_ARGS",
	tmplEnv:   "AGENTBBS_MELI_CONFIG_TEMPLATE",
	configRel: ".config/meli/config.toml",
	defArgs:   func(cfg string) []string { return []string{"-c", cfg} },
	template:  meliTemplate,
}

// Available reports whether the client binary can be found, so the hub can lock
// the entry with a helpful reason instead of failing on launch.
func (c Client) Available() bool { _, err := c.binary(); return err == nil }

// Name is the client's short name ("himalaya" / "meli").
func (c Client) Name() string { return c.name }

// Title is the client's display title, e.g. "Himalaya".
func (c Client) Title() string { return c.title }

func (c Client) binary() (string, error) {
	if p := os.Getenv(c.binEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s: %s not found (%s)", c.name, p, c.binEnv)
		}
		return p, nil
	}
	p, err := exec.LookPath(c.binName)
	if err != nil {
		return "", fmt.Errorf("%s is not installed on this host (looked for %q on PATH; set %s to override)", c.name, c.binName, c.binEnv)
	}
	return p, nil
}

func (c Client) configTemplate() (string, error) {
	if p := os.Getenv(c.tmplEnv); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("%s: reading %s: %w", c.name, c.tmplEnv, err)
		}
		return string(b), nil
	}
	return c.template, nil
}

func (c Client) argv(bin, cfg string) []string {
	if v := os.Getenv(c.argsEnv); strings.TrimSpace(v) != "" {
		// The override is the argv AFTER the binary; render {{config}} so callers
		// can place the -c flag wherever the client wants it.
		fields := strings.Fields(v)
		for i, f := range fields {
			fields[i] = strings.ReplaceAll(f, "{{config}}", cfg)
		}
		return append([]string{bin}, fields...)
	}
	return append([]string{bin}, c.defArgs(cfg)...)
}

// Run generates a throwaway config for be, launches the client attached to the
// SSH session's PTY, and cleans up on exit. A PTY is required (ssh -t).
func (c Client) Run(s ssh.Session, be Backend) error {
	ptyReq, winCh, hasPty := s.Pty()
	if !hasPty {
		return fmt.Errorf("%s needs an interactive terminal — connect with: ssh -t", c.name)
	}
	bin, err := c.binary()
	if err != nil {
		return err
	}
	tmpl, err := c.configTemplate()
	if err != nil {
		return err
	}

	home, err := os.MkdirTemp("", "agentbbs-"+c.name+"-")
	if err != nil {
		return fmt.Errorf("%s: temp dir: %w", c.name, err)
	}
	// The config holds the master secret; make sure it is only ever mode 0600 and
	// removed when the session ends, even on panic.
	defer os.RemoveAll(home)

	cfgPath := filepath.Join(home, filepath.FromSlash(c.configRel))
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return fmt.Errorf("%s: config dir: %w", c.name, err)
	}
	if err := os.WriteFile(cfgPath, []byte(render(tmpl, be.placeholders())), 0o600); err != nil {
		return fmt.Errorf("%s: write config: %w", c.name, err)
	}

	argv := c.argv(bin, cfgPath)
	cmd := exec.Command(argv[0], argv[1:]...)
	// Isolate the client entirely inside the temp HOME so it can never read or
	// write the host account's real dotfiles, and so its cache/state is disposable.
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"TERM="+ptyReq.Term,
	)

	f, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("%s: start: %w", c.name, err)
	}
	defer f.Close()

	_ = pty.Setsize(f, &pty.Winsize{Rows: uint16(ptyReq.Window.Height), Cols: uint16(ptyReq.Window.Width)})
	go func() {
		for w := range winCh {
			_ = pty.Setsize(f, &pty.Winsize{Rows: uint16(w.Height), Cols: uint16(w.Width)})
		}
	}()

	go func() { _, _ = io.Copy(f, s) }() // ssh -> client
	_, _ = io.Copy(s, f)                 // client -> ssh
	return cmd.Wait()
}

// himalayaTemplate is himalaya's TOML config (schema v1.x). The SMTP relay is
// the unauthenticated loopback submission the gateway uses, so no send auth is
// configured; override AGENTBBS_HIMALAYA_CONFIG_TEMPLATE if your relay differs.
const himalayaTemplate = `# Generated per-session by AgentBBS — do not edit; changes are discarded.
[accounts.agentbbs]
default = true
email = "{{address}}"
display-name = "{{display_name}}"

backend.type = "imap"
backend.host = "{{imap_host}}"
backend.port = {{imap_port}}
backend.encryption.type = "{{imap_encryption}}"
backend.login = "{{login}}"
backend.auth.type = "password"
backend.auth.raw = "{{password}}"

message.send.backend.type = "smtp"
message.send.backend.host = "{{smtp_host}}"
message.send.backend.port = {{smtp_port}}
message.send.backend.encryption.type = "{{smtp_encryption}}"
`

// meliTemplate is meli's TOML config. IMAP uses the master-user login; sending
// goes through the loopback relay with no auth/TLS (type = "none").
const meliTemplate = `# Generated per-session by AgentBBS — do not edit; changes are discarded.
[accounts.agentbbs]
root_mailbox = "INBOX"
format = "imap"
identity = "{{address}}"
display_name = "{{display_name}}"
subscribed_mailboxes = ["INBOX", "Sent", "Drafts", "Trash", "Junk"]
server_hostname = "{{imap_host}}"
server_username = "{{login}}"
server_password = "{{password}}"
server_port = {{imap_port}}
use_starttls = false
danger_accept_invalid_certs = true

[accounts.agentbbs.send_mail]
hostname = "{{smtp_host}}"
port = {{smtp_port}}
[accounts.agentbbs.send_mail.auth]
type = "none"
[accounts.agentbbs.send_mail.security]
type = "none"
`
