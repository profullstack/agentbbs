package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	qi "github.com/profullstack/agentbbs/internal/qryptinvite"
	"github.com/profullstack/agentbbs/internal/store"
)

// qryptInviteCmd is the ops side of qrypt.chat invites:
// `agentbbs qrypt-invite <user>` mints a single-use anonymous invite on behalf
// of an existing member, respecting their per-account quota, and prints the
// token + redeem URL (docs/qrypt-invites.md).
func qryptInviteCmd(st store.Store, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: agentbbs qrypt-invite <username>")
		os.Exit(2)
	}
	name := strings.ToLower(args[0])
	if _, found, err := st.UserByName(name); err != nil {
		fmt.Fprintln(os.Stderr, "lookup:", err)
		os.Exit(1)
	} else if !found {
		fmt.Fprintf(os.Stderr, "no such account: %s (register via ssh join@)\n", name)
		os.Exit(1)
	}

	cfg := qi.ConfigFromEnv()
	priv, err := cfg.PrivateKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	token, jti, err := qi.Mint(cfg.IssuerID, priv, cfg.TTL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mint:", err)
		os.Exit(1)
	}
	if err := st.RecordQryptInvite(name, jti, cfg.Quota); err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			used, _ := st.QryptInviteCount(name)
			fmt.Fprintf(os.Stderr, "%s is at their invite quota (%d/%d)\n", name, used, cfg.Quota)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "record:", err)
		os.Exit(1)
	}

	used, _ := st.QryptInviteCount(name)
	remaining := cfg.Quota - used
	if remaining < 0 {
		remaining = 0
	}
	fmt.Printf("qrypt.chat invite for %s (issuer %s, expires in %s, single-use):\n\n", name, cfg.IssuerID, cfg.TTL)
	fmt.Printf("  redeem  %s\n", cfg.RedeemURLFor(token))
	fmt.Printf("  token   %s\n", token)
	fmt.Printf("  jti     %s\n\n", jti)
	if cfg.Quota > 0 {
		fmt.Printf("invites left for %s: %d/%d\n", name, remaining, cfg.Quota)
	}
}

// qryptIssuerKeygen prints a fresh Ed25519 issuer keypair for first-time setup:
// the base64 seed (private — goes in AGENTBBS_QRYPT_ISSUER_KEY) and the base64
// raw public key (goes in qrypt.chat's invite_issuers row).
func qryptIssuerKeygen() {
	seed, pub, err := qi.GenerateIssuerKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	cfg := qi.ConfigFromEnv()
	fmt.Println("Fresh qrypt.chat invite-issuer keypair:")
	fmt.Println()
	fmt.Println("  1) On agentbbs, set the PRIVATE seed (keep it secret):")
	fmt.Printf("       AGENTBBS_QRYPT_ISSUER_KEY=%s\n\n", seed)
	fmt.Println("  2) In qrypt.chat, insert an invite_issuers row with the PUBLIC key:")
	fmt.Printf("       id                 %s\n", cfg.IssuerID)
	fmt.Printf("       ed25519_public_key %s\n\n", pub)
	fmt.Printf("     INSERT INTO invite_issuers (id, name, ed25519_public_key)\n")
	fmt.Printf("       VALUES ('%s', 'AgentBBS', '%s');\n", cfg.IssuerID, pub)
}
