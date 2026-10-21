package payments

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateSessionAndVerifyPaid(t *testing.T) {
	var gotAuth string
	var body map[string]any
	status := "open"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/checkout/sessions":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"success":true,"id":"cs_abc","url":"https://coinpayportal.com/checkout/cs_abc","status":"open","methods":["BTC","ETH",{"code":"PAYPAL"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/checkout/sessions/cs_abc":
			_, _ = w.Write([]byte(`{"success":true,"session":{"id":"cs_abc","status":"` + status + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/payments/pay_old":
			_, _ = w.Write([]byte(`{"payment":{"id":"pay_old","status":"forwarded"}}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("AGENTBBS_COINPAY_API_URL", srv.URL)
	t.Setenv("COINPAY_API_KEY", "cp_live_test")
	t.Setenv("AGENTBBS_COINPAY_MERCHANT_ID", "")

	if !APIConfigured() {
		t.Fatal("a business key alone must configure hosted checkout")
	}
	ses, ok, err := CreatePremiumSession(Buyer{AccountID: "alice", Email: "a@example.com", PubKeyFP: "SHA256:xyz", UserID: 7}, "https://bbs.profullstack.com/~alice")
	if err != nil || !ok {
		t.Fatalf("create: ok=%v err=%v", ok, err)
	}
	if ses.ID != "cs_abc" || !strings.HasSuffix(ses.URL, "/checkout/cs_abc") {
		t.Fatalf("session fields wrong: %+v", ses)
	}
	if len(ses.Methods) != 3 || ses.Methods[2] != "PAYPAL" {
		t.Fatalf("methods = %v", ses.Methods)
	}
	if gotAuth != "Bearer cp_live_test" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	for k, want := range map[string]any{
		"customer_email": "a@example.com", "account_id": "alice", "property": "bbs.profullstack.com",
		"currency": "USD", "amount": float64(99), "expires_in_minutes": float64(60),
		"success_url": "https://bbs.profullstack.com/~alice",
	} {
		if body[k] != want {
			t.Errorf("body[%s] = %v, want %v", k, body[k], want)
		}
	}
	if _, has := body["blockchain"]; has {
		t.Error("hosted checkout must not pin a blockchain")
	}
	meta, _ := body["metadata"].(map[string]any)
	if meta["pubkey_fp"] != "SHA256:xyz" || meta["user_id"] != "7" || meta["ref"] != PremiumReference("SHA256:xyz") {
		t.Errorf("metadata = %v", meta)
	}

	if paid, checked := VerifyPremium("cs_abc"); !checked || paid {
		t.Fatalf("open session: paid=%v checked=%v", paid, checked)
	}
	status = "paid"
	if paid, checked := VerifyPremium("cs_abc"); !checked || !paid {
		t.Fatalf("paid session: paid=%v checked=%v", paid, checked)
	}
	// A legacy single-chain payment stored before hosted checkout still settles.
	if paid, checked := VerifyPremium("pay_old"); !checked || !paid {
		t.Fatalf("legacy payment: paid=%v checked=%v", paid, checked)
	}
	if _, checked := VerifyPremium(""); checked {
		t.Fatal("empty id must not be checked")
	}
}

func TestCreateSessionNeedsEmail(t *testing.T) {
	t.Setenv("COINPAY_API_KEY", "cp_live_test")
	if _, ok, err := CreatePremiumSession(Buyer{AccountID: "alice"}, ""); ok || err == nil {
		t.Fatalf("missing email: ok=%v err=%v", ok, err)
	}
}

func TestNotConfigured(t *testing.T) {
	t.Setenv("COINPAY_API_KEY", "")
	if APIConfigured() {
		t.Fatal("must not be configured without a key")
	}
	if _, ok, err := CreatePremiumSession(Buyer{AccountID: "a", Email: "a@b.co"}, ""); ok || err != nil {
		t.Fatalf("unconfigured create: ok=%v err=%v", ok, err)
	}
	if _, checked := VerifyPremium("cs_abc"); checked {
		t.Fatal("unconfigured verify must not be checked")
	}
}

func TestTerminalQR(t *testing.T) {
	q := TerminalQR("https://coinpayportal.com/checkout/cs_abc", "  ")
	lines := strings.Split(strings.TrimRight(q, "\n"), "\n")
	if len(lines) < 10 || !strings.HasPrefix(lines[0], "  █") {
		t.Fatalf("unexpected QR:\n%s", q)
	}
}
