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
	tmpl := os.Getenv("AGENTBBS_COINPAY_VERIFY_CMD")
	if tmpl == "" {
		return false, false
	}
	var line string
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
	cmd := exec.Command(parts[0], parts[1:]...)
	if err := cmd.Run(); err != nil {
		return false, true
	}
	return true, true
}
