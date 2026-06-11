// Package pods gives paid members a personal Linux container over SSH
// (`ssh pod@host`) — "run shit in a docker-like" without root on the host.
//
// Engine preference: rootless Podman (daemonless; container root maps to an
// unprivileged host uid via user namespaces), falling back to Docker with a
// hardened profile (cap-drop ALL, no-new-privileges, non-root user, cpu/mem/
// pids caps). Either way the SSH user never touches the host OS.
package pods

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/ssh"
	"github.com/creack/pty"
)

// Manager provisions and attaches per-user pods.
type Manager struct {
	engine string // "podman" or "docker"
	image  string

	mu       sync.Mutex
	attached map[string]int // container name -> live session count
}

// Detect picks the best available engine. Returns an error if neither
// podman nor docker is present.
func Detect() (*Manager, error) {
	image := os.Getenv("AGENTBBS_POD_IMAGE")
	if image == "" {
		image = "debian:stable-slim"
	}
	for _, eng := range []string{"podman", "docker"} {
		if _, err := exec.LookPath(eng); err == nil {
			return &Manager{engine: eng, image: image, attached: map[string]int{}}, nil
		}
	}
	return nil, fmt.Errorf("pods: neither podman nor docker found")
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
	if m.engine == "docker" {
		// Under docker the pod runs as uid 1000 (never container root), so
		// the named home volume must be owned by 1000. A trusted one-shot
		// init container enforces that on every ensure — volumes can predate
		// the container or survive recreation. Rootless podman doesn't need
		// this: container root maps to the unprivileged host user.
		init := exec.Command(m.engine, "run", "--rm",
			"-v", name+"-home:/home/dev", m.image,
			"sh", "-c", "chown 1000:1000 /home/dev")
		if out, err := init.CombinedOutput(); err != nil {
			return "", fmt.Errorf("pods: volume init failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	// Already exists?
	if err := exec.Command(m.engine, "container", "inspect", name).Run(); err == nil {
		_ = exec.Command(m.engine, "start", name).Run() // no-op if running
		return name, nil
	}
	args := []string{
		"run", "-d",
		"--name", name,
		"--hostname", "pod-" + unsafeName.ReplaceAllString(user, "-"),
		"--memory", env("AGENTBBS_POD_MEM", "512m"),
		"--cpus", env("AGENTBBS_POD_CPUS", "1"),
		"--pids-limit", "256",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--restart", "unless-stopped",
		"-v", name + "-home:/home/dev",
		"-w", "/home/dev",
		"-e", "HOME=/home/dev",
	}
	if m.engine == "docker" {
		// Rootless podman user-ns maps container root safely; under docker,
		// refuse to hand out container root at all.
		args = append(args, "--user", "1000:1000")
	}
	args = append(args, m.image, "sleep", "infinity")
	out, err := exec.Command(m.engine, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("pods: create failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return name, nil
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
