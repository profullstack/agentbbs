// Package auth resolves SSH connections into AgentBBS identities (PRD §4.4).
package auth

import (
	"os"
	"strings"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Kind classifies an identity.
type Kind string

const (
	Guest  Kind = "guest"
	Member Kind = "member"
	Agent  Kind = "agent"
)

// User is the resolved identity for one session.
type User struct {
	Name     string
	Kind     Kind
	PubKeyFP string // SHA256 fingerprint, empty for guests without a key
	StoreID  int64  // 0 for guests
}

// GuestNames are usernames that always map to an anonymous guest hub session.
var GuestNames = map[string]bool{"bbs": true, "play": true, "guest": true}

// PodNames are usernames that route to a personal pod instead of the hub.
// Pod access requires an active paid membership (PRD pods addendum).
var PodNames = map[string]bool{"pod": true}

// JoinNames are usernames that trigger the onboarding flow: register the
// visitor's public key, print instructions, and disconnect.
var JoinNames = map[string]bool{"join": true, "signup": true, "register": true}

// DomainNames are usernames that route to the custom-domain self-service flow:
// list/add/remove the domains pointed at a member's homepage.
var DomainNames = map[string]bool{"domain": true, "domains": true}

// AdminNames are usernames that route to the privileged admin console (PRD §6).
// The route only opens for accounts whose name is in the operator allowlist
// (see IsAdmin); the name itself confers nothing.
var AdminNames = map[string]bool{"admin": true, "sysop": true}

// TorURLNames route to the one-shot "fetch a URL over Tor" command (premium).
var TorURLNames = map[string]bool{"tor-url": true}

// TorIRCNames route to an interactive IRC-over-Tor client in the member's pod.
var TorIRCNames = map[string]bool{"tor-irc": true}

// TorNames route to the generic "run a command over Tor" passthrough in the
// member's pod (premium). Checked after the more specific tor-* routes.
var TorNames = map[string]bool{"tor": true}

// IRCNames route a member straight into the BBS's own (members-only) IRC
// network via an in-process client. Distinct from tor-irc@, which is a client
// for connecting OUT to remote IRC servers over Tor.
var IRCNames = map[string]bool{"irc": true}

// GameNames are usernames that route to AgentGames: the line-delimited-JSON
// agent-vs-agent match protocol (PRD §5.2). `play@` stays a guest hub alias.
var GameNames = map[string]bool{"game": true, "games": true}

// IsGuestName reports whether the SSH username requests anonymous hub access.
func IsGuestName(u string) bool { return GuestNames[strings.ToLower(u)] }

// IsPodName reports whether the SSH username requests the pod route.
func IsPodName(u string) bool { return PodNames[strings.ToLower(u)] }

// IsJoinName reports whether the SSH username requests onboarding.
func IsJoinName(u string) bool { return JoinNames[strings.ToLower(u)] }

// IsDomainName reports whether the SSH username requests the custom-domain flow.
func IsDomainName(u string) bool { return DomainNames[strings.ToLower(u)] }

// IsAdminName reports whether the SSH username requests the admin console.
func IsAdminName(u string) bool { return AdminNames[strings.ToLower(u)] }

// IsTorURLName reports whether the SSH username requests the tor-url fetch.
func IsTorURLName(u string) bool { return TorURLNames[strings.ToLower(u)] }

// IsTorIRCName reports whether the SSH username requests the tor-irc client.
func IsTorIRCName(u string) bool { return TorIRCNames[strings.ToLower(u)] }

// IsTorName reports whether the SSH username requests the generic tor passthrough.
func IsTorName(u string) bool { return TorNames[strings.ToLower(u)] }

// IsIRCName reports whether the SSH username requests the in-BBS IRC client.
func IsIRCName(u string) bool { return IRCNames[strings.ToLower(u)] }

// systemReserved are names that don't drive an SSH route but would still
// collide with a per-user subdomain (<name>.<host>), the agent route, or common
// infra hostnames — so members may not claim them as account names.
var systemReserved = map[string]bool{
	"agent": true, "video": true, "www": true, "api": true, "mail": true,
	"smtp": true, "imap": true, "ftp": true, "ns": true, "ns1": true, "ns2": true,
	"cdn": true, "static": true, "assets": true, "root": true, "abuse": true,
	"postmaster": true, "webmaster": true, "support": true, "help": true,
	"admin": true, "sysop": true, "bbs": true, "guest": true, "pod": true,
}

// IsReservedName reports whether name is claimed by a route or infra label and
// therefore cannot be used as a member's account name.
func IsReservedName(name string) bool {
	n := strings.ToLower(name)
	if GuestNames[n] || PodNames[n] || JoinNames[n] || DomainNames[n] || AdminNames[n] ||
		TorURLNames[n] || TorIRCNames[n] || TorNames[n] || IRCNames[n] || systemReserved[n] {
		return true
	}
	return strings.HasPrefix(n, "video-") // video-<code> call routes
}

// SanitizeUsername normalizes a requested account name to the charset the hub
// and per-user subdomains allow: lowercased [a-z0-9-], 3–20 chars, with '_' and
// spaces folded to '-', no doubled, leading, or trailing dashes. It returns the
// cleaned name and whether it is usable (right length and not reserved).
func SanitizeUsername(raw string) (string, bool) {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == ' ':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) < 3 || len(name) > 20 || IsReservedName(name) {
		return name, false
	}
	return name, true
}

// IsGameName reports whether the SSH username requests the AgentGames protocol.
func IsGameName(u string) bool { return GameNames[strings.ToLower(u)] }

// Admins returns the operator-configured admin allowlist: the lowercased,
// comma/space-separated account names in $AGENTBBS_ADMINS. Admin status can
// only be granted by the operator (via env), never self-assigned in-band.
func Admins() map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(os.Getenv("AGENTBBS_ADMINS"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		out[strings.ToLower(f)] = true
	}
	return out
}

// IsAdmin reports whether the account name is in the operator allowlist.
func IsAdmin(name string) bool { return Admins()[strings.ToLower(name)] }

// KindFor infers the identity kind from a (non-guest) username.
// Usernames prefixed "agent-" are automated clients (PRD §3).
func KindFor(username string) Kind {
	if strings.HasPrefix(strings.ToLower(username), "agent-") {
		return Agent
	}
	return Member
}

// Fingerprint returns the SHA256 fingerprint for a session public key, or "".
func Fingerprint(key ssh.PublicKey) string {
	if key == nil {
		return ""
	}
	return gossh.FingerprintSHA256(key)
}
