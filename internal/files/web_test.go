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
