package files

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/profullstack/agentbbs/internal/store"
)

// webTestHandler builds the web handler with a stub authenticator that accepts
// alice/secret, plus a helper http client that carries the session cookie.
func webTestHandler(t *testing.T) (http.Handler, store.User) {
	t.Helper()
	svc, _, u := newTestService(t)
	h := svc.WebHandler(WebConfig{
		Title: "files.test",
		Authenticate: func(user, pass string) (store.User, bool, error) {
			if strings.HasPrefix(user, "alice") && pass == "secret" {
				return u, true, nil
			}
			return store.User{}, false, nil
		},
	})
	return h, u
}

func TestWebRequiresAuth(t *testing.T) {
	h, _ := webTestHandler(t)

	// Unauthenticated root shows the login form, not a file listing.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if body := rr.Body.String(); !strings.Contains(body, "Sign in") {
		t.Fatalf("expected login page, got: %.120s", body)
	}

	// API endpoints reject without a session.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/download?path=/me/x", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("download without auth: want 401, got %d", rr.Code)
	}

	// Bad credentials do not set a session cookie.
	rr = httptest.NewRecorder()
	form := url.Values{"user": {"alice"}, "pass": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rr, req)
	if len(rr.Result().Cookies()) != 0 {
		t.Fatal("bad login should not set a cookie")
	}
}

func TestWebRoundTrip(t *testing.T) {
	h, _ := webTestHandler(t)

	// Log in and capture the session cookie.
	form := url.Values{"user": {"alice@files.test"}, "pass": {"secret"}}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rr, req)
	cookies := rr.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a cookie")
	}
	cookie := cookies[0]
	auth := func(r *http.Request) { r.AddCookie(cookie) }

	// Upload a file into /me.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("dir", "/me")
	fw, _ := mw.CreateFormFile("file", "hello.txt")
	_, _ = fw.Write([]byte("hello world"))
	_ = mw.Close()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	auth(req)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("upload: want redirect, got %d (%s)", rr.Code, rr.Body.String())
	}

	// The listing now shows the file.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/?path=/me", nil)
	auth(req)
	h.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "hello.txt") {
		t.Fatalf("listing missing uploaded file: %.300s", rr.Body.String())
	}

	// Download returns the bytes.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/download?path=/me/hello.txt", nil)
	auth(req)
	h.ServeHTTP(rr, req)
	if got, _ := io.ReadAll(rr.Body); string(got) != "hello world" {
		t.Fatalf("download mismatch: %q", got)
	}

	// Delete removes it.
	rr = httptest.NewRecorder()
	form = url.Values{"path": {"/me/hello.txt"}}
	req = httptest.NewRequest(http.MethodPost, "/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	auth(req)
	h.ServeHTTP(rr, req)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/?path=/me", nil)
	auth(req)
	h.ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), "hello.txt") {
		t.Fatal("file still present after delete")
	}
}

// loginCookie logs alice in and returns her session cookie.
func loginCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	form := url.Values{"user": {"alice"}, "pass": {"secret"}}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rr, req)
	cs := rr.Result().Cookies()
	if len(cs) == 0 {
		t.Fatal("login did not set a cookie")
	}
	return cs[0]
}

// uploadTo uploads body to dir as the holder of cookie.
func uploadTo(t *testing.T, h http.Handler, cookie *http.Cookie, dir, name, body string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("dir", dir)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write([]byte(body))
	_ = mw.Close()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("upload to %s: want redirect, got %d (%s)", dir, rr.Code, rr.Body.String())
	}
}

func TestWebAnonPublicSite(t *testing.T) {
	h, _ := webTestHandler(t)
	cookie := loginCookie(t, h)

	// alice publishes to her own public area (/public, a sibling of private /me).
	uploadTo(t, h, cookie, "/public", "hello.txt", "from alice")

	// The unauthenticated root lists every member with a link to ~alice/public.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if body := rr.Body.String(); !strings.Contains(body, "/~alice/public/") {
		t.Fatalf("index missing ~alice/public link: %.300s", body)
	}

	// Anonymous (no cookie) can download a member's published file.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/~alice/public/hello.txt", nil))
	if got, _ := io.ReadAll(rr.Body); string(got) != "from alice" {
		t.Fatalf("~alice/public file: got %q", got)
	}

	// Bare ~alice redirects to ~alice/public/.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/~alice", nil))
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/~alice/public/" {
		t.Fatalf("~alice: want redirect to /~alice/public/, got %d %q", rr.Code, rr.Header().Get("Location"))
	}

	// Anonymous directory browse renders a listing.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/~alice/public/", nil))
	if body := rr.Body.String(); !strings.Contains(body, "hello.txt") {
		t.Fatalf("~alice/public browse missing file: %.300s", body)
	}

	// /me stays private: there is no anonymous route into it.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/~alice/me/", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("~alice/me: want 404 (private), got %d", rr.Code)
	}
}

func TestWebAnonMemberSiteEmptyNot404(t *testing.T) {
	// A registered member who has not published anything yet is reachable at
	// ~name/public as a browsable listing, not a 404 — materializing the area
	// seeds a default README.txt, so the listing greets visitors with it rather
	// than "(empty)". A missing file under them, and an unknown member, both
	// still 404.
	svc, st, _ := newTestService(t)
	if _, err := st.EnsureUser("bob", "member", "SHA256:bobkey"); err != nil {
		t.Fatal(err)
	}
	h := svc.WebHandler(WebConfig{Title: "files.test"})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/~bob/public/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("~bob/public (member, empty): want 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "README.txt") {
		t.Fatalf("~bob/public should render the seeded README in its listing: %.200s", rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/~bob/public/nope.txt", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("~bob/public/nope.txt: want 404, got %d", rr.Code)
	}

	// Anything under ~bob that is not /public is not exposed.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/~bob/secret", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("~bob/secret: want 404, got %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/~nobody/public/", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("~nobody (unknown): want 404, got %d", rr.Code)
	}
}

func TestWebAnonCannotEscape(t *testing.T) {
	h, _ := webTestHandler(t)
	cookie := loginCookie(t, h)
	uploadTo(t, h, cookie, "/me", "secret.txt", "private")

	// Only ~name/public is exposed; traversal out of a public area must not reach
	// the private /me or anything above it.
	for _, p := range []string{
		"/~alice/public/../../users/alice/secret.txt",
		"/~alice/public/../../../users/alice/secret.txt",
		"/~alice/public/..%2f..%2fusers%2falice%2fsecret.txt",
		"/~alice/me/secret.txt", // /me is private — not an anon surface
		"/~ghost/public/x",      // unknown member
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if body, _ := io.ReadAll(rr.Body); strings.Contains(string(body), "private") {
			t.Fatalf("anon path %q leaked private content", p)
		}
		if rr.Code == http.StatusOK && strings.Contains(rr.Body.String(), "secret.txt") {
			t.Fatalf("anon path %q exposed /me", p)
		}
	}

	// And the authed download API still rejects /me without a session.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/download?path=/me/secret.txt", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anon /me download: want 401, got %d", rr.Code)
	}
}

func TestWebPublicReadOnlyByDefault(t *testing.T) {
	h, _ := webTestHandler(t)
	// Log in.
	form := url.Values{"user": {"alice"}, "pass": {"secret"}}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rr, req)
	cookie := rr.Result().Cookies()[0]

	// Default public_write is "members" (writable), so /public should accept an
	// upload; this asserts the area resolves and writes land — the moderation
	// toggle is covered in the SFTP tests.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("dir", "/public")
	fw, _ := mw.CreateFormFile("file", "note.txt")
	_, _ = fw.Write([]byte("shared"))
	_ = mw.Close()
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("public upload: want redirect, got %d", rr.Code)
	}
}
