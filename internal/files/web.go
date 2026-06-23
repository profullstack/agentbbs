package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/profullstack/agentbbs/internal/store"
)

// WebConfig configures the browser-facing file manager served at
// files.<host>. Authenticate validates a member's webmail credentials.
type WebConfig struct {
	// Authenticate returns the member and true when user+pass are valid. user
	// may be a bare handle ("alice") or a full address ("alice@host").
	Authenticate func(user, pass string) (store.User, bool, error)
	// Title is shown in the page header (e.g. "files.profullstack.com").
	Title string
	// SessionTTL defaults to 12h.
	SessionTTL time.Duration
}

// webSrv holds the live login sessions for the web file manager.
type webSrv struct {
	svc  *Service
	cfg  WebConfig
	mu   sync.Mutex
	sess map[string]webSession // cookie token -> session
}

type webSession struct {
	name string
	exp  time.Time
}

const webCookie = "fsess"

// WebHandler returns the HTTP handler for the web file browser. Members log in
// with their webmail username + password and browse the same /me and /public
// areas as SFTP — no SSH key required, and no home directory is ever exposed.
func (s *Service) WebHandler(cfg WebConfig) http.Handler {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	if cfg.Title == "" {
		cfg.Title = "AgentBBS Files"
	}
	h := &webSrv{svc: s, cfg: cfg, sess: map[string]webSession{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleRoot)
	mux.HandleFunc("/login", h.handleLogin)
	mux.HandleFunc("/logout", h.handleLogout)
	mux.HandleFunc("/download", h.handleDownload)
	mux.HandleFunc("/upload", h.handleUpload)
	mux.HandleFunc("/mkdir", h.handleMkdir)
	mux.HandleFunc("/delete", h.handleDelete)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

// --- session helpers --------------------------------------------------------

func (h *webSrv) lookup(r *http.Request) (string, bool) {
	c, err := r.Cookie(webCookie)
	if err != nil {
		return "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sess[c.Value]
	if !ok {
		return "", false
	}
	if time.Now().After(s.exp) {
		delete(h.sess, c.Value)
		return "", false
	}
	return s.name, true
}

func (h *webSrv) set(w http.ResponseWriter, r *http.Request, name string) {
	tok := randHex(24)
	h.mu.Lock()
	h.sess[tok] = webSession{name: name, exp: time.Now().Add(h.cfg.SessionTTL)}
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: webCookie, Value: tok, Path: "/", HttpOnly: true,
		Secure: secureReq(r), SameSite: http.SameSiteLaxMode, MaxAge: int(h.cfg.SessionTTL.Seconds()),
	})
}

func (h *webSrv) clear(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(webCookie); err == nil {
		h.mu.Lock()
		delete(h.sess, c.Value)
		h.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: webCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

func secureReq(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- handlers ---------------------------------------------------------------

func (h *webSrv) handleRoot(w http.ResponseWriter, r *http.Request) {
	name, ok := h.lookup(r)
	if !ok {
		h.renderLogin(w, "")
		return
	}
	vpath := cleanVPath(r.URL.Query().Get("path"))
	sess, u, err := h.svc.OpenFor(name)
	if err != nil {
		h.clear(w, r)
		h.renderLogin(w, "Your account could not be opened — sign in again.")
		return
	}
	ents, err := sess.entries(vpath)
	if err != nil {
		// Bad path → fall back to the private workspace root.
		vpath = "/me"
		ents, err = sess.entries(vpath)
		if err != nil {
			http.Error(w, "cannot list files", http.StatusInternalServerError)
			return
		}
	}
	usage, _ := h.svc.Usage(u)
	data := listData{
		Title:    h.cfg.Title,
		User:     name,
		Path:     vpath,
		Crumbs:   crumbs(vpath),
		Writable: sess.canWrite(vpath) && vpath != "/",
		UsedH:    humanSize(usage.Bytes),
		QuotaH:   humanSize(usage.Quota),
		Err:      r.URL.Query().Get("err"),
		Msg:      r.URL.Query().Get("msg"),
	}
	if p := parentOf(vpath); p != vpath {
		data.Parent, data.ParentOK = p, true
	}
	for _, e := range ents {
		data.Entries = append(data.Entries, webEntry{
			Name: e.Name, IsDir: e.IsDir, SizeH: humanSize(e.Size),
			Path: path.Join(vpath, e.Name),
			Mod:  e.ModTime.Format("2006-01-02 15:04"),
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = listTmpl.Execute(w, data)
}

func (h *webSrv) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	user := strings.TrimSpace(r.FormValue("user"))
	pass := r.FormValue("pass")
	if user == "" || pass == "" {
		h.renderLogin(w, "Enter your username and webmail password.")
		return
	}
	u, ok, err := h.cfg.Authenticate(user, pass)
	if err != nil || !ok {
		h.renderLogin(w, "Invalid username or password.")
		return
	}
	h.set(w, r, u.Name)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *webSrv) handleLogout(w http.ResponseWriter, r *http.Request) {
	h.clear(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *webSrv) handleDownload(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.session(w, r)
	if !ok {
		return
	}
	vpath := cleanVPath(r.URL.Query().Get("path"))
	f, fi, err := sess.webOpen(vpath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Disposition", "attachment; filename=\""+path.Base(vpath)+"\"")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	_, _ = io.Copy(w, f)
}

func (h *webSrv) handleUpload(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.session(w, r)
	if !ok {
		return
	}
	dir := cleanVPath(r.FormValue("dir"))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		h.redirect(w, r, dir, "upload failed")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		h.redirect(w, r, dir, "no file chosen")
		return
	}
	defer file.Close()
	fname := path.Base(strings.TrimSpace(hdr.Filename))
	if fname == "" || fname == "." || fname == "/" {
		h.redirect(w, r, dir, "bad filename")
		return
	}
	dest := path.Join(dir, fname)
	if _, err := sess.webSave(dest, file); err != nil {
		if errors.Is(err, errQuota) {
			h.redirect(w, r, dir, "over quota — upload too large")
			return
		}
		if errors.Is(err, os.ErrPermission) {
			h.redirect(w, r, dir, "this area is read-only")
			return
		}
		h.redirect(w, r, dir, "upload failed")
		return
	}
	h.redirectMsg(w, r, dir, "uploaded "+fname)
}

func (h *webSrv) handleMkdir(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.session(w, r)
	if !ok {
		return
	}
	dir := cleanVPath(r.FormValue("dir"))
	name := path.Base(strings.TrimSpace(r.FormValue("name")))
	if name == "" || name == "." || name == "/" {
		h.redirect(w, r, dir, "bad folder name")
		return
	}
	if err := sess.webMkdir(path.Join(dir, name)); err != nil {
		h.redirect(w, r, dir, "could not create folder")
		return
	}
	h.redirectMsg(w, r, dir, "created "+name)
}

func (h *webSrv) handleDelete(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.session(w, r)
	if !ok {
		return
	}
	target := cleanVPath(r.FormValue("path"))
	parent := parentOf(target)
	if err := sess.webRemove(target); err != nil {
		h.redirect(w, r, parent, "could not delete")
		return
	}
	h.redirectMsg(w, r, parent, "deleted "+path.Base(target))
}

// session resolves the logged-in member into a filesystem session, writing an
// auth error to w when there is none.
func (h *webSrv) session(w http.ResponseWriter, r *http.Request) (*session, bool) {
	name, ok := h.lookup(r)
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return nil, false
	}
	sess, _, err := h.svc.OpenFor(name)
	if err != nil {
		http.Error(w, "account error", http.StatusInternalServerError)
		return nil, false
	}
	return sess, true
}

func (h *webSrv) redirect(w http.ResponseWriter, r *http.Request, dir, errMsg string) {
	u := "/?path=" + urlEsc(dir)
	if errMsg != "" {
		u += "&err=" + urlEsc(errMsg)
	}
	http.Redirect(w, r, u, http.StatusSeeOther)
}

func (h *webSrv) redirectMsg(w http.ResponseWriter, r *http.Request, dir, msg string) {
	http.Redirect(w, r, "/?path="+urlEsc(dir)+"&msg="+urlEsc(msg), http.StatusSeeOther)
}

func (h *webSrv) renderLogin(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginTmpl.Execute(w, loginData{Title: h.cfg.Title, Err: errMsg})
}

// --- view models + templates ------------------------------------------------

type loginData struct {
	Title string
	Err   string
}

type crumb struct {
	Name string
	Path string
}

type webEntry struct {
	Name  string
	IsDir bool
	SizeH string
	Path  string
	Mod   string
}

type listData struct {
	Title    string
	User     string
	Path     string
	Crumbs   []crumb
	Parent   string
	ParentOK bool
	Entries  []webEntry
	Writable bool
	UsedH    string
	QuotaH   string
	Err      string
	Msg      string
}

var baseCSS = `
*{box-sizing:border-box}body{margin:0;background:#0f172a;color:#e2e8f0;font:15px/1.5 system-ui,-apple-system,Segoe UI,sans-serif}
a{color:#4ade80;text-decoration:none}a:hover{text-decoration:underline}
.wrap{max-width:860px;margin:0 auto;padding:24px}
.bar{display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid #1e293b;padding-bottom:12px;margin-bottom:16px}
.bar h1{font-size:18px;margin:0;color:#4ade80}
.muted{color:#94a3b8;font-size:13px}
table{width:100%;border-collapse:collapse}
td,th{text-align:left;padding:8px 6px;border-bottom:1px solid #1e293b}
th{color:#94a3b8;font-weight:600;font-size:12px;text-transform:uppercase;letter-spacing:.04em}
.right{text-align:right}
.btn{background:#16a34a;color:#fff;border:0;padding:8px 14px;border-radius:6px;cursor:pointer;font-size:14px}
.btn.sm{padding:4px 9px;font-size:13px}
.btn.danger{background:#b91c1c}
input[type=text],input[type=password],input[type=file]{background:#1e293b;border:1px solid #334155;color:#e2e8f0;padding:8px;border-radius:6px;font-size:14px}
form.inline{display:inline}
.tools{display:flex;gap:18px;flex-wrap:wrap;align-items:center;margin:18px 0;padding:14px;background:#111c33;border:1px solid #1e293b;border-radius:8px}
.tools form{display:flex;gap:8px;align-items:center}
.flash{padding:10px 12px;border-radius:6px;margin-bottom:14px}
.flash.err{background:#3f1d1d;color:#fecaca}
.flash.ok{background:#14321f;color:#bbf7d0}
.card{max-width:380px;margin:8vh auto;padding:28px;background:#111c33;border:1px solid #1e293b;border-radius:10px}
.card h1{color:#4ade80;margin:0 0 4px}.card label{display:block;margin:14px 0 4px;font-size:13px;color:#94a3b8}
.card input{width:100%}
.crumbs a{color:#94a3b8}.crumbs b{color:#e2e8f0}
`

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html><html><head><meta charset=utf-8>
<meta name=viewport content="width=device-width,initial-scale=1"><title>{{.Title}}</title><style>` + baseCSS + `</style></head>
<body><div class=card>
<h1>{{.Title}}</h1>
<p class=muted>Sign in with your AgentBBS username and your <b>webmail</b> password.</p>
{{if .Err}}<div class="flash err">{{.Err}}</div>{{end}}
<form method=post action=/login>
<label>Username</label><input type=text name=user autofocus autocomplete=username placeholder="e.g. chovy">
<label>Webmail password</label><input type=password name=pass autocomplete=current-password>
<div style="margin-top:18px"><button class=btn type=submit>Sign in</button></div>
</form>
<p class=muted style="margin-top:18px">Forgot it? Re-run <code>ssh join@</code> to reset your webmail password. Not a member? <code>ssh join@bbs.profullstack.com</code></p>
</div></body></html>`))

var listTmpl = template.Must(template.New("list").Parse(`<!doctype html><html><head><meta charset=utf-8>
<meta name=viewport content="width=device-width,initial-scale=1"><title>{{.Title}}</title><style>` + baseCSS + `</style></head>
<body><div class=wrap>
<div class=bar><h1>{{.Title}}</h1>
<div class=muted>{{.User}} · {{.UsedH}} / {{.QuotaH}} used ·
<form class=inline method=post action=/logout><button class="btn sm" type=submit>Sign out</button></form></div></div>
{{if .Err}}<div class="flash err">{{.Err}}</div>{{end}}
{{if .Msg}}<div class="flash ok">{{.Msg}}</div>{{end}}
<div class=crumbs><a href="/?path=%2F">/</a>{{range .Crumbs}} <a href="/?path={{.Path}}">{{.Name}}</a> /{{end}}</div>
{{if .Writable}}<div class=tools>
<form method=post action=/upload enctype=multipart/form-data>
<input type=hidden name=dir value="{{.Path}}"><input type=file name=file required><button class=btn type=submit>Upload</button></form>
<form method=post action=/mkdir>
<input type=hidden name=dir value="{{.Path}}"><input type=text name=name placeholder="new folder" required><button class=btn type=submit>Create folder</button></form>
</div>{{else}}<p class=muted>This area is read-only.</p>{{end}}
<table><tr><th>Name</th><th class=right>Size</th><th>Modified</th><th></th></tr>
{{if .ParentOK}}<tr><td><a href="/?path={{.Parent}}">⬑ ..</a></td><td></td><td></td><td></td></tr>{{end}}
{{range .Entries}}<tr>
<td>{{if .IsDir}}📁 <a href="/?path={{.Path}}">{{.Name}}/</a>{{else}}📄 <a href="/download?path={{.Path}}">{{.Name}}</a>{{end}}</td>
<td class=right>{{if not .IsDir}}{{.SizeH}}{{end}}</td><td class=muted>{{.Mod}}</td>
<td class=right>{{if $.Writable}}<form class=inline method=post action=/delete onsubmit="return confirm('Delete {{.Name}}?')">
<input type=hidden name=path value="{{.Path}}"><button class="btn sm danger" type=submit>Delete</button></form>{{end}}</td>
</tr>{{end}}
{{if not .Entries}}<tr><td colspan=4 class=muted>(empty)</td></tr>{{end}}
</table>
<p class=muted style="margin-top:20px">Also reachable over SFTP: <code>sftp files@files.profullstack.com</code> (with your SSH key).</p>
</div></body></html>`))

// --- path helpers -----------------------------------------------------------

// cleanVPath normalizes a virtual path to an absolute, lexically clean form.
func cleanVPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/me"
	}
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
}

func parentOf(p string) string {
	p = cleanVPath(p)
	if p == "/" {
		return "/"
	}
	return cleanVPath(path.Dir(p))
}

func crumbs(p string) []crumb {
	p = cleanVPath(p)
	if p == "/" {
		return nil
	}
	var out []crumb
	acc := ""
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		acc += "/" + seg
		// Raw clean path: html/template URL-encodes it in the href query context.
		out = append(out, crumb{Name: seg, Path: acc})
	}
	return out
}

func urlEsc(p string) string {
	// path is already clean; escape just the characters that would break a query.
	r := strings.NewReplacer("%", "%25", "&", "%26", "?", "%3F", "#", "%23", " ", "%20", "+", "%2B")
	return r.Replace(p)
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
