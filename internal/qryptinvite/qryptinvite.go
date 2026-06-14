// Package qryptinvite mints single-use, Ed25519-signed invite tokens that the
// qrypt.chat app accepts to create an ANONYMOUS account. AgentBBS is the
// trusted issuer: it holds the private key and signs tokens; qrypt.chat verifies
// them against the issuer's public key registered in its invite_issuers table.
//
// Token format (v1, see the shared qrypt-invite contract):
//
//	token        = "qci1." + b64url(payloadJSON) + "." + b64url(sig)
//	signing input = "qci1." + b64url(payloadJSON)   (the first two segments)
//	sig          = Ed25519.Sign(issuerPriv, []byte(signing input))
//	b64url       = base64 URL-encoding, NO padding
//
// There is no alg field and this is not a JWT: Ed25519 is the single fixed
// algorithm.
package qryptinvite

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Prefix is segment[0] of every v1 token; verifiers must reject anything else.
const Prefix = "qci1"

// b64 is the URL-safe, unpadded base64 alphabet the contract mandates.
var b64 = base64.RawURLEncoding

// Payload is the JSON body carried in segment[1] of a token. Field tags match
// the contract exactly; qrypt.chat decodes the same shape.
type Payload struct {
	JTI  string `json:"jti"`  // 16 random bytes, hex (32 chars); burns the token
	Iss  string `json:"iss"`  // issuer id, e.g. "agentbbs"
	Tier string `json:"tier"` // always "anonymous" in v1
	Iat  int64  `json:"iat"`  // issued-at (unix seconds)
	Exp  int64  `json:"exp"`  // expiry (unix seconds)
	Uses int    `json:"uses"` // single-use: 1
}

// Mint produces a signed single-use anonymous invite token for issuerID, valid
// for ttl. It returns the token string and its jti (the unique id qrypt.chat
// stores on redeem to prevent double-spend).
func Mint(issuerID string, priv ed25519.PrivateKey, ttl time.Duration) (token string, jti string, err error) {
	if issuerID == "" {
		return "", "", errors.New("qryptinvite: empty issuer id")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return "", "", fmt.Errorf("qryptinvite: private key is %d bytes, want %d", len(priv), ed25519.PrivateKeySize)
	}
	if ttl <= 0 {
		return "", "", errors.New("qryptinvite: ttl must be positive")
	}

	jti, err = newJTI()
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	payload := Payload{
		JTI:  jti,
		Iss:  issuerID,
		Tier: "anonymous",
		Iat:  now.Unix(),
		Exp:  now.Add(ttl).Unix(),
		Uses: 1,
	}
	pj, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	signingInput := Prefix + "." + b64.EncodeToString(pj)
	sig := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + b64.EncodeToString(sig), jti, nil
}

// newJTI returns 16 random bytes hex-encoded (32 chars), per the contract.
func newJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// GenerateIssuerKey creates a fresh Ed25519 issuer keypair for first-time setup.
// seedB64 is the 32-byte seed (the PRIVATE half — set it as AGENTBBS_QRYPT_ISSUER_KEY);
// publicKeyB64 is the raw 32-byte public key (register it in qrypt.chat's
// invite_issuers.ed25519_public_key). Both are standard base64.
func GenerateIssuerKey() (seedB64 string, publicKeyB64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	seed := priv.Seed() // 32 bytes
	return base64.StdEncoding.EncodeToString(seed),
		base64.StdEncoding.EncodeToString(pub), nil
}

// ParsePrivateKey decodes a base64 issuer key into an ed25519.PrivateKey. It
// accepts either a 32-byte seed (preferred, what GenerateIssuerKey emits) or a
// full 64-byte private key. Base64 may be standard or URL-encoded, padded or not.
func ParsePrivateKey(b64key string) (ed25519.PrivateKey, error) {
	raw, err := decodeBase64Any(b64key)
	if err != nil {
		return nil, fmt.Errorf("qryptinvite: decode private key: %w", err)
	}
	switch len(raw) {
	case ed25519.SeedSize: // 32
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize: // 64
		return ed25519.PrivateKey(raw), nil
	default:
		return nil, fmt.Errorf("qryptinvite: private key is %d bytes, want %d (seed) or %d (full key)",
			len(raw), ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

// PublicKeyB64 returns the raw 32-byte public key (standard base64) for a
// private key — the value an operator registers in qrypt.chat.
func PublicKeyB64(priv ed25519.PrivateKey) string {
	pub := priv.Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(pub)
}

// decodeBase64Any tries the four base64 variants the contract may produce.
func decodeBase64Any(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not valid base64")
}
