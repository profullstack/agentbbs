// Package pods gives paid members a personal Linux container over SSH
// (`ssh pod@host`) — "run shit in a docker-like" without root on the host.
//
// Engine preference: rootless Podman (daemonless; container root maps to an
// unprivileged host uid via user namespaces), falling back to Docker.
//
// Capability profile differs by engine. Under rootless podman the host is
// protected by the userns mapping itself, so pods keep podman's default
// capability set — the pod's root behaves like real root (apt, chown, su,
// binding :80, ping). Under docker a breakout is host-root, so docker pods run
// non-root (uid 1000) with cap-drop ALL + no-new-privileges. Either way the SSH
// user never touches the host OS. cpu/mem/pids limits apply to both.
package pods

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/ssh"
	"github.com/creack/pty"
)

// Manager provisions and attaches per-user pods.
type Manager struct {
	engine   string // "podman" or "docker"
	image    string
	usersDir string // <data>/users on the host; when set, <user>/public_html is bind-mounted into the pod

	mu       sync.Mutex
	attached map[string]int // container name -> live session count
}

// Detect picks the best available engine. Returns an error if neither
// podman nor docker is present. usersDir is the host <data>/users tree: when
// non-empty, each pod bind-mounts <usersDir>/<user>/public_html at
// /home/dev/public_html so a member's ~/public_html is exactly the directory
// Caddy serves at <name>.<host>. Pass "" to disable the bind.
func Detect(usersDir string) (*Manager, error) {
	image := os.Getenv("AGENTBBS_POD_IMAGE")
	if image == "" {
		image = "debian:stable-slim"
	}
	for _, eng := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(eng); err == nil {
			return &Manager{engine: eng, image: image, usersDir: usersDir, attached: map[string]int{}}, nil
		}
	}
	return nil, fmt.Errorf("pods: neither podman nor docker found")
}

// publicHTMLMount returns the host path to the member's public_html and the
// "-v src:dst" volume spec that maps it into the pod, or "" for both when the
// bind is disabled. The host directory is created if absent so the mount source
// exists before the container starts.
func (m *Manager) publicHTMLMount(user string) (host, spec string) {
	if m.usersDir == "" {
		return "", ""
	}
	host = filepath.Join(m.usersDir, user, "public_html")
	if err := os.MkdirAll(host, 0o755); err != nil {
		// Non-fatal: fall back to the named-volume-only pod (no homepage bind).
		return "", ""
	}
	return host, host + ":/home/dev/public_html"
}

// Engine reports the active container engine.
func (m *Manager) Engine() string { return m.engine }

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

func (m *Manager) containerName(user string) string {
	return "agentbbs-pod-" + unsafeName.ReplaceAllString(strings.ToLower(user), "-")
}

// ensure creates (or starts) the user's container and returns its name.
func (m *Manager) ensure(user string) (string, error) {
	name := m.containerName(user)
	// Bind the host's public_html into the pod so a member's edits at
	// ~/public_html are exactly what Caddy serves at <name>.<host>.
	_, pubSpec := m.publicHTMLMount(user)
	if m.engine == "docker" {
		// Under docker the pod runs as uid 1000 (never container root), so the
		// named home volume — and the bind-mounted public_html — must be owned
		// by 1000. A trusted one-shot init container enforces that on every
		// ensure — mounts can predate the container or survive recreation.
		// Rootless podman doesn't need this: container root maps to the
		// unprivileged host user that already owns these trees.
		initArgs := []string{"run", "--rm", "-v", name + "-home:/home/dev"}
		chown := "chown 1000:1000 /home/dev"
		if pubSpec != "" {
			initArgs = append(initArgs, "-v", pubSpec)
			chown += "; chown 1000:1000 /home/dev/public_html"
		}
		initArgs = append(initArgs, m.image, "sh", "-c", chown)
		init := exec.Command(m.engine, initArgs...)
		if out, err := init.CombinedOutput(); err != nil {
			return "", fmt.Errorf("pods: volume init failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	// Already exists?
	if err := exec.Command(m.engine, "container", "inspect", name).Run(); err == nil {
		_ = exec.Command(m.engine, "start", name).Run() // no-op if running
		m.tuneApt(name)
		return name, nil
	}
	args := []string{
		"run", "-d",
		"--name", name,
		"--hostname", "pod-" + unsafeName.ReplaceAllString(user, "-"),
		"--memory", env("AGENTBBS_POD_MEM", "512m"),
		"--cpus", env("AGENTBBS_POD_CPUS", "1"),
		"--pids-limit", "256",
		"--restart", "unless-stopped",
		"-v", name + "-home:/home/dev",
		"-w", "/home/dev",
		"-e", "HOME=/home/dev",
	}
	if pubSpec != "" {
		args = append(args, "-v", pubSpec)
	}
	if m.engine == "docker" {
		// Rootful docker: a breakout is host-root, so refuse to hand out
		// container root — run as uid 1000 with no caps and no privilege
		// escalation.
		args = append(args,
			"--user", "1000:1000",
			"--cap-drop", "ALL",
			"--security-opt", "no-new-privileges",
		)
	} else {
		// Rootless podman: container root is already an unprivileged host uid
		// via user namespaces, so keep podman's default capability set and let
		// the pod's root act like real root (apt, chown, su, binding :80,
		// ping). Power users can opt into extra caps (e.g. SYS_PTRACE for
		// strace, NET_ADMIN for iptables) via AGENTBBS_POD_EXTRA_CAPS.
		for _, c := range strings.Fields(strings.ReplaceAll(env("AGENTBBS_POD_EXTRA_CAPS", ""), ",", " ")) {
			args = append(args, "--cap-add", c)
		}
	}
	args = append(args, m.image, "sleep", "infinity")
	out, err := exec.Command(m.engine, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("pods: create failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	m.tuneApt(name)
	return name, nil
}

// tuneApt makes apt usable inside the hardened pod. apt drops privileges to
// the _apt user for downloads (setgroups/setegid/seteuid), which needs
// CAP_SETUID/CAP_SETGID/CAP_CHOWN — caps we intentionally drop (cap-drop ALL).
// Rather than re-grant those to the whole container, disable apt's download
// sandbox so package management runs as the pod's (rootless-mapped) root.
//
// Only applies to the podman/container-root path; under docker the pod runs as
// uid 1000 and can't write /etc/apt (apt isn't usable there by design). Failure
// is non-fatal: a missing config just means the user sees the old apt errors.
func (m *Manager) tuneApt(name string) {
	if m.engine == "docker" {
		return
	}
	_ = exec.Command(m.engine, "exec", "--user", "root", name,
		"sh", "-c", `printf 'APT::Sandbox::User "root";\n' > /etc/apt/apt.conf.d/00no-sandbox`,
	).Run()
}

// Attach provisions the pod and wires the SSH session to a shell inside it.
// Blocks until the shell exits or the session closes.
func (m *Manager) Attach(s ssh.Session, user string) error {
	ptyReq, winCh, hasPty := s.Pty()
	if !hasPty {
		return fmt.Errorf("pods: a PTY is required (ssh -t)")
	}
	name, err := m.ensure(user)
	if err != nil {
		return err
	}

	shell := env("AGENTBBS_POD_SHELL", "/bin/bash")
	cmd := exec.Command(m.engine, "exec", "-it",
		"-e", "TERM="+ptyReq.Term,
		name, shell, "-l")
	f, err := pty.Start(cmd)
	if err != nil {
		// busybox-ish images may lack bash
		cmd = exec.Command(m.engine, "exec", "-it", "-e", "TERM="+ptyReq.Term, name, "/bin/sh", "-l")
		f, err = pty.Start(cmd)
		if err != nil {
			return fmt.Errorf("pods: attach failed: %w", err)
		}
	}
	defer f.Close()

	m.ref(name, +1)
	defer m.deref(name)

	_ = pty.Setsize(f, &pty.Winsize{Rows: uint16(ptyReq.Window.Height), Cols: uint16(ptyReq.Window.Width)})
	go func() {
		for w := range winCh {
			_ = pty.Setsize(f, &pty.Winsize{Rows: uint16(w.Height), Cols: uint16(w.Width)})
		}
	}()

	go func() { _, _ = io.Copy(f, s) }() // ssh -> pod
	_, _ = io.Copy(s, f)                 // pod -> ssh
	_ = cmd.Wait()
	return nil
}

// Exec provisions the user's pod and runs argv inside it wired to the SSH
// session (PTY required). Used for tor@/tor-irc@ so arbitrary or interactive
// commands run sandboxed in the member's container, never on the host. Blocks
// until the command exits or the session closes.
func (m *Manager) Exec(s ssh.Session, user string, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("pods: no command given")
	}
	ptyReq, winCh, hasPty := s.Pty()
	if !hasPty {
		return fmt.Errorf("pods: a PTY is required (ssh -t)")
	}
	name, err := m.ensure(user)
	if err != nil {
		return err
	}

	args := append([]string{"exec", "-it", "-e", "TERM=" + ptyReq.Term, name}, argv...)
	cmd := exec.Command(m.engine, args...)
	f, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pods: exec failed: %w", err)
	}
	defer f.Close()

	m.ref(name, +1)
	defer m.deref(name)

	_ = pty.Setsize(f, &pty.Winsize{Rows: uint16(ptyReq.Window.Height), Cols: uint16(ptyReq.Window.Width)})
	go func() {
		for w := range winCh {
			_ = pty.Setsize(f, &pty.Winsize{Rows: uint16(w.Height), Cols: uint16(w.Width)})
		}
	}()

	go func() { _, _ = io.Copy(f, s) }() // ssh -> pod
	_, _ = io.Copy(s, f)                 // pod -> ssh
	_ = cmd.Wait()
	return nil
}

func (m *Manager) ref(name string, d int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attached[name] += d
}

// deref stops the container shortly after the last session detaches, unless
// AGENTBBS_POD_KEEP=1 keeps pods running between visits.
func (m *Manager) deref(name string) {
	m.mu.Lock()
	m.attached[name]--
	last := m.attached[name] <= 0
	m.mu.Unlock()
	if last && os.Getenv("AGENTBBS_POD_KEEP") != "1" {
		go func() { _ = exec.Command(m.engine, "stop", "-t", "2", name).Run() }()
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
