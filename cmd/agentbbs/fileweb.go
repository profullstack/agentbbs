package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/profullstack/agentbbs/internal/files"
	"github.com/profullstack/agentbbs/internal/mailbox"
	"github.com/profullstack/agentbbs/internal/store"
)

// startFilesWeb serves the browser-based file manager (files.<host>) on a
// loopback address Caddy reverse-proxies. Members sign in with their webmail
// password — no SSH key needed — and browse the same /me and /public areas as
// SFTP. No-op when Files is disabled (a.files == nil).
func (a *app) startFilesWeb() {
	if a.files == nil {
		return
	}
	addr := env("AGENTBBS_FILES_WEB_ADDR", "127.0.0.1:8092")
	title := env("AGENTBBS_FILES_WEB_TITLE", "files."+strings.TrimPrefix(a.host, "bbs."))
	h := a.files.WebHandler(files.WebConfig{Authenticate: a.filesWebAuth, Title: title})
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("files web listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("files web", "err", err)
		}
	}()
}

// filesWebAuth validates a member's webmail credentials against the Mailu IMAP
// backend (the same login Roundcube uses), then maps them to the account. The
// username may be a bare handle or a full address; only the local part matters.
func (a *app) filesWebAuth(user, pass string) (store.User, bool, error) {
	name := strings.ToLower(strings.TrimSpace(user))
	if at := strings.IndexByte(name, '@'); at >= 0 {
		name = name[:at]
	}
	if name == "" || pass == "" {
		return store.User{}, false, nil
	}
	u, ok, err := a.st.UserByName(name)
	if err != nil {
		return store.User{}, false, err
	}
	if !ok || u.Banned {
		return store.User{}, false, nil
	}
	imapAddr := env("AGENTBBS_MAIL_IMAP_ADDR", a.mailHost+":993")
	plaintext := os.Getenv("AGENTBBS_MAIL_IMAP_PLAINTEXT") == "1"
	if err := mailbox.VerifyLogin(imapAddr, name+"@"+a.mailDomain, pass, plaintext); err != nil {
		return store.User{}, false, nil // credentials rejected
	}
	return u, true, nil
}
