package pilot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	defaultMaxRequestBytes  = 16 * 1024
	defaultMaxResponseBytes = 32 * 1024
	defaultMaxStatuses      = 100
)

var validStates = map[string]bool{"healthy": true, "degraded": true, "outage": true, "unknown": true}

type publishRequest struct {
	Subject    string `json:"subject"`
	Audience   string `json:"audience"`
	State      string `json:"state"`
	Summary    string `json:"summary"`
	ObservedAt string `json:"observed_at"`
	ExpiresAt  string `json:"expires_at"`
	Revision   int64  `json:"revision"`
}

type queryRequest struct {
	Audience string `json:"audience"`
	Subject  string `json:"subject,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func readJSONRequest(r io.Reader, maxBytes int, dst any) error {
	if maxBytes <= 0 {
		return errors.New("request size limit is invalid")
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	if len(b) > maxBytes {
		return errors.New("request exceeds size limit")
	}
	if bytes.Contains(b, []byte{'\r'}) {
		return errors.New("CRLF is not accepted")
	}
	if n := bytes.Count(b, []byte{'\n'}); n > 1 || (n == 1 && b[len(b)-1] != '\n') {
		return errors.New("request must be one JSON object")
	}
	b = bytes.TrimSuffix(b, []byte{'\n'})
	if len(bytes.TrimSpace(b)) == 0 {
		return errors.New("request is empty")
	}
	if err := rejectDuplicateKeys(b); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request must contain one JSON object")
		}
		return fmt.Errorf("trailing request data: %w", err)
	}
	return nil
}

// rejectDuplicateKeys walks the bounded JSON value before unmarshalling. The
// standard library accepts duplicate object keys, which is unsafe for a policy
// request because different consumers can choose different winners.
func rejectDuplicateKeys(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := scanJSONValue(dec, 0); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request contains multiple JSON values")
		}
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	return nil
}

func scanJSONValue(dec *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting exceeds limit")
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return fmt.Errorf("invalid JSON object: %w", err)
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				if seen[name] {
					return fmt.Errorf("duplicate JSON field %q", name)
				}
				seen[name] = true
				if err := scanJSONValue(dec, depth+1); err != nil {
					return err
				}
			}
			_, err = dec.Token()
		case '[':
			for dec.More() {
				if err := scanJSONValue(dec, depth+1); err != nil {
					return err
				}
			}
			_, err = dec.Token()
		}
		if err != nil {
			return fmt.Errorf("invalid JSON container: %w", err)
		}
	}
	return nil
}

func validateString(name, value string, max int) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > max {
		return fmt.Errorf("%s exceeds %d bytes", name, max)
	}
	if strings.IndexByte(value, '\r') >= 0 || strings.IndexByte(value, '\n') >= 0 {
		return fmt.Errorf("%s contains a line break", name)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

func parseAndValidatePublish(req publishRequest, identity KeyEntry, now time.Time, cfg Config) (Status, error) {
	if req.Subject != identity.Name {
		return Status{}, errors.New("subject must match the authenticated identity")
	}
	if err := validateString("subject", req.Subject, 32); err != nil {
		return Status{}, err
	}
	if err := validateString("audience", req.Audience, 32); err != nil {
		return Status{}, err
	}
	if err := validateString("state", req.State, 16); err != nil {
		return Status{}, err
	}
	if !validStates[req.State] {
		return Status{}, fmt.Errorf("invalid state %q", req.State)
	}
	if req.Summary != "" {
		if err := validateString("summary", req.Summary, 1024); err != nil {
			return Status{}, err
		}
	}
	if req.Revision <= 0 {
		return Status{}, errors.New("revision must be positive")
	}
	observed, err := time.Parse(time.RFC3339, req.ObservedAt)
	if err != nil {
		return Status{}, errors.New("observed_at must be RFC3339")
	}
	expires, err := time.Parse(time.RFC3339, req.ExpiresAt)
	if err != nil {
		return Status{}, errors.New("expires_at must be RFC3339")
	}
	observed = observed.UTC()
	expires = expires.UTC()
	if observed.After(now.Add(cfg.MaxFutureSkew)) {
		return Status{}, errors.New("observed_at is too far in the future")
	}
	if now.Sub(observed) > cfg.MaxAge {
		return Status{}, errors.New("observed_at is stale")
	}
	if !expires.After(now) {
		return Status{}, errors.New("expires_at must be in the future")
	}
	if !expires.After(observed) || expires.Sub(observed) > cfg.MaxTTL {
		return Status{}, errors.New("expires_at is outside the allowed freshness window")
	}
	return Status{Subject: req.Subject, Audience: req.Audience, State: req.State, Summary: req.Summary, ObservedAt: observed, ExpiresAt: expires, Revision: req.Revision}, nil
}

func validateQuery(req queryRequest, cfg Config) error {
	if err := validateString("audience", req.Audience, 32); err != nil {
		return err
	}
	if req.Subject != "" {
		if err := validateString("subject", req.Subject, 32); err != nil {
			return err
		}
	}
	limit := req.Limit
	if limit == 0 {
		limit = min(cfg.MaxStatuses, defaultMaxStatuses)
	}
	if limit < 1 || limit > cfg.MaxStatuses || limit > defaultMaxStatuses {
		return fmt.Errorf("limit must be between 1 and %d", min(cfg.MaxStatuses, defaultMaxStatuses))
	}
	return nil
}

func responseWithinLimit(v any, maxBytes int) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > maxBytes {
		return nil, errors.New("response exceeds size limit")
	}
	return append(b, '\n'), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
