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
//	AGENTBBS_COINPAY_PROPERTY      site hostname on the session (default bbs.profullstack.com)
//
// Checkout is a CoinPay hosted session (POST /checkout/sessions): the buyer
// opens its URL (shown with a QR code in the SSH terminal), picks any coin the
// business accepts or PayPal, and we poll GET /checkout/sessions/:id for paid.
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
	"strings"
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
func PremiumAmount() string   { return envOr("AGENTBBS_PREMIUM_AMOUNT", "99") }
func PremiumCurrency() string { return envOr("AGENTBBS_PREMIUM_CURRENCY", "USD") }

// MerchantID is the CoinPay merchant/business id payments are created under.
func MerchantID() string { return os.Getenv("AGENTBBS_COINPAY_MERCHANT_ID") }

func apiKey() string { return os.Getenv("COINPAY_API_KEY") }
func apiBase() string {
	return trimSlash(envOr("AGENTBBS_COINPAY_API_URL", "https://coinpayportal.com/api"))
}

// APIConfigured reports whether live CoinPay calls can be made. The business
// API key identifies the merchant, so a hosted checkout needs only the key.
func APIConfigured() bool { return apiKey() != "" }

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

// PremiumReference derives a stable memo for a user's lifetime membership from
// their key fingerprint; sent as payment metadata for reconciliation.
func PremiumReference(pubkeyFP string) string { return Reference("premium", pubkeyFP) }

// Reference derives a stable, short payment reference for a user+plan.
func Reference(plan, pubkeyFP string) string {
	mac := hmac.New(sha256.New, []byte("agentbbs."+plan))
	mac.Write([]byte(pubkeyFP))
	return "abbs-" + plan + "-" + hex.EncodeToString(mac.Sum(nil))[:12]
}

// Property is the site hostname CoinPay records on the checkout session and
// echoes on every webhook, so the shared business can tell BBS sales apart.
func Property() string { return envOr("AGENTBBS_COINPAY_PROPERTY", "bbs.profullstack.com") }

// Buyer identifies who a premium checkout session is for.
type Buyer struct {
	AccountID string // stable account handle (CoinPay account_id)
	Email     string // verified member email (CoinPay customer_email)
	PubKeyFP  string // SSH key fingerprint (metadata only)
	UserID    int64  // local store id (metadata only)
}

// Session is a created CoinPay hosted checkout session: the buyer opens URL,
// picks any coin the business accepts or PayPal, and pays there.
type Session struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Status    string   `json:"status"`
	ExpiresAt string   `json:"expires_at"`
	Methods   []string `json:"-"`
}

// SessionBody builds the POST /checkout/sessions request body. Exposed for tests.
func SessionBody(b Buyer, successURL string) map[string]any {
	amount := json.Number(PremiumAmount())
	if f, err := strconv.ParseFloat(PremiumAmount(), 64); err == nil {
		amount = json.Number(strconv.FormatFloat(f, 'f', -1, 64))
	}
	body := map[string]any{
		"amount":             amount,
		"currency":           PremiumCurrency(),
		"description":        "AgentBBS Founding Lifetime membership",
		"customer_email":     b.Email,
		"account_id":         b.AccountID,
		"property":           Property(),
		"expires_in_minutes": 60,
		"metadata": map[string]any{
			"ref":       PremiumReference(b.PubKeyFP),
			"plan":      "premium_lifetime",
			"handle":    b.AccountID,
			"pubkey_fp": b.PubKeyFP,
			"user_id":   strconv.FormatInt(b.UserID, 10),
			"site":      Property(),
		},
	}
	if successURL != "" {
		body["success_url"] = successURL
		body["cancel_url"] = successURL
	}
	return body
}

// apiDo performs an authenticated CoinPay API call and returns the raw body.
func apiDo(ctx context.Context, method, path string, body any) ([]byte, error) {
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
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("coinpay %s %s: %s: %s", method, path, resp.Status, bytesTrim(raw))
	}
	return raw, nil
}

func bytesTrim(b []byte) string { return string(bytes.TrimSpace(b)) }

// CreatePremiumSession creates a hosted CoinPay checkout session for the
// lifetime membership. created is false when CoinPay isn't configured. Store
// the session id (cs_...) to verify settlement later.
func CreatePremiumSession(b Buyer, successURL string) (Session, bool, error) {
	if !APIConfigured() {
		return Session{}, false, nil
	}
	if b.Email == "" || b.AccountID == "" {
		return Session{}, false, fmt.Errorf("coinpay: checkout needs an email and an account id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := apiDo(ctx, http.MethodPost, "/checkout/sessions", SessionBody(b, successURL))
	if err != nil {
		return Session{}, false, err
	}
	var out struct {
		Session
		Methods []json.RawMessage `json:"methods"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Session{}, false, fmt.Errorf("coinpay: bad session response: %w", err)
	}
	if out.ID == "" || out.URL == "" {
		return Session{}, false, fmt.Errorf("coinpay: session created without an id/url: %s", bytesTrim(raw))
	}
	ses := out.Session
	for _, m := range out.Methods {
		var code string
		if json.Unmarshal(m, &code) == nil {
			ses.Methods = append(ses.Methods, code)
			continue
		}
		var obj map[string]any
		if json.Unmarshal(m, &obj) == nil {
			for _, k := range []string{"code", "method", "id", "type"} {
				if v, ok := obj[k].(string); ok && v != "" {
					ses.Methods = append(ses.Methods, v)
					break
				}
			}
		}
	}
	return ses, true, nil
}

// paidStatus reports whether a CoinPay session/payment status means settled.
func paidStatus(st string) bool {
	switch st {
	case "paid", "complete", "completed", "confirmed", "forwarded":
		return true
	}
	return false
}

// SessionStatus fetches a checkout session's status.
func SessionStatus(ctx context.Context, id string) (string, error) {
	raw, err := apiDo(ctx, http.MethodGet, "/checkout/sessions/"+url.PathEscape(id), nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Status  string `json:"status"`
		Session *struct {
			Status string `json:"status"`
		} `json:"session"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.Status == "" && out.Session != nil {
		out.Status = out.Session.Status
	}
	return out.Status, nil
}

// VerifyPremium reports whether a created premium payment has settled. payID is
// the CoinPay checkout session id (cs_...) from CreatePremiumSession, or a legacy
// payment id from before hosted checkout. checked is false when we
// could not reach CoinPay (caller falls back to store state); paid is true on a
// confirmed/forwarded status.
func VerifyPremium(payID string) (paid bool, checked bool) {
	if payID == "" || !APIConfigured() {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if strings.HasPrefix(payID, "cs_") {
		st, err := SessionStatus(ctx, payID)
		if err != nil {
			return false, false // network/API error: fall back to store state
		}
		return paidStatus(st), true
	}
	// Legacy single-chain payment ids from before hosted checkout.
	raw, err := apiDo(ctx, http.MethodGet, "/payments/"+url.PathEscape(payID), nil)
	if err != nil {
		return false, false
	}
	var env struct {
		Payment struct {
			Status string `json:"status"`
		} `json:"payment"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return false, false
	}
	return paidStatus(env.Payment.Status), true
}
