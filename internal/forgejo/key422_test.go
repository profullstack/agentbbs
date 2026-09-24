package forgejo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// key422Server answers the dedupe GET with an empty list, then returns 422 with
// the given body for the POST.
func key422Server(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(body))
	}))
}

func TestEnsureKeyTreatsAlreadyUsed422AsBenign(t *testing.T) {
	srv := key422Server(t, `{"message":"Key content has been used as non-deploy key"}`)
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	added, err := c.EnsureKey("alice", "agentbbs", aliceKey)
	if err != nil {
		t.Fatalf("a duplicate key must not be an error, got %v", err)
	}
	if added {
		t.Error("expected added=false for a key already on the account")
	}
}

// A 422 that is Forgejo rejecting the key content must surface, not be silently
// swallowed as "already exists" — that is how an unregisterable key stayed
// invisible in the logs forever.
func TestEnsureKeySurfacesRejecting422(t *testing.T) {
	srv := key422Server(t, `{"message":"Key content is not a valid SSH key"}`)
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	added, err := c.EnsureKey("alice", "agentbbs", aliceKey)
	if err == nil {
		t.Fatal("expected an error when Forgejo rejects the key content")
	}
	if added {
		t.Error("expected added=false on rejection")
	}
	if !strings.Contains(err.Error(), "not a valid SSH key") {
		t.Errorf("error should carry Forgejo's reason, got %v", err)
	}
}
