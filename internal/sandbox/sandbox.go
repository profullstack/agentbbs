// Package sandbox wraps game/agent subprocesses with per-session isolation
// and resource limits (PRD §7 S2). It prefers bubblewrap, falls back to
// prlimit, and degrades to a plain exec with a warning.
package sandbox

import (
	"fmt"
	"os/exec"
)

// Mode selects the isolation technology.
type Mode string

const (
	ModeAuto    Mode = "auto"
	ModeBwrap   Mode = "bwrap"
	ModePrlimit Mode = "prlimit"
	ModeNone    Mode = "none"
)

// Limits are per-process resource caps.
type Limits struct {
	CPUSeconds int // hard CPU-time cap (fork-bomb/runaway protection)
	MemoryMB   int
	MaxProcs   int
}

// DefaultLimits suit a single interactive game session.
var DefaultLimits = Limits{CPUSeconds: 3600, MemoryMB: 512, MaxProcs: 64}

// Runner builds sandboxed exec.Cmds.
type Runner struct {
	mode Mode
}

// New picks the best available mode when ModeAuto is requested.
func New(mode Mode) *Runner {
	if mode == "" || mode == ModeAuto {
		switch {
		case have("bwrap"):
			mode = ModeBwrap
		case have("prlimit"):
			mode = ModePrlimit
		default:
			mode = ModeNone
		}
	}
	return &Runner{mode: mode}
}

// Mode reports the active isolation mode.
func (r *Runner) Mode() Mode { return r.mode }

func have(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

// Command wraps program+args in the runner's sandbox. workDir is the only
// writable path (savegames land there); everything else is read-only.
func (r *Runner) Command(workDir, program string, args ...string) *exec.Cmd {
	lim := DefaultLimits
	switch r.mode {
	case ModeBwrap:
		bw := []string{
			"--ro-bind", "/", "/",
			"--dev", "/dev",
			"--proc", "/proc",
			"--tmpfs", "/tmp",
			"--bind", workDir, workDir,
			"--unshare-net",
			"--unshare-pid",
			"--die-with-parent",
			"--chdir", workDir,
		}
		// Resource caps still come from prlimit when available.
		if have("prlimit") {
			pl := prlimitArgs(lim)
			full := append(pl, "bwrap")
			full = append(full, bw...)
			full = append(full, "--", program)
			full = append(full, args...)
			return exec.Command("prlimit", full...)
		}
		full := append(bw, "--", program)
		full = append(full, args...)
		return exec.Command("bwrap", full...)
	case ModePrlimit:
		full := append(prlimitArgs(lim), program)
		full = append(full, args...)
		cmd := exec.Command("prlimit", full...)
		cmd.Dir = workDir
		return cmd
	default:
		cmd := exec.Command(program, args...)
		cmd.Dir = workDir
		return cmd
	}
}

func prlimitArgs(l Limits) []string {
	return []string{
		fmt.Sprintf("--cpu=%d", l.CPUSeconds),
		fmt.Sprintf("--as=%d", l.MemoryMB*1024*1024),
		fmt.Sprintf("--nproc=%d", l.MaxProcs),
		"--",
	}
}
