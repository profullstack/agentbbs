package news

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/dustin/go-nntp"
	nntpclient "github.com/dustin/go-nntp/client"
)

// Reader is a thin NNTP client used by the in-BBS `news@` TUI. It dials the
// loopback listener and authenticates as the member, so it exercises the same
// server path (auth, From-stamping, numbering) as external newsreaders.
type Reader struct {
	c    *nntpclient.Client
	user string
}

// Dial connects to the loopback NNTP server at addr and authenticates as user.
// The password is ignored by the server (membership is the credential).
func Dial(addr, user string) (*Reader, error) {
	c, err := nntpclient.New("tcp", addr)
	if err != nil {
		return nil, err
	}
	if _, err := c.Authenticate(user, "-"); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("auth: %w", err)
	}
	return &Reader{c: c, user: user}, nil
}

// Close ends the NNTP session.
func (r *Reader) Close() error { return r.c.Close() }

// User is the authenticated member name.
func (r *Reader) User() string { return r.user }

// Groups lists the available newsgroups, name-sorted.
func (r *Reader) Groups() ([]nntp.Group, error) {
	gs, err := r.c.List("active")
	if err != nil {
		return nil, err
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Name < gs[j].Name })
	return gs, nil
}

// Select makes group current and returns its bounds.
func (r *Reader) Select(name string) (nntp.Group, error) { return r.c.Group(name) }

// Overview is one row of a group's article overview.
type Overview struct {
	Num     int64
	Subject string
	From    string
	Date    string
	MsgID   string
	Refs    string
}

// Overview fetches the overview rows for [low,high] in the current group.
func (r *Reader) Overview(low, high int64) ([]Overview, error) {
	if high < low || high == 0 {
		return nil, nil
	}
	lines, err := r.c.Over(fmt.Sprintf("%d-%d", low, high))
	if err != nil {
		return nil, err
	}
	out := make([]Overview, 0, len(lines))
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) < 6 {
			continue
		}
		num, _ := strconv.ParseInt(f[0], 10, 64)
		out = append(out, Overview{
			Num: num, Subject: f[1], From: f[2], Date: f[3], MsgID: f[4], Refs: f[5],
		})
	}
	return out, nil
}

// Article returns the full article (headers and body) for a number in the
// current group.
func (r *Reader) Article(num int64) (string, error) {
	_, _, rd, err := r.c.Article(strconv.FormatInt(num, 10))
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(rd)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Post submits an article to group with the given subject/body. References ties
// a reply to its parent (its Message-ID). The server stamps the From header.
func (r *Reader) Post(group, subject, references, body string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Newsgroups: %s\r\n", group)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	if references != "" {
		fmt.Fprintf(&b, "References: %s\r\n", references)
	}
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return r.c.Post(strings.NewReader(b.String()))
}
