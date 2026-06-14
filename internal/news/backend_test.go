package news

import (
	"errors"
	"io"
	"net/textproto"
	"strings"
	"testing"

	"github.com/dustin/go-nntp"
	"github.com/profullstack/agentbbs/internal/news/nntpd"
	"github.com/profullstack/agentbbs/internal/store"
)

// fakeStore is an in-memory NewsStore for backend tests.
type fakeStore struct {
	users    map[string]store.User
	groups   map[string]store.NewsGroup
	articles []store.NewsArticle
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users: map[string]store.User{
			"alice": {ID: 1, Name: "alice"},
			"bob":   {ID: 2, Name: "bob", Banned: true},
		},
		groups: map[string]store.NewsGroup{
			"pfs.general": {Name: "pfs.general", Posting: true},
			"pfs.locked":  {Name: "pfs.locked", Posting: false},
		},
	}
}

func (f *fakeStore) UserByName(name string) (store.User, bool, error) {
	u, ok := f.users[name]
	return u, ok, nil
}
func (f *fakeStore) EnsureNewsGroup(name, desc string) error {
	if _, ok := f.groups[name]; !ok {
		f.groups[name] = store.NewsGroup{Name: name, Description: desc, Posting: true}
	}
	return nil
}
func (f *fakeStore) NewsGroups() ([]store.NewsGroup, error) {
	var out []store.NewsGroup
	for _, g := range f.groups {
		out = append(out, g)
	}
	return out, nil
}
func (f *fakeStore) NewsGroup(name string) (store.NewsGroup, bool, error) {
	g, ok := f.groups[name]
	return g, ok, nil
}
func (f *fakeStore) NewsArticleByNum(group string, num int64) (store.NewsArticle, bool, error) {
	for _, a := range f.articles {
		if a.Group == group && a.Num == num {
			return a, true, nil
		}
	}
	return store.NewsArticle{}, false, nil
}
func (f *fakeStore) NewsArticleByMsgID(id string) (store.NewsArticle, bool, error) {
	for _, a := range f.articles {
		if a.MsgID == id {
			return a, true, nil
		}
	}
	return store.NewsArticle{}, false, nil
}
func (f *fakeStore) NewsArticlesRange(group string, from, to int64) ([]store.NewsArticle, error) {
	var out []store.NewsArticle
	for _, a := range f.articles {
		if a.Group == group && a.Num >= from && a.Num <= to {
			out = append(out, a)
		}
	}
	return out, nil
}
func (f *fakeStore) InsertNewsArticle(a store.NewsArticle) (store.NewsArticle, error) {
	var max int64
	for _, x := range f.articles {
		if x.Group == a.Group && x.Num > max {
			max = x.Num
		}
	}
	a.Num = max + 1
	f.articles = append(f.articles, a)
	return a, nil
}

func TestAnonymousBackendRefusesData(t *testing.T) {
	b := &backend{st: newFakeStore(), host: "news.h"}
	if b.Authorized() || b.AllowPost() {
		t.Fatal("anonymous backend must not be authorized or allowed to post")
	}
	if _, err := b.ListGroups(-1); !errors.Is(err, nntpd.ErrAuthRequired) {
		t.Fatalf("ListGroups should require auth, got %v", err)
	}
	if _, err := b.GetGroup("pfs.general"); !errors.Is(err, nntpd.ErrAuthRequired) {
		t.Fatalf("GetGroup should require auth, got %v", err)
	}
	if _, err := b.GetArticle(&nntp.Group{Name: "pfs.general"}, "1"); !errors.Is(err, nntpd.ErrAuthRequired) {
		t.Fatalf("GetArticle should require auth, got %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	b := &backend{st: newFakeStore(), host: "news.h"}

	if _, err := b.Authenticate("nobody", "x"); !errors.Is(err, nntpd.ErrAuthRejected) {
		t.Fatalf("unknown user must be rejected, got %v", err)
	}
	if _, err := b.Authenticate("bob", "x"); !errors.Is(err, nntpd.ErrAuthRejected) {
		t.Fatalf("banned user must be rejected, got %v", err)
	}
	nb, err := b.Authenticate("Alice", "anything") // case-insensitive, password ignored
	if err != nil {
		t.Fatalf("valid member rejected: %v", err)
	}
	ab, ok := nb.(*backend)
	if !ok || !ab.Authorized() || !ab.AllowPost() || ab.user != "alice" {
		t.Fatalf("authed backend wrong: %+v ok=%v", ab, ok)
	}
}

func TestPostStampsFromAndNumbers(t *testing.T) {
	fs := newFakeStore()
	ab := &backend{st: fs, host: "news.h", user: "alice"}

	mkArticle := func(groups, subject, body string) *nntp.Article {
		h := textproto.MIMEHeader{}
		h.Set("Newsgroups", groups)
		h.Set("Subject", subject)
		h.Set("From", "forged <evil@elsewhere>") // must be ignored
		return &nntp.Article{Header: h, Body: strings.NewReader(body)}
	}

	if err := ab.Post(mkArticle("pfs.general", "Hi", "hello\nworld\n")); err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(fs.articles) != 1 {
		t.Fatalf("expected 1 stored article, got %d", len(fs.articles))
	}
	got := fs.articles[0]
	if got.From != "alice <alice@news.h>" {
		t.Fatalf("From not stamped to member: %q", got.From)
	}
	if got.Num != 1 || got.Subject != "Hi" || got.MsgID == "" {
		t.Fatalf("stored article wrong: %+v", got)
	}

	// Posting only to a locked/unknown group yields ErrNotWanted.
	if err := ab.Post(mkArticle("pfs.locked, no.such.group", "x", "y")); !errors.Is(err, nntpd.ErrNotWanted) {
		t.Fatalf("post to locked/unknown should be not-wanted, got %v", err)
	}

	// A retrieved article reconstructs headers and body.
	art, err := ab.GetArticle(&nntp.Group{Name: "pfs.general"}, "1")
	if err != nil {
		t.Fatalf("get article: %v", err)
	}
	if art.Header.Get("From") != "alice <alice@news.h>" || art.Header.Get("Subject") != "Hi" {
		t.Fatalf("reconstructed headers wrong: %+v", art.Header)
	}
	body, _ := io.ReadAll(art.Body)
	if string(body) != "hello\nworld\n" {
		t.Fatalf("reconstructed body wrong: %q", body)
	}
}
