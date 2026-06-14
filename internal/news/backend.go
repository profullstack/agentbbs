// Package news implements the members-only Usenet (NNTP) server that backs
// news.<host> for AgentBBS members (free and paid alike). The RFC 3977 protocol
// engine is the vendored internal/news/nntpd (a patched go-nntp/server); the
// nntp.Article/Group wire types come from github.com/dustin/go-nntp. Articles
// live in the shared SQLite store. See docs/news.md.
//
// Access control: the NNTP handlers themselves do not gate reads, so the network
// is made members-only inside the backend. The anonymous backend refuses every
// data method with "authorization required"; AUTHINFO swaps in an authenticated
// backend bound to the member. Membership IS the credential — the password is
// ignored (this is a private, TLS-only net), exactly like the co-located IRC
// network.
package news

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-nntp"
	"github.com/profullstack/agentbbs/internal/news/nntpd"
	"github.com/profullstack/agentbbs/internal/store"
)

// NewsStore is the slice of the store the NNTP backend needs.
type NewsStore interface {
	UserByName(name string) (store.User, bool, error)
	EnsureNewsGroup(name, description string) error
	NewsGroups() ([]store.NewsGroup, error)
	NewsGroup(name string) (store.NewsGroup, bool, error)
	NewsArticleByNum(group string, num int64) (store.NewsArticle, bool, error)
	NewsArticleByMsgID(msgID string) (store.NewsArticle, bool, error)
	NewsArticlesRange(group string, from, to int64) ([]store.NewsArticle, error)
	InsertNewsArticle(a store.NewsArticle) (store.NewsArticle, error)
}

// backend implements nntpd.Backend. A backend with user=="" is the
// anonymous (pre-auth) backend; AUTHINFO returns an authenticated copy.
type backend struct {
	st   NewsStore
	host string
	user string // BBS member name; "" until authenticated
}

func (b *backend) authed() bool { return b.user != "" }

// Authorized reports whether this session may proceed without AUTHINFO.
func (b *backend) Authorized() bool { return b.authed() }

// Authenticate approves a login iff the supplied user is an existing,
// non-banned BBS member. The password is ignored (membership is the credential).
func (b *backend) Authenticate(user, _ string) (nntpd.Backend, error) {
	name := strings.ToLower(strings.TrimSpace(user))
	if name == "" {
		return nil, nntpd.ErrAuthRejected
	}
	u, ok, err := b.st.UserByName(name)
	if err != nil {
		return nil, nntpd.ErrAuthRejected
	}
	if !ok || u.Banned {
		return nil, nntpd.ErrAuthRejected
	}
	return &backend{st: b.st, host: b.host, user: u.Name}, nil
}

// AllowPost reports whether POST is accepted: members only.
func (b *backend) AllowPost() bool { return b.authed() }

func (b *backend) ListGroups(int) ([]*nntp.Group, error) {
	if !b.authed() {
		return nil, nntpd.ErrAuthRequired
	}
	gs, err := b.st.NewsGroups()
	if err != nil {
		return nil, err
	}
	out := make([]*nntp.Group, 0, len(gs))
	for _, g := range gs {
		out = append(out, toNNTPGroup(g))
	}
	return out, nil
}

func (b *backend) GetGroup(name string) (*nntp.Group, error) {
	if !b.authed() {
		return nil, nntpd.ErrAuthRequired
	}
	g, ok, err := b.st.NewsGroup(name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nntpd.ErrNoSuchGroup
	}
	return toNNTPGroup(g), nil
}

func (b *backend) GetArticle(group *nntp.Group, id string) (*nntp.Article, error) {
	if !b.authed() {
		return nil, nntpd.ErrAuthRequired
	}
	if strings.HasPrefix(id, "<") {
		a, ok, err := b.st.NewsArticleByMsgID(id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nntpd.ErrInvalidMessageID
		}
		return b.toNNTPArticle(a), nil
	}
	if group == nil {
		return nil, nntpd.ErrNoGroupSelected
	}
	num, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, nntpd.ErrInvalidArticleNumber
	}
	a, ok, err := b.st.NewsArticleByNum(group.Name, num)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nntpd.ErrInvalidArticleNumber
	}
	return b.toNNTPArticle(a), nil
}

func (b *backend) GetArticles(group *nntp.Group, from, to int64) ([]nntpd.NumberedArticle, error) {
	if !b.authed() {
		return nil, nntpd.ErrAuthRequired
	}
	if group == nil {
		return nil, nntpd.ErrNoGroupSelected
	}
	rows, err := b.st.NewsArticlesRange(group.Name, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]nntpd.NumberedArticle, 0, len(rows))
	for _, a := range rows {
		out = append(out, nntpd.NumberedArticle{Num: a.Num, Article: b.toNNTPArticle(a)})
	}
	return out, nil
}

// Post stores a member's article into every existing target group named in its
// Newsgroups header. The From header is stamped to the authenticated member so
// posts cannot be forged; client-supplied From/Path are ignored.
func (b *backend) Post(article *nntp.Article) error {
	if !b.authed() {
		return nntpd.ErrPostingNotPermitted
	}
	body, err := io.ReadAll(article.Body)
	if err != nil {
		return nntpd.ErrPostingFailed
	}
	groups := splitGroups(article.Header.Get("Newsgroups"))
	if len(groups) == 0 {
		return nntpd.ErrPostingFailed
	}

	from := fmt.Sprintf("%s <%s@%s>", b.user, b.user, b.host)
	date := strings.TrimSpace(article.Header.Get("Date"))
	if date == "" {
		date = time.Now().UTC().Format(time.RFC1123Z)
	}
	msgID := strings.TrimSpace(article.Header.Get("Message-Id"))
	if msgID == "" {
		msgID = b.newMessageID()
	}
	bodyStr := string(body)

	posted := 0
	for _, g := range groups {
		grp, ok, err := b.st.NewsGroup(g)
		if err != nil {
			return nntpd.ErrPostingFailed
		}
		if !ok || !grp.Posting {
			continue // skip unknown / read-only groups (cross-post tolerant)
		}
		a := store.NewsArticle{
			Group:   g,
			MsgID:   msgID,
			Subject: strings.TrimSpace(article.Header.Get("Subject")),
			From:    from,
			Refs:    strings.TrimSpace(article.Header.Get("References")),
			Date:    date,
			Body:    bodyStr,
			Lines:   strings.Count(bodyStr, "\n"),
			Bytes:   len(bodyStr),
		}
		if _, err := b.st.InsertNewsArticle(a); err != nil {
			return nntpd.ErrPostingFailed
		}
		posted++
	}
	if posted == 0 {
		return nntpd.ErrNotWanted
	}
	return nil
}

func (b *backend) newMessageID() string {
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), hex.EncodeToString(rnd[:]), b.host)
}

func toNNTPGroup(g store.NewsGroup) *nntp.Group {
	posting := nntp.PostingPermitted
	if !g.Posting {
		posting = nntp.PostingNotPermitted
	}
	return &nntp.Group{
		Name:        g.Name,
		Description: g.Description,
		Count:       g.Count,
		Low:         g.Low,
		High:        g.High,
		Posting:     posting,
	}
}

// toNNTPArticle reconstructs the wire article (headers + body) from a stored row.
func (b *backend) toNNTPArticle(a store.NewsArticle) *nntp.Article {
	h := textproto.MIMEHeader{}
	h.Set("Subject", a.Subject)
	h.Set("From", a.From)
	h.Set("Date", a.Date)
	h.Set("Newsgroups", a.Group)
	h.Set("Message-Id", a.MsgID)
	if a.Refs != "" {
		h.Set("References", a.Refs)
	}
	h.Set("Path", b.host)
	return &nntp.Article{
		Header: h,
		Body:   strings.NewReader(a.Body),
		Bytes:  a.Bytes,
		Lines:  a.Lines,
	}
}

// splitGroups parses a Newsgroups header ("a.b, c.d") into trimmed names.
func splitGroups(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if g := strings.TrimSpace(p); g != "" {
			out = append(out, g)
		}
	}
	return out
}
