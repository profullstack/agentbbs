package pilot

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fpAlice = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const fpBob = "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

func writeRegistry(t *testing.T, entries []KeyEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.json")
	b, err := json.Marshal(registryFile{Keys: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testRegistry() Registry {
	alice := KeyEntry{Name: "alice", Fingerprint: fpAlice, PublishAudiences: []string{"helsinki"}, QueryAudiences: []string{"helsinki"}}
	bob := KeyEntry{Name: "bob", Fingerprint: fpBob, QueryAudiences: []string{"helsinki"}}
	return Registry{byName: map[string]KeyEntry{"alice": alice, "bob": bob}, byFP: map[string]KeyEntry{fpAlice: alice, fpBob: bob}}
}

func TestRegistryRejectsDuplicateAndRevokedKeys(t *testing.T) {
	path := writeRegistry(t, []KeyEntry{{Name: "alice", Fingerprint: fpAlice}, {Name: "bob", Fingerprint: fpAlice, Revoked: true}})
	if _, err := LoadRegistry(path); err == nil || !strings.Contains(err.Error(), "duplicate pilot key fingerprint") {
		t.Fatalf("duplicate fingerprint error = %v", err)
	}
	path = writeRegistry(t, []KeyEntry{{Name: "alice", Fingerprint: fpAlice, Revoked: true}})
	r, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Authorize("alice", fpAlice); ok {
		t.Fatal("revoked key authorized")
	}
}

func TestRegistryAudienceMatrix(t *testing.T) {
	r := testRegistry()
	cases := []struct {
		name, fp, audience string
		publish, query     bool
	}{
		{"alice", fpAlice, "helsinki", true, true},
		{"alice", fpAlice, "other", false, false},
		{"bob", fpBob, "helsinki", false, true},
		{"bob", fpBob, "other", false, false},
		{"bob", fpAlice, "helsinki", false, false},
		{"unknown", fpAlice, "helsinki", false, false},
		{"alice", fpBob, "helsinki", false, false},
	}
	for _, tc := range cases {
		if got := r.canPublish(tc.name, tc.fp, tc.audience); got != tc.publish {
			t.Errorf("publish(%q,%q,%q) = %v, want %v", tc.name, tc.fp, tc.audience, got, tc.publish)
		}
		if got := r.canQuery(tc.name, tc.fp, tc.audience); got != tc.query {
			t.Errorf("query(%q,%q,%q) = %v, want %v", tc.name, tc.fp, tc.audience, got, tc.query)
		}
	}
}

func TestReadJSONRequestRejectsMalformedCRLFSizeAndDuplicateFields(t *testing.T) {
	for name, input := range map[string]string{
		"malformed": `{"subject":`,
		"crlf":      "{\"subject\":\"alice\"}\r\n",
		"duplicate": `{"subject":"alice","subject":"bob"}`,
		"multiple":  `{"subject":"alice"}{"subject":"bob"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var dst map[string]any
			if err := readJSONRequest(strings.NewReader(input), 256, &dst); err == nil {
				t.Fatal("request unexpectedly accepted")
			}
		})
	}
	var dst map[string]any
	if err := readJSONRequest(strings.NewReader(`{"subject":"`+strings.Repeat("x", 300)+`"}`), 64, &dst); err == nil {
		t.Fatal("oversized request unexpectedly accepted")
	}
	if err := readJSONRequest(strings.NewReader(`{"subject":"alice"}`+"\n"), 256, &dst); err != nil {
		t.Fatalf("single LF should be accepted: %v", err)
	}
}

func testConfig() Config {
	return Config{MaxRequestBytes: 16 * 1024, MaxResponseBytes: 32 * 1024, MaxStatuses: 100, MaxAge: 15 * time.Minute, MaxTTL: time.Hour, MaxFutureSkew: 5 * time.Minute, RateWindow: time.Minute, RequestsPerWindow: 30}
}

func validPublish(now time.Time) publishRequest {
	return publishRequest{Subject: "alice", Audience: "helsinki", State: "healthy", Summary: "ready", ObservedAt: now.Add(-time.Minute).Format(time.RFC3339), ExpiresAt: now.Add(10 * time.Minute).Format(time.RFC3339), Revision: 1}
}

func TestFreshnessAndStatusValidation(t *testing.T) {
	now := time.Now().UTC()
	cfg := testConfig()
	entry := testRegistry().byName["alice"]
	status, err := parseAndValidatePublish(validPublish(now), entry, now, cfg)
	if err != nil || status.Subject != "alice" {
		t.Fatalf("valid status: %v %+v", err, status)
	}
	for name, mutate := range map[string]func(*publishRequest){
		"stale":        func(r *publishRequest) { r.ObservedAt = now.Add(-16 * time.Minute).Format(time.RFC3339) },
		"future":       func(r *publishRequest) { r.ObservedAt = now.Add(6 * time.Minute).Format(time.RFC3339) },
		"expired":      func(r *publishRequest) { r.ExpiresAt = now.Add(-time.Second).Format(time.RFC3339) },
		"long ttl":     func(r *publishRequest) { r.ExpiresAt = now.Add(2 * time.Hour).Format(time.RFC3339) },
		"wrong owner":  func(r *publishRequest) { r.Subject = "bob" },
		"bad state":    func(r *publishRequest) { r.State = "green" },
		"long summary": func(r *publishRequest) { r.Summary = strings.Repeat("x", 1025) },
	} {
		t.Run(name, func(t *testing.T) {
			req := validPublish(now)
			mutate(&req)
			if _, err := parseAndValidatePublish(req, entry, now, cfg); err == nil {
				t.Fatal("invalid status unexpectedly accepted")
			}
		})
	}
}

func TestStoreRestartPersistenceAndRevisionConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pilot.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	status := Status{Subject: "alice", Audience: "helsinki", State: "healthy", Summary: "ready", ObservedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), Revision: 1}
	if err := st.Publish(status); err != nil {
		t.Fatal(err)
	}
	if err := st.Publish(status); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	conflict := status
	conflict.Summary = "different"
	if err := st.Publish(conflict); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflict error = %v", err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := st.Backup(backup); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if info, err := os.Stat(backup); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions: info=%v err=%v", info, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatalf("retain old database: %v", err)
	}
	if err := Restore(backup, path); err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored permissions: info=%v err=%v", info, err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatalf("open restored backup: %v", err)
	}
	defer st.Close()
	rows, err := st.Query("helsinki", "alice", now, 10)
	if err != nil || len(rows) != 1 || rows[0].Summary != "ready" {
		t.Fatalf("restored query: err=%v rows=%+v", err, rows)
	}
	if rows[0].Stale {
		t.Fatal("restored status should be fresh")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err = st.Query("helsinki", "alice", now, 10)
	if err != nil || len(rows) != 1 || rows[0].Summary != "ready" {
		t.Fatalf("restart query: err=%v rows=%+v", err, rows)
	}
	if rows[0].Stale {
		t.Fatal("status should be fresh")
	}
	rows, err = st.Query("helsinki", "alice", now.Add(2*time.Minute), 10)
	if err != nil || len(rows) != 1 || !rows[0].Stale {
		t.Fatalf("stale derivation: err=%v rows=%+v", err, rows)
	}
}

func TestValidateConfigRejectsPublicBind(t *testing.T) {
	cfg := testConfig()
	cfg.Address = "0.0.0.0:2223"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("wildcard bind unexpectedly accepted")
	}
	cfg.Address = "192.168.1.2:2223"
	if err := validateConfig(cfg); err == nil {
		t.Fatal("RFC1918 bind unexpectedly accepted")
	}
	cfg.Address = "100.100.10.2:2223"
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("tailnet bind rejected: %v", err)
	}
}

func TestAllowedPeerMatchesBindScope(t *testing.T) {
	loop, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:5000")
	tail, _ := net.ResolveTCPAddr("tcp", "100.100.10.5:5000")
	lan, _ := net.ResolveTCPAddr("tcp", "192.168.1.5:5000")
	if !allowedPeer("127.0.0.1:2223", loop) || allowedPeer("127.0.0.1:2223", tail) {
		t.Fatal("loopback peer scope incorrect")
	}
	if !allowedPeer("100.100.10.2:2223", tail) || allowedPeer("100.100.10.2:2223", lan) {
		t.Fatal("tailnet peer scope incorrect")
	}
}

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(time.Minute, 2)
	now := time.Unix(100, 0)
	if !l.allow("alice", now) || !l.allow("alice", now.Add(time.Second)) || l.allow("alice", now.Add(2*time.Second)) {
		t.Fatal("rate limit did not cap requests")
	}
	if !l.allow("alice", now.Add(61*time.Second)) {
		t.Fatal("rate limit did not expire")
	}
}

func TestRunRequiresRegistryAndDoesNotUseNormalStore(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig()
	cfg.Address = "127.0.0.1:2223"
	cfg.RegistryPath = filepath.Join(root, "missing-registry")
	cfg.DatabasePath = filepath.Join(root, "pilot.db")
	cfg.HostKeyPath = filepath.Join(root, "host")
	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("missing registry unexpectedly accepted")
	}
}

func TestRunDoesNotGenerateHostKey(t *testing.T) {
	root := t.TempDir()
	registryPath := writeRegistry(t, []KeyEntry{{Name: "alice", Fingerprint: fpAlice, QueryAudiences: []string{"helsinki"}}})
	cfg := testConfig()
	cfg.Address = "127.0.0.1:2223"
	cfg.RegistryPath = registryPath
	cfg.DatabasePath = filepath.Join(root, "pilot.db")
	cfg.HostKeyPath = filepath.Join(root, "missing-host-key")
	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("startup without host key unexpectedly succeeded")
	}
	if _, err := os.Lstat(cfg.HostKeyPath); !os.IsNotExist(err) {
		t.Fatalf("startup created host key: err=%v", err)
	}
}
