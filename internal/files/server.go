package files

import (
	"io"

	"github.com/charmbracelet/ssh"
	"github.com/pkg/sftp"

	"github.com/profullstack/agentbbs/internal/auth"
)

// Subsystem returns the wish/ssh "sftp" subsystem handler. Wire it with
//
//	wish.WithSubsystem("sftp", svc.Subsystem())
//
// so members reach their files over the existing :22 listener. Identity is the
// connecting SSH key (the username is ignored); access is refused for non-members,
// banned accounts, and members whose SFTP access an operator has revoked.
func (s *Service) Subsystem() ssh.SubsystemHandler {
	return func(sess ssh.Session) {
		fp := auth.Fingerprint(sess.PublicKey())
		if fp == "" {
			io.WriteString(sess.Stderr(), "files: an SSH key is required (try: sftp -i ~/.ssh/id_ed25519 ...)\n")
			_ = sess.Exit(1)
			return
		}
		u, ok, err := s.st.UserByFingerprint(fp)
		if err != nil {
			io.WriteString(sess.Stderr(), "files: account lookup failed\n")
			_ = sess.Exit(1)
			return
		}
		if !ok {
			io.WriteString(sess.Stderr(), "files: this key isn't a member — register first: ssh join@\n")
			_ = sess.Exit(1)
			return
		}
		if u.Banned {
			io.WriteString(sess.Stderr(), "files: this account is suspended\n")
			_ = sess.Exit(1)
			return
		}
		if fa, err := s.st.FilesAccess(u.ID); err == nil && fa.Revoked {
			io.WriteString(sess.Stderr(), "files: SFTP access has been revoked for this account\n")
			_ = sess.Exit(1)
			return
		}

		fsSess, err := s.newSession(u)
		if err != nil {
			io.WriteString(sess.Stderr(), "files: could not open your workspace\n")
			_ = sess.Exit(1)
			return
		}

		rw := &countingRWC{inner: sess}
		conn := s.reg.add(u.Name, fp, sess.RemoteAddr().String(), rw.Close)
		rw.conn = conn
		defer s.reg.remove(conn.id)

		handlers := sftp.Handlers{FileGet: fsSess, FilePut: fsSess, FileCmd: fsSess, FileList: fsSess}
		srv := sftp.NewRequestServer(rw, handlers)
		defer srv.Close()
		if err := srv.Serve(); err != nil && err != io.EOF {
			io.WriteString(sess.Stderr(), "files: session ended\n")
		}
	}
}

// countingRWC wraps the SSH channel to meter bytes for the management TUI and to
// expose Close for force-disconnect. Read = bytes from the client (uploads);
// Write = bytes to the client (downloads).
type countingRWC struct {
	inner io.ReadWriteCloser
	conn  *liveConn
}

func (c *countingRWC) Read(p []byte) (int, error) {
	n, err := c.inner.Read(p)
	if c.conn != nil {
		c.conn.rxBytes.Add(int64(n))
	}
	return n, err
}

func (c *countingRWC) Write(p []byte) (int, error) {
	n, err := c.inner.Write(p)
	if c.conn != nil {
		c.conn.txBytes.Add(int64(n))
	}
	return n, err
}

func (c *countingRWC) Close() error { return c.inner.Close() }
