// Package pilot contains the isolated, internal-only operational-status pilot.
// It deliberately does not share the normal AgentBBS account or feature paths.
package pilot

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

const maxRegistryBytes = 64 * 1024

var (
	namePattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	audiencePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
)

// KeyEntry is one pre-approved identity. A key is unique across the entire
// registry, including revoked entries, so reusing a fingerprint is rejected.
type KeyEntry struct {
	Name             string   `json:"name"`
	Fingerprint      string   `json:"fingerprint"`
	PublishAudiences []string `json:"publish_audiences"`
	QueryAudiences   []string `json:"query_audiences"`
	Revoked          bool     `json:"revoked"`
}

type registryFile struct {
	Keys []KeyEntry `json:"keys"`
}

// Registry is an immutable validated snapshot of the approved key registry.
type Registry struct {
	byName map[string]KeyEntry
	byFP   map[string]KeyEntry
}

// LoadRegistry reads and validates a registry file. Unknown JSON fields are
// rejected so an operator typo cannot silently widen or change policy.
func LoadRegistry(path string) (Registry, error) {
	if err := validatePrivateFile(path); err != nil {
		return Registry{}, fmt.Errorf("pilot key registry: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return Registry{}, fmt.Errorf("read pilot key registry: %w", err)
	}
	b, readErr := io.ReadAll(io.LimitReader(file, int64(maxRegistryBytes)+1))
	closeErr := file.Close()
	if readErr != nil {
		return Registry{}, fmt.Errorf("read pilot key registry: %w", readErr)
	}
	if closeErr != nil {
		return Registry{}, fmt.Errorf("close pilot key registry: %w", closeErr)
	}
	if len(b) > maxRegistryBytes {
		return Registry{}, errors.New("pilot key registry exceeds size limit")
	}
	if err := rejectDuplicateKeys(b); err != nil {
		return Registry{}, fmt.Errorf("decode pilot key registry: %w", err)
	}
	var f registryFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Registry{}, fmt.Errorf("decode pilot key registry: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return Registry{}, errors.New("pilot key registry must contain one JSON object")
	} else if !errors.Is(err, io.EOF) {
		return Registry{}, fmt.Errorf("decode pilot key registry: %w", err)
	}
	if len(f.Keys) == 0 {
		return Registry{}, errors.New("pilot key registry must contain at least one key")
	}
	r := Registry{byName: make(map[string]KeyEntry, len(f.Keys)), byFP: make(map[string]KeyEntry, len(f.Keys))}
	for i, entry := range f.Keys {
		if err := validateEntry(entry); err != nil {
			return Registry{}, fmt.Errorf("pilot key registry entry %d: %w", i, err)
		}
		if _, exists := r.byName[entry.Name]; exists {
			return Registry{}, fmt.Errorf("duplicate pilot identity name %q", entry.Name)
		}
		if _, exists := r.byFP[entry.Fingerprint]; exists {
			return Registry{}, fmt.Errorf("duplicate pilot key fingerprint %q", entry.Fingerprint)
		}
		r.byName[entry.Name] = entry
		r.byFP[entry.Fingerprint] = entry
	}
	return r, nil
}

func validateEntry(entry KeyEntry) error {
	if !namePattern.MatchString(entry.Name) {
		return fmt.Errorf("invalid name %q", entry.Name)
	}
	if !fingerprintPattern.MatchString(entry.Fingerprint) {
		return fmt.Errorf("invalid SHA256 fingerprint %q", entry.Fingerprint)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(entry.Fingerprint, "SHA256:"))
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("invalid SHA256 fingerprint %q", entry.Fingerprint)
	}
	if err := validateAudiences(entry.PublishAudiences); err != nil {
		return fmt.Errorf("publish audiences: %w", err)
	}
	if err := validateAudiences(entry.QueryAudiences); err != nil {
		return fmt.Errorf("query audiences: %w", err)
	}
	return nil
}

func validatePrivateFile(path string) error {
	if path == "" {
		return errors.New("path is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("symlink is not allowed")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("file permissions %04o are too broad", info.Mode().Perm())
	}
	return nil
}

func validateAudiences(audiences []string) error {
	seen := make(map[string]bool, len(audiences))
	for _, audience := range audiences {
		if !audiencePattern.MatchString(audience) {
			return fmt.Errorf("invalid audience %q", audience)
		}
		if seen[audience] {
			return fmt.Errorf("duplicate audience %q", audience)
		}
		seen[audience] = true
	}
	return nil
}

// Authorize returns the non-revoked identity for an exact username/fingerprint
// pair. The username is never inferred from caller input after this check.
func (r Registry) Authorize(name, fingerprint string) (KeyEntry, bool) {
	entry, ok := r.byName[name]
	if !ok || entry.Revoked || entry.Fingerprint != fingerprint {
		return KeyEntry{}, false
	}
	return entry, true
}

func (r Registry) entryByFingerprint(fingerprint string) (KeyEntry, bool) {
	entry, ok := r.byFP[fingerprint]
	if !ok || entry.Revoked {
		return KeyEntry{}, false
	}
	return entry, true
}

func containsAudience(audiences []string, audience string) bool {
	for _, candidate := range audiences {
		if candidate == audience {
			return true
		}
	}
	return false
}

func (r Registry) canPublish(name, fingerprint, audience string) bool {
	entry, ok := r.Authorize(name, fingerprint)
	return ok && containsAudience(entry.PublishAudiences, audience)
}

func (r Registry) canQuery(name, fingerprint, audience string) bool {
	entry, ok := r.Authorize(name, fingerprint)
	return ok && containsAudience(entry.QueryAudiences, audience)
}

// Audiences returns all audiences present in the registry in stable order.
func (r Registry) Audiences() []string {
	seen := map[string]bool{}
	for _, entry := range r.byName {
		for _, audience := range append(append([]string{}, entry.PublishAudiences...), entry.QueryAudiences...) {
			seen[audience] = true
		}
	}
	out := make([]string, 0, len(seen))
	for audience := range seen {
		out = append(out, audience)
	}
	sort.Strings(out)
	return out
}
