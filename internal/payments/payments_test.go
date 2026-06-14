package payments

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAndVerifyPremium(t *testing.T) {
	var gotAuth, gotBlockchain, gotBusiness string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/payments/create":
			var body map[string]any
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			gotBlockchain, _ = body["blockchain"].(string)
			gotBusiness, _ = body["business_id"].(string)
			_, _ = w.Write([]byte(`{"payment":{"id":"pay_1","payment_address":"0xABC","crypto_amount":"0.0031","crypto_currency":"ETH","status":"pending"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/payments/pay_1":
			_, _ = w.Write([]byte(`{"payment":{"id":"pay_1","status":"confirmed"}}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("AGENTBBS_COINPAY_API_URL", srv.URL)
	t.Setenv("COINPAY_API_KEY", "cp_test_key")
	t.Setenv("AGENTBBS_COINPAY_MERCHANT_ID", "biz_42")

	if !APIConfigured() {
		t.Fatal("APIConfigured should be true with key + merchant id")
	}

	c, ok, err := CreatePremiumCharge("abbs-premium-deadbeef")
	if err != nil || !ok {
		t.Fatalf("create: ok=%v err=%v", ok, err)
	}
	if c.Address != "0xABC" || c.ID != "pay_1" || c.CryptoAmount != "0.0031" {
		t.Fatalf("charge fields wrong: %+v", c)
	}
	if gotAuth != "Bearer cp_test_key" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotBlockchain != "ETH" {
		t.Fatalf("blockchain not upper-cased: %q", gotBlockchain)
	}
	if gotBusiness != "biz_42" {
		t.Fatalf("business_id = %q", gotBusiness)
	}

	if paid, checked := VerifyPremium("pay_1"); !checked || !paid {
		t.Fatalf("verify confirmed: paid=%v checked=%v", paid, checked)
	}
	// Empty id is a clean "not checked".
	if _, checked := VerifyPremium(""); checked {
		t.Fatal("empty payID must not be checked")
	}
}

func TestNotConfigured(t *testing.T) {
	t.Setenv("COINPAY_API_KEY", "")
	t.Setenv("AGENTBBS_COINPAY_MERCHANT_ID", "")
	if APIConfigured() {
		t.Fatal("must not be configured without key/merchant")
	}
	if _, ok, err := CreatePremiumCharge("ref"); ok || err != nil {
		t.Fatalf("unconfigured create: ok=%v err=%v", ok, err)
	}
	if _, checked := VerifyPremium("pay_1"); checked {
		t.Fatal("unconfigured verify must not be checked")
	}
}
