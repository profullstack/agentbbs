package arcade

import (
	"io"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
)

// ptyExec is a tea.ExecCommand that runs the child on a real host PTY and
// bridges it to the session streams. Needed because doom-ascii (like most
// raw-mode terminal programs) demands an actual TTY, and over SSH the
// bubbletea program's stdin/stdout are session streams, not a host TTY.
type ptyExec struct {
	cmd           *exec.Cmd
	stdin         io.Reader
	stdout        io.Writer
	width, height int
}

func newPtyExec(cmd *exec.Cmd, width, height int) *ptyExec {
	return &ptyExec{cmd: cmd, width: width, height: height}
}

func (p *ptyExec) SetStdin(r io.Reader)  { p.stdin = r }
func (p *ptyExec) SetStdout(w io.Writer) { p.stdout = w }
func (p *ptyExec) SetStderr(io.Writer)   {}

var _ tea.ExecCommand = (*ptyExec)(nil)

func (p *ptyExec) Run() error {
	f, err := pty.StartWithSize(p.cmd, &pty.Winsize{
		Rows: uint16(max(p.height, 24)),
		Cols: uint16(max(p.width, 80)),
	})
	if err != nil {
		return err
	}
	defer f.Close()

	done := make(chan struct{})
	go func() { _, _ = io.Copy(p.stdout, f); close(done) }()
	go func() {
		_, _ = io.Copy(f, p.stdin)
		// Session input is gone (disconnect): don't leave the game orphaned
		// on the host (PRD §7 S3 — abandoned sessions are reaped).
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	}()

	err = p.cmd.Wait()
	<-done
	return err
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
