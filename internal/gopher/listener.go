package gopher

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"time"

	"github.com/charmbracelet/log"
)

// maxSelector caps the selector line a client may send. RFC 1436 selectors are
// short; a generous bound stops a peer from streaming forever.
const maxSelector = 4096

// Serve runs the public Gopher listener (RFC 1436) on addr until ctx is
// cancelled. Every connection is a single transaction: read one CRLF-terminated
// selector, resolve it with authed=false (public content only), write the
// response, close. It is read-only — nothing is ever written to the BBS here.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go s.handleConn(conn)
	}
}

// handleConn serves one gopher transaction.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	br := bufio.NewReader(&io.LimitedReader{R: conn, N: maxSelector})
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return
	}
	sel := strings.TrimRight(line, "\r\n")
	// A gopher client may append "\t$" (Gopher+) — we speak plain RFC 1436, so
	// keep only the selector up to the first tab.
	if i := strings.IndexByte(sel, '\t'); i >= 0 {
		sel = sel[:i]
	}
	resp := s.Resolve(sel, false, "")
	if _, err := conn.Write(resp.Wire()); err != nil {
		log.Debug("gopher write", "err", err)
	}
}
