package qryptinvite

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// verifyAsQrypt independently checks a token the way the qrypt.chat backend
// would: split into 3 segments, require segment[0] == "qci1", verify the
// Ed25519 signature over "qci1."+payloadSeg with the issuer's public key, and
// decode the payload. It deliberately does NOT reuse Mint's internals.
func verifyAsQrypt(t *testing.T, token string, pub ed25519.PublicKey) Payload {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	if parts[0] != "qci1" {
		t.Fatalf("segment[0] = %q, want qci1", parts[0])
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if !ed25519.Verify(pub, []byte(signingInput), sig) {
		t.Fatal("signature did not verify")
	}
	pj, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var p Payload
	if err := json.Unmarshal(pj, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return p
}

func TestMintAndVerify(t *testing.T) {
	seedB64, pubB64, err := GenerateIssuerKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ParsePrivateKey(seedB64)
	if err != nil {
		t.Fatalf("ParsePrivateKey(seed): %v", err)
	}
	pubRaw, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	pub := ed25519.PublicKey(pubRaw)

	before := time.Now()
	token, jti, err := Mint("agentbbs", priv, 168*time.Hour)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	p := verifyAsQrypt(t, token, pub)

	if p.Iss != "agentbbs" {
		t.Errorf("iss = %q, want agentbbs", p.Iss)
	}
	if p.Tier != "anonymous" {
		t.Errorf("tier = %q, want anonymous", p.Tier)
	}
	if p.Uses != 1 {
		t.Errorf("uses = %d, want 1", p.Uses)
	}
	if p.JTI != jti {
		t.Errorf("payload jti %q != returned jti %q", p.JTI, jti)
	}
	if len(p.JTI) != 32 {
		t.Errorf("jti len = %d, want 32 hex chars", len(p.JTI))
	}
	if p.Exp <= time.Now().Unix() {
		t.Errorf("exp %d is not in the future", p.Exp)
	}
	if p.Iat < before.Unix()-1 || p.Iat > time.Now().Unix()+1 {
		t.Errorf("iat %d outside the mint window", p.Iat)
	}
	// exp == iat + ttl
	if got, want := p.Exp-p.Iat, int64((168 * time.Hour).Seconds()); got != want {
		t.Errorf("exp-iat = %d, want %d", got, want)
	}
}

func TestJTIUnique(t *testing.T) {
	seedB64, _, err := GenerateIssuerKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ParsePrivateKey(seedB64)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		_, jti, err := Mint("agentbbs", priv, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if seen[jti] {
			t.Fatalf("duplicate jti %q on iteration %d", jti, i)
		}
		seen[jti] = true
	}
}

func TestTamperedTokenFails(t *testing.T) {
	seedB64, pubB64, err := GenerateIssuerKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := ParsePrivateKey(seedB64)
	pubRaw, _ := base64.StdEncoding.DecodeString(pubB64)
	pub := ed25519.PublicKey(pubRaw)

	token, _, err := Mint("agentbbs", priv, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")

	// Tamper with the payload: flip the tier to "verified" and re-encode. The
	// signature was made over the original payload, so verification must fail.
	pj, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var p Payload
	if err := json.Unmarshal(pj, &p); err != nil {
		t.Fatal(err)
	}
	p.Tier = "verified"
	p.Uses = 9999
	tj, _ := json.Marshal(p)
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(tj) + "." + parts[2]

	if got := verifySig(tampered, pub); got {
		t.Fatal("tampered token verified but should have failed")
	}
	// The untouched token still verifies, proving the key is right.
	if !verifySig(token, pub) {
		t.Fatal("original token failed to verify")
	}

	// Tampering with the signature segment must also fail.
	badSig := parts[0] + "." + parts[1] + "." + flipLastChar(parts[2])
	if verifySig(badSig, pub) {
		t.Fatal("token with corrupted signature verified but should have failed")
	}
}

// verifySig is a minimal boolean form of the qrypt verify path.
func verifySig(token string, pub ed25519.PublicKey) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "qci1" {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig)
}

func flipLastChar(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	last := b[len(b)-1]
	if last == 'A' {
		b[len(b)-1] = 'B'
	} else {
		b[len(b)-1] = 'A'
	}
	return string(b)
}

func TestParsePrivateKeyAcceptsSeedAndFull(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	seedB64 := base64.StdEncoding.EncodeToString(priv.Seed())
	fullB64 := base64.StdEncoding.EncodeToString(priv)

	fromSeed, err := ParsePrivateKey(seedB64)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	fromFull, err := ParsePrivateKey(fullB64)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if !fromSeed.Equal(fromFull) {
		t.Fatal("seed and full-key parses produced different keys")
	}
	if !fromSeed.Equal(priv) {
		t.Fatal("parsed key differs from original")
	}

	if _, err := ParsePrivateKey("not-base64-@@@"); err == nil {
		t.Error("expected error on garbage input")
	}
	if _, err := ParsePrivateKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Error("expected error on wrong-length key")
	}
}
