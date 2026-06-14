// Package payments gates the one-time $99 Founding Lifetime membership on
// CoinPay (coinpayportal.com). It talks to the CoinPay REST API directly over
// HTTP — no `coinpay` CLI needs to be installed on the host.
//
// Config (env):
//
//	COINPAY_API_KEY                Bearer key for the CoinPay API (cp_live_…)
//	AGENTBBS_COINPAY_MERCHANT_ID   merchant/business id payments are created under
//	AGENTBBS_COINPAY_API_URL       API base (default https://coinpayportal.com/api)
//	AGENTBBS_PREMIUM_AMOUNT        fiat amount (default 99)
//	AGENTBBS_PREMIUM_CURRENCY      fiat currency (default USD)
//	AGENTBBS_PREMIUM_BLOCKCHAIN    settlement chain (default eth)
//
// Operators can also grant pod time manually: `agentbbs grant-pod <user> N`.
package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// PodTerm is how much access one manual pod grant buys (grant-pod CLI).
const PodTerm = 31 * 24 * time.Hour

// PremiumPriceLabel is the human-readable price for the one-time lifetime
// membership offered at join@.
const PremiumPriceLabel = "$99 (lifetime)"

// FoundingCap is the marketing cap on Founding Lifetime memberships — the offer
// is pitched as available to the first this-many accounts.
const FoundingCap = "1,000"

// Premium charge parameters — overridable via env so the offer can change
// without a rebuild.
func PremiumAmount() string     { return envOr("AGENTBBS_PREMIUM_AMOUNT", "99") }
func PremiumCurrency() string   { return envOr("AGENTBBS_PREMIUM_CURRENCY", "USD") }
func PremiumBlockchain() string { return envOr("AGENTBBS_PREMIUM_BLOCKCHAIN", "eth") }

// MerchantID is the CoinPay merchant/business id payments are created under.
func MerchantID() string { return os.Getenv("AGENTBBS_COINPAY_MERCHANT_ID") }

func apiKey() string { return os.Getenv("COINPAY_API_KEY") }
func apiBase() string {
	return trimSlash(envOr("AGENTBBS_COINPAY_API_URL", "https://coinpayportal.com/api"))
}

// APIConfigured reports whether live CoinPay calls can be made.
func APIConfigured() bool { return apiKey() != "" && MerchantID() != "" }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// Charge is a created CoinPay payment a user must fund: a unique deposit
// address, the crypto amount, the fiat amount it settles, and the payment id
// (store it to verify settlement later).
type Charge struct {
	Address      string
	CryptoAmount string
	Currency     string
	FiatAmount   string
	FiatCurrency string
	ID           string
	QR           string
}

// PremiumReference derives a stable memo for a user's lifetime membership from
// their key fingerprint; sent as payment metadata for reconciliation.
func PremiumReference(pubkeyFP string) string { return Reference("premium", pubkeyFP) }

// Reference derives a stable, short payment reference for a user+plan.
func Reference(plan, pubkeyFP string) string {
	mac := hmac.New(sha256.New, []byte("agentbbs."+plan))
	mac.Write([]byte(pubkeyFP))
	return "abbs-" + plan + "-" + hex.EncodeToString(mac.Sum(nil))[:12]
}

// flexStr decodes a JSON value that may arrive as either a string or a number
// into a string. CoinPay returns crypto_amount as a bare JSON number (e.g.
// 0.0031), but has sent it quoted in the past — accept both so a representation
// change on their side can't break the charge again.
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexStr(s)
		return nil
	}
	*f = flexStr(b) // number (or other scalar) — keep its literal text
	return nil
}

// coinpayPayment is the (subset of the) CoinPay payment object, returned
// wrapped as {"payment": {…}}.
type coinpayPayment struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	Address      string  `json:"payment_address"`
	CryptoAmount flexStr `json:"crypto_amount"`
	CryptoCurr   string  `json:"crypto_currency"`
	QR           string  `json:"qr_code"`
}

type paymentEnvelope struct {
	Payment coinpayPayment `json:"payment"`
}

// coinpayDo performs an authenticated CoinPay API call and decodes the
// {"payment":{…}} envelope.
func coinpayDo(ctx context.Context, method, path string, body any) (*paymentEnvelope, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase()+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("coinpay %s %s: %s: %s", method, path, resp.Status, bytesTrim(raw))
	}
	var env paymentEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("coinpay %s %s: bad response: %w", method, path, err)
	}
	return &env, nil
}

func bytesTrim(b []byte) string { return string(bytes.TrimSpace(b)) }

// CreatePremiumCharge creates a CoinPay payment for the lifetime membership and
// returns the deposit address, amount, and payment id (store the id to verify
// settlement later). created is false when CoinPay isn't configured.
func CreatePremiumCharge(ref string) (Charge, bool, error) {
	if !APIConfigured() {
		return Charge{}, false, nil
	}
	amount := json.Number(PremiumAmount())
	if f, err := strconv.ParseFloat(PremiumAmount(), 64); err == nil {
		amount = json.Number(strconv.FormatFloat(f, 'f', -1, 64))
	}
	body := map[string]any{
		"business_id": MerchantID(),
		"amount":      amount,
		"currency":    PremiumCurrency(),
		"blockchain":  toUpper(PremiumBlockchain()),
		"description": "AgentBBS Premium membership (lifetime)",
		"metadata":    map[string]string{"ref": ref},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	env, err := coinpayDo(ctx, http.MethodPost, "/payments/create", body)
	if err != nil {
		return Charge{}, false, err
	}
	p := env.Payment
	if p.Address == "" {
		return Charge{}, false, fmt.Errorf("coinpay: payment created without an address")
	}
	return Charge{
		Address:      p.Address,
		CryptoAmount: string(p.CryptoAmount),
		Currency:     p.CryptoCurr,
		FiatAmount:   PremiumAmount(),
		FiatCurrency: PremiumCurrency(),
		ID:           p.ID,
		QR:           p.QR,
	}, true, nil
}

// VerifyPremium reports whether a created premium payment has settled. payID is
// the CoinPay payment id from CreatePremiumCharge. checked is false when we
// could not reach CoinPay (caller falls back to store state); paid is true on a
// confirmed/forwarded status.
func VerifyPremium(payID string) (paid bool, checked bool) {
	if payID == "" || !APIConfigured() {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	env, err := coinpayDo(ctx, http.MethodGet, "/payments/"+url.PathEscape(payID), nil)
	if err != nil {
		return false, false // network/API error — fall back to store state
	}
	switch env.Payment.Status {
	case "confirmed", "forwarded", "completed", "paid":
		return true, true
	default:
		return false, true
	}
}

func toUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
