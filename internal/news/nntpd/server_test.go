package nntpd

import (
	"errors"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/dustin/go-nntp"
)

type whitespaceBackend struct {
	group     *nntp.Group
	allowPost bool
}

func (b *whitespaceBackend) ListGroups(max int) ([]*nntp.Group, error) {
	return []*nntp.Group{b.group}, nil
}

func (b *whitespaceBackend) GetGroup(name string) (*nntp.Group, error) {
	if name == b.group.Name {
		return b.group, nil
	}
	return nil, ErrNoSuchGroup
}

func (b *whitespaceBackend) GetArticle(group *nntp.Group, id string) (*nntp.Article, error) {
	return nil, ErrInvalidArticleNumber
}

func (b *whitespaceBackend) GetArticles(group *nntp.Group, from, to int64) ([]NumberedArticle, error) {
	return nil, nil
}

func (b *whitespaceBackend) Authorized() bool { return true }

func (b *whitespaceBackend) Authenticate(user, pass string) (Backend, error) {
	return b, nil
}

func (b *whitespaceBackend) AllowPost() bool { return b.allowPost }

func (b *whitespaceBackend) Post(article *nntp.Article) error {
	return errors.New("posting disabled")
}

func TestProcessCollapsesRepeatedCommandWhitespace(t *testing.T) {
	backend := &whitespaceBackend{
		group: &nntp.Group{Name: "pfs.general", Count: 1, Low: 1, High: 1, Posting: nntp.PostingPermitted},
	}
	server := NewServer(backend)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan struct{})
	go func() {
		server.Process(serverConn)
		close(done)
	}()

	client := textproto.NewConn(clientConn)
	defer client.Close()

	if line, err := client.ReadLine(); err != nil || !strings.HasPrefix(line, "200 ") {
		t.Fatalf("greeting = %q, %v", line, err)
	}
	if err := client.PrintfLine("GROUP  pfs.general"); err != nil {
		t.Fatalf("send GROUP: %v", err)
	}
	if line, err := client.ReadLine(); err != nil || !strings.HasPrefix(line, "211 ") {
		t.Fatalf("GROUP with repeated spaces = %q, %v; want 211", line, err)
	}
	if err := client.PrintfLine("QUIT"); err != nil {
		t.Fatalf("send QUIT: %v", err)
	}
	if line, err := client.ReadLine(); err != nil || !strings.HasPrefix(line, "205 ") {
		t.Fatalf("QUIT = %q, %v; want 205", line, err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server did not close after QUIT")
	}
}

func TestIHaveWithoutMessageIDReturnsSyntaxError(t *testing.T) {
	backend := &whitespaceBackend{
		group:     &nntp.Group{Name: "pfs.general", Posting: nntp.PostingPermitted},
		allowPost: true,
	}
	server := NewServer(backend)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan struct{})
	go func() {
		server.Process(serverConn)
		close(done)
	}()

	client := textproto.NewConn(clientConn)
	defer client.Close()

	if line, err := client.ReadLine(); err != nil || !strings.HasPrefix(line, "200 ") {
		t.Fatalf("greeting = %q, %v", line, err)
	}
	if err := client.PrintfLine("IHAVE"); err != nil {
		t.Fatalf("send IHAVE: %v", err)
	}
	if line, err := client.ReadLine(); err != nil || !strings.HasPrefix(line, "501 ") {
		t.Fatalf("IHAVE without message-id = %q, %v; want 501", line, err)
	}
	if err := client.PrintfLine("QUIT"); err != nil {
		t.Fatalf("send QUIT: %v", err)
	}
	if line, err := client.ReadLine(); err != nil || !strings.HasPrefix(line, "205 ") {
		t.Fatalf("QUIT = %q, %v; want 205", line, err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server did not close after QUIT")
	}
}
