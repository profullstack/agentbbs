package gopher

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/profullstack/agentbbs/internal/store"
)

// Content is the read-only slice of the store the gopher engine needs. It is
// satisfied directly by store.Store, so no new store methods are required; tests
// supply a small fake.
type Content interface {
	ListUsers(limit int) ([]store.User, error)
	NewsGroups() ([]store.NewsGroup, error)
	NewsArticlesRange(group string, from, to int64) ([]store.NewsArticle, error)
	NewsArticleByNum(group string, num int64) (store.NewsArticle, bool, error)
}

// Server resolves gopher selectors into responses over BBS content. It is shared
// by the public listener (Serve) and the hedgehog SSH browser (RunBrowser); the
// authed flag on Resolve is the only behavioural difference between them.
type Server struct {
	c         Content
	dataDir   string          // <data>; homepages live at <data>/users/<name>/public_html
	host      string          // hostname advertised in menu selector rows
	port      string          // port advertised in menu selector rows (e.g. "70")
	pubGroups map[string]bool // newsgroups exposed on the public (unauthenticated) surface
	aboutFn   func() string   // body of the /about text page (brand + MOTD + connect help)
}

// New builds a Server. host/port are echoed into menu rows so gopher clients can
// follow links. pubGroups is the allowlist of newsgroups visible without auth
// (the authenticated hedgehog surface sees them all). aboutFn supplies the
// /about page body and may be nil.
func New(c Content, dataDir, host, port string, pubGroups []string, aboutFn func() string) *Server {
	pg := make(map[string]bool, len(pubGroups))
	for _, g := range pubGroups {
		if g = strings.TrimSpace(g); g != "" {
			pg[g] = true
		}
	}
	if port == "" {
		port = "70"
	}
	return &Server{c: c, dataDir: dataDir, host: host, port: port, pubGroups: pg, aboutFn: aboutFn}
}

// Kind classifies a resolved response.
type Kind int

const (
	KindMenu   Kind = iota // a gopher directory (Menu set)
	KindText               // a text document (Text set)
	KindBinary             // a binary file (Data set)
	KindError              // selector unknown/refused (Text set to the message)
)

// Response is the result of resolving a selector. Wire() renders it to the exact
// bytes the native listener sends; the TUI reads Kind/Menu/Text directly.
type Response struct {
	Kind Kind
	Menu *Menu  // KindMenu
	Text string // KindText, KindError
	Data []byte // KindBinary
}

// Wire renders the response to the gopher wire format: menus and text end with
// the RFC 1436 "." terminator (text is dot-stuffed); binaries are sent raw.
func (r Response) Wire() []byte {
	switch r.Kind {
	case KindMenu:
		return []byte(r.Menu.Render())
	case KindText:
		return []byte(dotStuff(r.Text) + ".\r\n")
	case KindBinary:
		return r.Data
	default: // KindError
		return []byte(renderItem(Item{Type: TypeError, Display: r.Text, Host: "error.host", Port: "1"}) + ".\r\n")
	}
}

// Resolve maps a gopher selector to a response. authed is true only on the
// hedgehog (SSH-authenticated) surface, where member is the connecting member's
// name; the public listener always passes authed=false. The engine never writes.
func (s *Server) Resolve(selector string, authed bool, member string) Response {
	sel := strings.Trim(selector, " \t\r\n")
	sel = strings.TrimPrefix(sel, "/")
	if sel == "" {
		return s.root(authed, member)
	}
	head, rest := split1(sel)
	switch head {
	case "about":
		return Response{Kind: KindText, Text: s.about()}
	case "members":
		return s.members()
	case "news":
		return s.news(rest, authed)
	case "files":
		if rest == "" {
			return s.filesRoot()
		}
		return s.userTree("files", rest, "public")
	default:
		// "~name[/sub]" — a member homepage under public_html.
		if strings.HasPrefix(head, "~") {
			return s.userTree("", sel, "public_html")
		}
		return errResp("selector not found: " + selector)
	}
}

// root is the top-level menu. It is identical on both surfaces except the
// authenticated banner and the fact that /news lists private groups too.
func (s *Server) root(authed bool, member string) Response {
	m := NewMenu(s.host, s.port)
	m.Info("AgentBBS over Gopher")
	m.Info("A terminal BBS for humans & AI agents.")
	if authed {
		m.Info("Signed in as " + member + " (hedgehog: private groups visible).")
	} else {
		m.Info("Public view. For members-only content: ssh gopher@" + s.host)
	}
	m.Info("")
	m.Link(TypeText, "About AgentBBS", "/about")
	m.Link(TypeMenu, "Members  — homepages & directory", "/members")
	m.Link(TypeMenu, "News     — public newsgroups", "/news")
	m.Link(TypeMenu, "Files    — members' public files", "/files")
	return Response{Kind: KindMenu, Menu: m}
}

// about returns the /about text body (aboutFn, or a minimal fallback).
func (s *Server) about() string {
	if s.aboutFn != nil {
		if t := strings.TrimRight(s.aboutFn(), "\n"); t != "" {
			return t + "\n"
		}
	}
	return "AgentBBS — a terminal BBS for humans & AI agents.\n" +
		"Connect: ssh " + trimBBS(s.host) + "  ·  gopher://" + s.host + "\n"
}

// members lists every non-banned account, each linking to its homepage tree.
func (s *Server) members() Response {
	users, err := s.c.ListUsers(1000)
	if err != nil {
		return errResp("members unavailable")
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	m := NewMenu(s.host, s.port)
	m.Info("Member homepages — the same pages served at https://" + s.host + "/~name")
	m.Info("")
	for _, u := range users {
		if u.Banned || u.Name == "" {
			continue
		}
		m.Link(TypeMenu, u.Name, "/~"+u.Name)
	}
	if m.Len() <= 2 {
		m.Info("(no members yet)")
	}
	return Response{Kind: KindMenu, Menu: m}
}

// news serves the newsgroup tree. rest is "" (group list), "<group>" (article
// list), or "<group>/<num>" (one article). On the public surface only groups in
// pubGroups are reachable; hedgehog (authed) reaches every group.
func (s *Server) news(rest string, authed bool) Response {
	if rest == "" {
		groups, err := s.c.NewsGroups()
		if err != nil {
			return errResp("news unavailable")
		}
		m := NewMenu(s.host, s.port)
		if authed {
			m.Info("Newsgroups (all groups — you are authenticated)")
		} else {
			m.Info("Public newsgroups. The rest are members-only: ssh gopher@" + s.host)
		}
		m.Info("")
		for _, g := range groups {
			if !authed && !s.pubGroups[g.Name] {
				continue
			}
			label := g.Name
			if g.Description != "" {
				label += "  — " + g.Description
			}
			m.Link(TypeMenu, label, "/news/"+g.Name)
		}
		if m.Len() <= 2 {
			m.Info("(no public groups)")
		}
		return Response{Kind: KindMenu, Menu: m}
	}

	group, tail := split1(rest)
	if !authed && !s.pubGroups[group] {
		return errResp("group is members-only: ssh gopher@" + s.host)
	}
	if tail == "" {
		return s.newsArticles(group)
	}
	num, err := strconv.ParseInt(tail, 10, 64)
	if err != nil {
		return errResp("bad article number")
	}
	a, ok, err := s.c.NewsArticleByNum(group, num)
	if err != nil || !ok {
		return errResp("article not found")
	}
	var b strings.Builder
	b.WriteString("From:    " + a.From + "\n")
	b.WriteString("Subject: " + a.Subject + "\n")
	b.WriteString("Date:    " + a.Date + "\n")
	b.WriteString("Group:   " + group + "  #" + tail + "\n")
	b.WriteString(strings.Repeat("-", 64) + "\n\n")
	b.WriteString(a.Body)
	return Response{Kind: KindText, Text: b.String()}
}

// newsArticles lists the articles in a group, newest first.
func (s *Server) newsArticles(group string) Response {
	gs, err := s.c.NewsGroups()
	if err != nil {
		return errResp("news unavailable")
	}
	var high int64
	found := false
	for _, g := range gs {
		if g.Name == group {
			high, found = g.High, true
			break
		}
	}
	if !found {
		return errResp("no such group")
	}
	arts, err := s.c.NewsArticlesRange(group, 1, high)
	if err != nil {
		return errResp("group unavailable")
	}
	sort.Slice(arts, func(i, j int) bool { return arts[i].Num > arts[j].Num })
	m := NewMenu(s.host, s.port)
	m.Info(group)
	m.Info("")
	for _, a := range arts {
		label := "#" + strconv.FormatInt(a.Num, 10) + "  " + a.Subject
		if a.From != "" {
			label += "  (" + a.From + ")"
		}
		m.Link(TypeText, label, "/news/"+group+"/"+strconv.FormatInt(a.Num, 10))
	}
	if m.Len() <= 2 {
		m.Info("(no articles yet)")
	}
	return Response{Kind: KindMenu, Menu: m}
}

// filesRoot lists every non-banned member, each linking to their public files
// area (<data>/users/<name>/public), mirroring the anonymous web files surface.
func (s *Server) filesRoot() Response {
	users, err := s.c.ListUsers(1000)
	if err != nil {
		return errResp("files unavailable")
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	m := NewMenu(s.host, s.port)
	m.Info("Members' public files")
	m.Info("")
	for _, u := range users {
		if u.Banned || u.Name == "" {
			continue
		}
		m.Link(TypeMenu, u.Name, "/files/~"+u.Name)
	}
	if m.Len() <= 2 {
		m.Info("(no members yet)")
	}
	return Response{Kind: KindMenu, Menu: m}
}

// userTree serves a member's per-user area (public_html homepage, or the public
// files area) as gopher content. prefix is the selector root ("" for homepages,
// "files" for the files area); sel is the remainder after the prefix, of the form
// "~name[/subpath]". area is the on-disk subdirectory under <data>/users/<name>.
//
// The subpath is confined to the member's area: it is cleaned as an absolute
// path (so any ".." that would escape is neutralised) and the resolved target is
// re-checked to live under the base directory before any file is opened.
func (s *Server) userTree(prefix, sel, area string) Response {
	// sel is "~name[/subpath]" for both surfaces (Resolve passes the slice after
	// any "/files" head), so the first segment is always the member.
	tilde, sub := split1(sel)
	name := strings.TrimPrefix(tilde, "~")
	if !validName(name) {
		return errResp("bad member name")
	}
	base := filepath.Join(s.dataDir, "users", name, area)

	// Confine sub to base. Cleaning as an absolute path drops any leading ".."
	// components; the HasPrefix re-check is belt-and-suspenders against symlink
	// or edge cases.
	clean := filepath.Clean("/" + sub)
	target := filepath.Join(base, clean)
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return errResp("forbidden")
	}
	// Symlink escape guard: resolve symlinks and re-check the real path stays
	// within the member's area. A symlink created inside public_html that
	// points outside would pass the lexical check but leak external content.
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		if resolved != base && !strings.HasPrefix(resolved, base+string(os.PathSeparator)) {
			return errResp("forbidden")
		}
	}

	info, err := os.Stat(target)
	if err != nil {
		return errResp("not found")
	}
	linkBase := "/~" + name
	if prefix == "files" {
		linkBase = "/files/~" + name
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(target, base), string(os.PathSeparator))

	if info.IsDir() {
		return s.dirMenu(target, linkBase, rel, name)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return errResp("unreadable")
	}
	if isText(target) {
		return Response{Kind: KindText, Text: string(data)}
	}
	return Response{Kind: KindBinary, Data: data}
}

// dirMenu renders a directory listing. linkBase is the selector prefix for this
// member's area ("/~name" or "/files/~name"); rel is the directory's path within
// the area ("" at the area root).
func (s *Server) dirMenu(dir, linkBase, rel, name string) Response {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errResp("unreadable")
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir() // directories first
		}
		return entries[i].Name() < entries[j].Name()
	})
	m := NewMenu(s.host, s.port)
	title := name
	if rel != "" {
		title += "/" + rel
	}
	m.Info(title)
	m.Info("")
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // hidden files
		}
		childSel := linkBase
		if rel != "" {
			childSel += "/" + rel
		}
		childSel += "/" + e.Name()
		if e.IsDir() {
			m.Link(TypeMenu, e.Name()+"/", childSel)
			continue
		}
		t := byte(TypeBinary)
		if isText(e.Name()) {
			t = TypeText
		} else if isImage(e.Name()) {
			t = TypeImage
		}
		m.Link(t, e.Name(), childSel)
	}
	if m.Len() <= 2 {
		m.Info("(empty)")
	}
	return Response{Kind: KindMenu, Menu: m}
}

// --- helpers ---

// split1 splits "a/b/c" into ("a", "b/c"); a bare "a" yields ("a", "").
func split1(p string) (head, rest string) {
	p = strings.TrimPrefix(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

// validName accepts the member-name charset (see auth.SanitizeUsername) and
// nothing that could traverse or escape a path.
func validName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func errResp(msg string) Response { return Response{Kind: KindError, Text: msg} }

// trimBBS drops a leading "bbs." so connect hints read "profullstack.com".
func trimBBS(host string) string { return strings.TrimPrefix(host, "bbs.") }

// isText reports whether a filename should be served as a gopher text document.
func isText(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".markdown", ".html", ".htm", ".css", ".js", ".json",
		".xml", ".gmi", ".gph", ".log", ".csv", ".org", ".rst", "":
		return true
	}
	return false
}

// isImage reports whether a filename is a common image (gopher type 'I').
func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico", ".svg":
		return true
	}
	return false
}

// dotStuff prepares a text body for the RFC 1436 "." terminator: it forces CRLF
// line endings and escapes any line consisting solely of "." to ".." so it is
// not mistaken for the end-of-response marker.
func dotStuff(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		if ln == "." {
			lines[i] = ".."
		}
	}
	out := strings.Join(lines, "\r\n")
	if !strings.HasSuffix(out, "\r\n") {
		out += "\r\n"
	}
	return out
}
