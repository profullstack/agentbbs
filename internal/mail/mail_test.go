package mail

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Config
		want bool
	}{
		{"host+from", Config{Host: "mail.example.com", From: "bbs@example.com"}, true},
		{"no host", Config{From: "bbs@example.com"}, false},
		{"no from", Config{Host: "mail.example.com"}, false},
		{"empty", Config{}, false},
	} {
		if got := tc.c.Configured(); got != tc.want {
			t.Errorf("%s: Configured() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPortDefaultsToSubmission(t *testing.T) {
	if got := (Config{}).port(); got != "587" {
		t.Errorf("port() = %q, want 587", got)
	}
	if got := (Config{Port: "25"}).port(); got != "25" {
		t.Errorf("port() = %q, want 25", got)
	}
}

// A loopback relay presents a certificate for its public mail host, so the
// name STARTTLS is verified against has to be overridable.
func TestTLSServerNameOverride(t *testing.T) {
	if got := (Config{Host: "127.0.0.1"}).tlsServerName(); got != "127.0.0.1" {
		t.Errorf("tlsServerName() = %q, want the host", got)
	}
	c := Config{Host: "127.0.0.1", ServerName: "mail.example.com"}
	if got := c.tlsServerName(); got != "mail.example.com" {
		t.Errorf("tlsServerName() = %q, want the override", got)
	}
}

func TestIsLoopback(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"[::1]", true},
		{"localhost", true},
		{"LocalHost", true},
		{"mail.example.com", false},
		{"10.0.0.5", false},
		{"", false},
	} {
		if got := isLoopback(tc.host); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestSendUnconfigured(t *testing.T) {
	if err := (Config{}).Send("a@example.com", "s", "b"); err == nil {
		t.Fatal("Send() on an unconfigured relay should fail")
	}
}

// fakeSMTP is a minimal ESMTP server that does not advertise STARTTLS. It
// records the dialogue so a test can assert on the envelope and body.
func fakeSMTP(t *testing.T) (addr string, got func() string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	done := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- ""
			return
		}
		defer func() { _ = conn.Close() }()
		var log strings.Builder
		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)
		say := func(s string) {
			_, _ = w.WriteString(s + "\r\n")
			_ = w.Flush()
		}
		say("220 fake ESMTP ready")
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			log.WriteString(line)
			line = strings.TrimRight(line, "\r\n")
			switch {
			case inData:
				if line == "." {
					inData = false
					say("250 2.0.0 queued")
				}
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				say("250-fake")
				say("250 8BITMIME")
			case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
				say("250 2.0.0 ok")
			case line == "DATA":
				inData = true
				say("354 go ahead")
			case line == "QUIT":
				say("221 2.0.0 bye")
				done <- log.String()
				return
			default:
				say("250 2.0.0 ok")
			}
		}
		done <- log.String()
	}()
	return ln.Addr().String(), func() string { return <-done }
}

func TestSendDeliversMessage(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	c := Config{Host: host, Port: port, From: "bbs@example.com"}
	if err := c.Send("member@example.net", "Your AgentBBS confirmation code", "code: 123456\nthanks"); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	dialogue := got()
	for _, want := range []string{
		"MAIL FROM:<bbs@example.com>",
		"RCPT TO:<member@example.net>",
		"Subject: Your AgentBBS confirmation code",
		"code: 123456",
	} {
		if !strings.Contains(dialogue, want) {
			t.Errorf("dialogue missing %q:\n%s", want, dialogue)
		}
	}
	// The body must be CRLF-terminated on the wire, not bare LF.
	if strings.Contains(dialogue, "code: 123456\nthanks") {
		t.Error("body lines were not CRLF-normalised")
	}
}

// A relay that cannot be reached must surface a wrapped error naming the
// address, so the operator can tell "nothing is listening" from "TLS refused".
func TestSendUnreachableRelayNamesAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing is listening there now

	host, port, _ := net.SplitHostPort(addr)
	err = Config{Host: host, Port: port, From: "bbs@example.com"}.Send("a@example.com", "s", "b")
	if err == nil {
		t.Fatal("Send() to a dead relay should fail")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error %q should name the relay address %q", err, addr)
	}
}

// selfSignedFor mints a certificate for name that is already expired — the
// exact condition that took registration down: an on-box relay whose cert
// lapsed.
func selfSignedFor(t *testing.T, name string) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-90 * 24 * time.Hour),
		NotAfter:     time.Now().Add(-24 * time.Hour), // expired yesterday
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// starttlsSMTP is a fake relay that advertises STARTTLS and serves an expired
// certificate for a name that is not the address being dialled.
func starttlsSMTP(t *testing.T, certName string) (host, port string, delivered <-chan bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	cert := selfSignedFor(t, certName)

	done := make(chan bool, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- false
			return
		}
		defer func() { _ = conn.Close() }()

		serve := func(c net.Conn, tlsUp bool) bool {
			r := bufio.NewReader(c)
			w := bufio.NewWriter(c)
			say := func(s string) {
				_, _ = w.WriteString(s + "\r\n")
				_ = w.Flush()
			}
			if !tlsUp {
				say("220 fake ESMTP ready")
			}
			inData := false
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return false
				}
				line = strings.TrimRight(line, "\r\n")
				switch {
				case inData:
					if line == "." {
						inData = false
						say("250 2.0.0 queued")
					}
				case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
					say("250-fake")
					if !tlsUp {
						say("250 STARTTLS")
					} else {
						say("250 8BITMIME")
					}
				case line == "STARTTLS":
					return true // caller upgrades and re-serves
				case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
					say("250 2.0.0 ok")
				case line == "DATA":
					inData = true
					say("354 go ahead")
				case line == "QUIT":
					say("221 2.0.0 bye")
					return false
				default:
					say("250 2.0.0 ok")
				}
			}
		}

		if !serve(conn, false) {
			done <- false
			return
		}
		// Acknowledge STARTTLS, then hand the connection to TLS.
		_, _ = conn.Write([]byte("220 2.0.0 ready to start TLS\r\n"))
		tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
		if err := tc.Handshake(); err != nil {
			done <- false
			return
		}
		serve(tc, true)
		done <- true
	}()

	h, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	return h, p, done
}

// The regression this guards: an expired/mismatched certificate on a co-located
// relay must NOT be able to block join@ registration. Loopback traffic cannot be
// intercepted, so the sender proceeds instead of failing the signup.
func TestSendLoopbackRelayToleratesBadCert(t *testing.T) {
	host, port, delivered := starttlsSMTP(t, "mail.example.com")
	c := Config{Host: host, Port: port, From: "bbs@example.com", ServerName: "mail.example.com"}
	if err := c.Send("member@example.net", "Your AgentBBS confirmation code", "code: 123456"); err != nil {
		t.Fatalf("Send() over a loopback relay with an expired cert = %v, want nil", err)
	}
	if !<-delivered {
		t.Error("relay did not complete the TLS session")
	}
}

// The same expired cert on a relay that is NOT loopback must still be rejected:
// skipping verification is a loopback-only concession, not a blanket opt-out.
func TestSendRemoteRelayRejectsBadCert(t *testing.T) {
	_, port, _ := starttlsSMTP(t, "mail.example.com")
	// "localhost." (trailing dot) resolves to 127.0.0.1 but is not recognised as
	// a loopback literal, so the verified path is exercised against a real dial.
	c := Config{Host: "localhost.", Port: port, From: "bbs@example.com"}
	err := c.Send("member@example.net", "s", "b")
	if err == nil {
		t.Fatal("Send() to a non-loopback relay with an expired cert should fail")
	}
	if !strings.Contains(err.Error(), "starttls") {
		t.Errorf("error %q should identify the STARTTLS stage", err)
	}
}
