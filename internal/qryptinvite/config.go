package qryptinvite

import (
	"crypto/ed25519"
	"errors"
	"os"
	"strconv"
	"time"
)

// Config is the resolved qrypt.chat invite-issuer configuration, read from the
// environment (see docs/qrypt-invites.md). It is shared by the SSH plugin and
// the ops CLI so they mint identical tokens.
type Config struct {
	IssuerID  string        // AGENTBBS_QRYPT_ISSUER_ID (default "agentbbs")
	Key       string        // AGENTBBS_QRYPT_ISSUER_KEY (base64 seed/priv)
	TTL       time.Duration // AGENTBBS_QRYPT_INVITE_TTL (default 168h)
	RedeemURL string        // AGENTBBS_QRYPT_REDEEM_URL (default https://qrypt.chat/anon?invite=)
	Quota     int           // AGENTBBS_QRYPT_INVITE_QUOTA (default 5)
}

// DefaultIssuerID, DefaultRedeemURL, DefaultTTL and DefaultQuota are the
// fallbacks when the corresponding env var is unset.
const (
	DefaultIssuerID  = "agentbbs"
	DefaultRedeemURL = "https://qrypt.chat/anon?invite="
	DefaultTTL       = 168 * time.Hour
	DefaultQuota     = 5
)

// ConfigFromEnv reads the AGENTBBS_QRYPT_* environment variables, applying
// defaults. The key is not validated here; call PrivateKey to parse it.
func ConfigFromEnv() Config {
	c := Config{
		IssuerID:  DefaultIssuerID,
		Key:       os.Getenv("AGENTBBS_QRYPT_ISSUER_KEY"),
		TTL:       DefaultTTL,
		RedeemURL: DefaultRedeemURL,
		Quota:     DefaultQuota,
	}
	if v := os.Getenv("AGENTBBS_QRYPT_ISSUER_ID"); v != "" {
		c.IssuerID = v
	}
	if v := os.Getenv("AGENTBBS_QRYPT_REDEEM_URL"); v != "" {
		c.RedeemURL = v
	}
	if v := os.Getenv("AGENTBBS_QRYPT_INVITE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.TTL = d
		}
	}
	if v := os.Getenv("AGENTBBS_QRYPT_INVITE_QUOTA"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Quota = n
		}
	}
	return c
}

// ErrNoKey means AGENTBBS_QRYPT_ISSUER_KEY is unset, so no tokens can be minted.
var ErrNoKey = errors.New("qryptinvite: AGENTBBS_QRYPT_ISSUER_KEY is not set (run: agentbbs qrypt-issuer-keygen)")

// PrivateKey parses the configured issuer key, or returns ErrNoKey if unset.
func (c Config) PrivateKey() (ed25519.PrivateKey, error) {
	if c.Key == "" {
		return nil, ErrNoKey
	}
	return ParsePrivateKey(c.Key)
}

// RedeemLink returns the full URL a member opens to redeem token.
func (c Config) RedeemURLFor(token string) string {
	return c.RedeemURL + token
}
