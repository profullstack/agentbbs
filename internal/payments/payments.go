// Package payments gates paid features (the pod subscription, $1/mo) on
// CoinPay — the default LogicSRC payment/DID/wallet plugin.
//
// v1 integration is CLI-shaped: join@ hands the user a `coinpay` command
// carrying a unique payment reference, and verification shells out to the
// coinpay CLI. The exact command templates are env-configurable so the
// deployed CoinPay surface can evolve without a rebuild:
//
//	AGENTBBS_COINPAY_PAY_TMPL    e.g. "coinpay pay --to profullstack --amount 1 --currency USDC --memo %s"
//	AGENTBBS_COINPAY_VERIFY_CMD  e.g. "coinpay verify --memo %s" (exit 0 == paid)
//
// Operators can also grant manually: `agentbbs grant-pod <user> --months N`.
package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// PodPriceLabel is the human-readable price for the pod membership.
const PodPriceLabel = "$1/mo"

// PodTerm is how much access one payment buys.
const PodTerm = 31 * 24 * time.Hour

// PremiumPriceLabel is the human-readable price for the one-time lifetime
// membership offered at join@.
const PremiumPriceLabel = "$10 (lifetime)"

// premium charge defaults — all overridable via env so the CoinPay surface can
// change without a rebuild (mirrors the pod templates above).
func PremiumAmount() string     { return envOr("AGENTBBS_PREMIUM_AMOUNT", "10") }
func PremiumCurrency() string   { return envOr("AGENTBBS_PREMIUM_CURRENCY", "USD") }
func PremiumBlockchain() string { return envOr("AGENTBBS_PREMIUM_BLOCKCHAIN", "eth") }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Charge is a created CoinPay payment a user must fund: a unique deposit
// address plus the crypto amount (and the fiat amount it settles).
type Charge struct {
	Address      string `json:"payment_address"`
	CryptoAmount string `json:"crypto_amount"`
	Currency     string `json:"crypto_currency"`
	FiatAmount   string `json:"amount"`
	FiatCurrency string `json:"currency"`
	ID           string `json:"id"`
	QR           string `json:"qr_code"`
}

// PremiumReference derives the stable CoinPay memo for a user's lifetime
// membership from their key fingerprint.
func PremiumReference(pubkeyFP string) string { return Reference("premium", pubkeyFP) }

// CreatePremiumCharge shells out to the CoinPay CLI to mint a payment address
// for the $10 lifetime membership and parses the JSON it prints. created is
// false when no create command is configured or the CLI is unavailable, so the
// caller can fall back to PremiumPayCommand. The reference is passed as the
// payment metadata/memo so the eventual settlement reconciles to the account.
//
//	AGENTBBS_COINPAY_PREMIUM_CREATE_CMD
//	  default: coinpay payment create --amount 10 --currency USD --blockchain eth --json --metadata %s
func CreatePremiumCharge(ref string) (Charge, bool, error) {
	tmpl := os.Getenv("AGENTBBS_COINPAY_PREMIUM_CREATE_CMD")
	if tmpl == "" {
		tmpl = "coinpay payment create --amount " + PremiumAmount() +
			" --currency " + PremiumCurrency() +
			" --blockchain " + PremiumBlockchain() + " --json --metadata %s"
	}
	line := tmpl
	if strings.Contains(tmpl, "%s") {
		line = fmt.Sprintf(tmpl, ref)
	} else {
		line = tmpl + " " + ref
	}
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return Charge{}, false, nil
	}
	if _, err := exec.LookPath(parts[0]); err != nil {
		return Charge{}, false, nil // CLI not installed — caller falls back
	}
	out, err := exec.Command(parts[0], parts[1:]...).Output()
	if err != nil {
		return Charge{}, false, err
	}
	var c Charge
	if err := json.Unmarshal(out, &c); err != nil {
		// Some CLIs wrap the payment under a top-level key, e.g. {"payment":{…}}.
		var wrap struct {
			Payment Charge `json:"payment"`
		}
		if json.Unmarshal(out, &wrap) == nil && wrap.Payment.Address != "" {
			c = wrap.Payment
		} else {
			return Charge{}, false, err
		}
	}
	if c.Address == "" {
		return Charge{}, false, nil
	}
	return c, true, nil
}

// PremiumPayCommand is the manual fallback shown when no charge could be minted
// in-session: the command the user can run themselves to pay.
//
//	AGENTBBS_COINPAY_PREMIUM_PAY_TMPL
func PremiumPayCommand(ref string) string {
	tmpl := os.Getenv("AGENTBBS_COINPAY_PREMIUM_PAY_TMPL")
	if tmpl == "" {
		tmpl = "coinpay payment create --amount " + PremiumAmount() +
			" --currency " + PremiumCurrency() +
			" --blockchain " + PremiumBlockchain() + " --metadata %s"
	}
	if strings.Contains(tmpl, "%s") {
		return fmt.Sprintf(tmpl, ref)
	}
	return tmpl + " " + ref
}

// VerifyPremium checks whether a premium charge has settled, via the CoinPay
// status command. Like Verify, checked is false when unconfigured/unavailable.
//
//	AGENTBBS_COINPAY_PREMIUM_STATUS_CMD  e.g. "coinpay payment status %s" (exit 0 == paid)
func VerifyPremium(payRef string) (paid bool, checked bool) {
	return runVerify(os.Getenv("AGENTBBS_COINPAY_PREMIUM_STATUS_CMD"), payRef)
}

// Reference derives a stable, short payment reference for a user+plan from
// the user's key fingerprint, so CoinPay memos can be reconciled to accounts.
func Reference(plan, pubkeyFP string) string {
	mac := hmac.New(sha256.New, []byte("agentbbs."+plan))
	mac.Write([]byte(pubkeyFP))
	return "abbs-" + plan + "-" + hex.EncodeToString(mac.Sum(nil))[:12]
}

// PayCommand renders the coinpay command a user should run, with the payment
// reference substituted.
func PayCommand(ref string) string {
	tmpl := os.Getenv("AGENTBBS_COINPAY_PAY_TMPL")
	if tmpl == "" {
		tmpl = "coinpay pay --to profullstack --amount 1 --currency USDC --memo %s"
	}
	if strings.Contains(tmpl, "%s") {
		return fmt.Sprintf(tmpl, ref)
	}
	return tmpl + " " + ref
}

// Verify checks a payment reference against the coinpay CLI. It returns
// (paid, checked): checked is false when no verifier is configured or the
// coinpay binary is unavailable, so callers can fall back to store state.
func Verify(ref string) (paid bool, checked bool) {
	return runVerify(os.Getenv("AGENTBBS_COINPAY_VERIFY_CMD"), ref)
}

// runVerify runs a "%s"-templated verify command and maps its exit status to
// (paid, checked): checked is false when the template is empty or the binary is
// absent, so callers fall back to store state.
func runVerify(tmpl, ref string) (paid bool, checked bool) {
	if tmpl == "" {
		return false, false
	}
	line := tmpl
	if strings.Contains(tmpl, "%s") {
		line = fmt.Sprintf(tmpl, ref)
	} else {
		line = tmpl + " " + ref
	}
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, false
	}
	if _, err := exec.LookPath(parts[0]); err != nil {
		return false, false
	}
	if err := exec.Command(parts[0], parts[1:]...).Run(); err != nil {
		return false, true
	}
	return true, true
}
