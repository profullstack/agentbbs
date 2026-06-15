// Package nntpd is a vendored, lightly patched copy of
// github.com/dustin/go-nntp/server (MIT, Dustin Sallings). Two changes make it
// a faithful, members-only Usenet server:
//
//  1. AUTHINFO USER/PASS now return RFC 4643 response codes (381 "password
//     required" then 281 "authentication accepted"). Upstream returned 350/250,
//     which neither the matching go-nntp client nor standard newsreaders
//     (slrn, tin) accept — so authentication was effectively broken.
//  2. The per-command and drop-connection logging is routed through an optional
//     Server.ErrLog and the noisy "Got cmd" line is dropped (a public server on
//     a small box should not log every protocol verb).
//
// Everything else is upstream. The Backend interface is unchanged, so backends
// written against go-nntp/server work here verbatim.
package nntpd

import (
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/dustin/go-nntp"
)

// An NNTPError is a coded NNTP error message.
type NNTPError struct {
	Code int
	Msg  string
}

// Sentinel errors, returned by backends and rendered to the client.
var (
	ErrNoSuchGroup          = &NNTPError{411, "No such newsgroup"}
	ErrNoGroupSelected      = &NNTPError{412, "No newsgroup selected"}
	ErrInvalidMessageID     = &NNTPError{430, "No article with that message-id"}
	ErrInvalidArticleNumber = &NNTPError{423, "No article with that number"}
	ErrNoCurrentArticle     = &NNTPError{420, "Current article number is invalid"}
	ErrUnknownCommand       = &NNTPError{500, "Unknown command"}
	ErrSyntax               = &NNTPError{501, "not supported, or syntax error"}
	ErrPostingNotPermitted  = &NNTPError{440, "Posting not permitted"}
	ErrPostingFailed        = &NNTPError{441, "posting failed"}
	ErrNotWanted            = &NNTPError{435, "Article not wanted"}
	ErrAuthRequired         = &NNTPError{450, "authorization required"}
	ErrAuthRejected         = &NNTPError{452, "authorization rejected"}
	ErrNotAuthenticated     = &NNTPError{480, "authentication required"}
)

func (e *NNTPError) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Msg) }

// Handler is a low-level protocol handler.
type Handler func(args []string, s *session, c *textproto.Conn) error

// NumberedArticle ties an article to its per-group sequence number.
type NumberedArticle struct {
	Num     int64
	Article *nntp.Article
}

// Backend provides the data and does the work.
type Backend interface {
	ListGroups(max int) ([]*nntp.Group, error)
	GetGroup(name string) (*nntp.Group, error)
	GetArticle(group *nntp.Group, id string) (*nntp.Article, error)
	GetArticles(group *nntp.Group, from, to int64) ([]NumberedArticle, error)
	Authorized() bool
	// Authenticate validates the credentials and optionally returns a
	// replacement backend for this session (nil keeps the current one).
	Authenticate(user, pass string) (Backend, error)
	AllowPost() bool
	Post(article *nntp.Article) error
}

type session struct {
	server  *Server
	backend Backend
	group   *nntp.Group
}

// Server is an NNTP server handle.
type Server struct {
	Handlers map[string]Handler
	Backend  Backend
	// ErrLog, when non-nil, receives connection-level errors. Protocol verbs
	// are never logged (a public server should not log every command).
	ErrLog *log.Logger
	group  *nntp.Group
}

// NewServer builds a server bound to a backend.
func NewServer(backend Backend) *Server {
	rv := Server{
		Handlers: make(map[string]Handler),
		Backend:  backend,
	}
	rv.Handlers[""] = handleDefault
	rv.Handlers["quit"] = handleQuit
	rv.Handlers["group"] = handleGroup
	rv.Handlers["list"] = handleList
	rv.Handlers["head"] = handleHead
	rv.Handlers["body"] = handleBody
	rv.Handlers["article"] = handleArticle
	rv.Handlers["post"] = handlePost
	rv.Handlers["ihave"] = handleIHave
	rv.Handlers["capabilities"] = handleCap
	rv.Handlers["mode"] = handleMode
	rv.Handlers["authinfo"] = handleAuthInfo
	rv.Handlers["newgroups"] = handleNewGroups
	rv.Handlers["over"] = handleOver
	rv.Handlers["xover"] = handleOver
	return &rv
}

func (s *Server) logf(format string, args ...any) {
	if s.ErrLog != nil {
		s.ErrLog.Printf(format, args...)
	}
}

func (s *session) dispatchCommand(cmd string, args []string, c *textproto.Conn) error {
	handler, found := s.server.Handlers[strings.ToLower(cmd)]
	if !found {
		handler = s.server.Handlers[""]
	}
	return handler(args, s, c)
}

// Process handles a single NNTP connection.
func (s *Server) Process(nc net.Conn) {
	defer nc.Close()
	c := textproto.NewConn(nc)

	sess := &session{server: s, backend: s.Backend, group: nil}

	_ = c.PrintfLine("200 Hello!")
	for {
		l, err := c.ReadLine()
		if err != nil {
			return
		}
		fields := strings.Fields(l)
		if len(fields) == 0 {
			err = handleDefault(nil, sess, c)
		} else {
			args := []string{}
			if len(fields) > 1 {
				args = fields[1:]
			}
			err = sess.dispatchCommand(fields[0], args, c)
		}
		if err != nil {
			if _, isNNTPError := err.(*NNTPError); err == io.EOF {
				return
			} else if isNNTPError {
				_ = c.PrintfLine("%s", err.Error())
			} else {
				s.logf("dropping conn: %v", err)
				return
			}
		}
	}
}

func parseRange(spec string) (low, high int64) {
	if spec == "" {
		return 0, math.MaxInt64
	}
	parts := strings.Split(spec, "-")
	if len(parts) == 1 {
		h, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			h = math.MaxInt64
		}
		return 0, h
	}
	l, _ := strconv.ParseInt(parts[0], 10, 64)
	h, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		h = math.MaxInt64
	}
	return l, h
}

func handleOver(args []string, s *session, c *textproto.Conn) error {
	if s.group == nil {
		return ErrNoGroupSelected
	}
	spec := ""
	if len(args) > 0 {
		spec = args[0]
	}
	from, to := parseRange(spec)
	articles, err := s.backend.GetArticles(s.group, from, to)
	if err != nil {
		return err
	}
	_ = c.PrintfLine("224 here it comes")
	dw := c.DotWriter()
	defer dw.Close()
	for _, a := range articles {
		fmt.Fprintf(dw, "%d\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n", a.Num,
			a.Article.Header.Get("Subject"),
			a.Article.Header.Get("From"),
			a.Article.Header.Get("Date"),
			a.Article.Header.Get("Message-Id"),
			a.Article.Header.Get("References"),
			a.Article.Bytes, a.Article.Lines)
	}
	return nil
}

func handleListOverviewFmt(c *textproto.Conn) error {
	if err := c.PrintfLine("215 Order of fields in overview database."); err != nil {
		return err
	}
	dw := c.DotWriter()
	defer dw.Close()
	_, err := fmt.Fprintln(dw, `Subject:
From:
Date:
Message-ID:
References:
:bytes
:lines`)
	return err
}

func handleList(args []string, s *session, c *textproto.Conn) error {
	ltype := "active"
	if len(args) > 0 {
		ltype = strings.ToLower(args[0])
	}
	if ltype == "overview.fmt" {
		return handleListOverviewFmt(c)
	}
	groups, err := s.backend.ListGroups(-1)
	if err != nil {
		return err
	}
	_ = c.PrintfLine("215 list of newsgroups follows")
	dw := c.DotWriter()
	defer dw.Close()
	for _, g := range groups {
		switch ltype {
		case "active":
			fmt.Fprintf(dw, "%s %d %d %v\r\n", g.Name, g.High, g.Low, g.Posting)
		case "newsgroups":
			fmt.Fprintf(dw, "%s %s\r\n", g.Name, g.Description)
		}
	}
	return nil
}

func handleNewGroups(args []string, s *session, c *textproto.Conn) error {
	_ = c.PrintfLine("231 list of newsgroups follows")
	_ = c.PrintfLine(".")
	return nil
}

func handleDefault(args []string, s *session, c *textproto.Conn) error {
	return ErrUnknownCommand
}

func handleQuit(args []string, s *session, c *textproto.Conn) error {
	_ = c.PrintfLine("205 bye")
	return io.EOF
}

func handleGroup(args []string, s *session, c *textproto.Conn) error {
	if len(args) < 1 {
		return ErrNoSuchGroup
	}
	group, err := s.backend.GetGroup(args[0])
	if err != nil {
		return err
	}
	s.group = group
	_ = c.PrintfLine("211 %d %d %d %s", group.Count, group.Low, group.High, group.Name)
	return nil
}

func (s *session) getArticle(args []string) (*nntp.Article, error) {
	if len(args) == 0 {
		return nil, ErrNoCurrentArticle
	}
	if strings.HasPrefix(args[0], "<") {
		return s.backend.GetArticle(s.group, args[0])
	}
	if s.group == nil {
		return nil, ErrNoGroupSelected
	}
	return s.backend.GetArticle(s.group, args[0])
}

func handleHead(args []string, s *session, c *textproto.Conn) error {
	article, err := s.getArticle(args)
	if err != nil {
		return err
	}
	_ = c.PrintfLine("221 1 %s", article.MessageID())
	dw := c.DotWriter()
	defer dw.Close()
	for k, v := range article.Header {
		fmt.Fprintf(dw, "%s: %s\r\n", k, v[0])
	}
	return nil
}

func handleBody(args []string, s *session, c *textproto.Conn) error {
	article, err := s.getArticle(args)
	if err != nil {
		return err
	}
	_ = c.PrintfLine("222 1 %s", article.MessageID())
	dw := c.DotWriter()
	defer dw.Close()
	_, err = io.Copy(dw, article.Body)
	return err
}

func handleArticle(args []string, s *session, c *textproto.Conn) error {
	article, err := s.getArticle(args)
	if err != nil {
		return err
	}
	_ = c.PrintfLine("220 1 %s", article.MessageID())
	dw := c.DotWriter()
	defer dw.Close()
	for k, v := range article.Header {
		fmt.Fprintf(dw, "%s: %s\r\n", k, v[0])
	}
	fmt.Fprintln(dw, "")
	_, err = io.Copy(dw, article.Body)
	return err
}

func handlePost(args []string, s *session, c *textproto.Conn) error {
	if !s.backend.AllowPost() {
		return ErrPostingNotPermitted
	}
	_ = c.PrintfLine("340 Go ahead")
	var err error
	var article nntp.Article
	article.Header, err = c.ReadMIMEHeader()
	if err != nil {
		return ErrPostingFailed
	}
	article.Body = c.DotReader()
	if err = s.backend.Post(&article); err != nil {
		return err
	}
	_ = c.PrintfLine("240 article received OK")
	return nil
}

func handleIHave(args []string, s *session, c *textproto.Conn) error {
	if !s.backend.AllowPost() {
		return ErrNotWanted
	}
	article, err := s.backend.GetArticle(nil, args[0])
	if article != nil {
		return ErrNotWanted
	}
	_ = c.PrintfLine("335 send it")
	article = &nntp.Article{}
	article.Header, err = c.ReadMIMEHeader()
	if err != nil {
		return ErrPostingFailed
	}
	article.Body = c.DotReader()
	if err = s.backend.Post(article); err != nil {
		return err
	}
	_ = c.PrintfLine("235 article received OK")
	return nil
}

func handleCap(args []string, s *session, c *textproto.Conn) error {
	_ = c.PrintfLine("101 Capability list:")
	dw := c.DotWriter()
	defer dw.Close()
	fmt.Fprintf(dw, "VERSION 2\n")
	fmt.Fprintf(dw, "READER\n")
	fmt.Fprintf(dw, "AUTHINFO USER\n")
	if s.backend.AllowPost() {
		fmt.Fprintf(dw, "POST\n")
		fmt.Fprintf(dw, "IHAVE\n")
	}
	fmt.Fprintf(dw, "OVER\n")
	fmt.Fprintf(dw, "XOVER\n")
	fmt.Fprintf(dw, "LIST ACTIVE NEWSGROUPS OVERVIEW.FMT\n")
	return nil
}

func handleMode(args []string, s *session, c *textproto.Conn) error {
	if s.backend.AllowPost() {
		_ = c.PrintfLine("200 Posting allowed")
	} else {
		_ = c.PrintfLine("201 Posting prohibited")
	}
	return nil
}

// handleAuthInfo implements RFC 4643 AUTHINFO USER/PASS (381 then 281).
func handleAuthInfo(args []string, s *session, c *textproto.Conn) error {
	if len(args) < 2 {
		return ErrSyntax
	}
	if strings.ToLower(args[0]) != "user" {
		return ErrSyntax
	}
	if s.backend.Authorized() {
		return c.PrintfLine("281 already authenticated")
	}

	if err := c.PrintfLine("381 Password required"); err != nil {
		return err
	}
	a, err := c.ReadLine()
	if err != nil {
		return err
	}
	parts := strings.SplitN(a, " ", 3)
	if len(parts) < 3 || strings.ToLower(parts[0]) != "authinfo" || strings.ToLower(parts[1]) != "pass" {
		return ErrSyntax
	}
	b, err := s.backend.Authenticate(args[1], parts[2])
	if err != nil {
		return err
	}
	if b != nil {
		s.backend = b
	}
	return c.PrintfLine("281 authentication accepted")
}
