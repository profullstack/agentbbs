package main

// provision-user registers a member account from an SSH *public* key supplied
// out of band — the bridge that lets external services (e.g. the TronBrowser
// extension store, files.profullstack.com) onboard a publisher without the
// interactive `ssh join@` flow. An AgentBBS account is just (handle + key
// fingerprint), so this fingerprints the key and EnsureUser's it; Files/SFTP
// access is free for every member, so the account can immediately:
//
//	scp dist.crx files@<host>:/public/extensions/<slug>/
//
// Mirrors the other operator subcommands (grant-pod, mint-token, …). Output is
// JSON on stdout so a caller can parse it; errors go to stderr with exit 1.
//
//	agentbbs provision-user --name acme --pubkey "ssh-ed25519 AAAA… acme@dev"
//	agentbbs provision-user --name acme --pubkey-file ./id_ed25519.pub

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/profullstack/agentbbs/internal/auth"
	"github.com/profullstack/agentbbs/internal/store"
)

func provisionUser(st store.Store, args []string) {
	fs := flag.NewFlagSet("provision-user", flag.ExitOnError)
	name := fs.String("name", "", "member handle to create (a-z0-9-, 3-20, not reserved)")
	pubkey := fs.String("pubkey", "", "SSH public key (authorized_keys line)")
	pubkeyFile := fs.String("pubkey-file", "", "read the SSH public key from this file")
	kind := fs.String("kind", string(auth.Member), "account kind: member | agent")
	fs.Parse(args)

	// Normalize with the same rules the hub uses for self-service joins, so
	// store-provisioned handles are indistinguishable from join@ ones.
	handle, ok := auth.SanitizeUsername(*name)
	if !ok {
		fail("invalid --name: needs 3-20 chars of a-z, 0-9, dash and must not be reserved")
	}

	keyText := strings.TrimSpace(*pubkey)
	if keyText == "" && *pubkeyFile != "" {
		b, err := os.ReadFile(*pubkeyFile)
		if err != nil {
			fail("read --pubkey-file: " + err.Error())
		}
		keyText = strings.TrimSpace(string(b))
	}
	if keyText == "" {
		fail("provide --pubkey or --pubkey-file")
	}

	fp, err := auth.FingerprintAuthorizedKey(keyText)
	if err != nil {
		fail("not a valid SSH public key: " + err.Error())
	}

	// If this key already belongs to someone, report that account rather than
	// silently creating a second handle for the same key.
	if existing, ok, err := st.UserByFingerprint(fp); err != nil {
		fail("lookup by fingerprint: " + err.Error())
	} else if ok && existing.Name != handle {
		fail(fmt.Sprintf("this key already belongs to member %q (fp %s)", existing.Name, fp))
	}

	u, err := st.EnsureUser(handle, *kind, fp)
	if err != nil {
		if errors.Is(err, store.ErrKeyMismatch) {
			fail(fmt.Sprintf("handle %q is already registered with a different key", handle))
		}
		fail("ensure user: " + err.Error())
	}

	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"ok":          true,
		"name":        u.Name,
		"kind":        u.Kind,
		"fingerprint": fp,
		"store_id":    u.ID,
	})
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "provision-user: "+msg)
	os.Exit(1)
}
