package main

import "testing"

func TestEnvIntRequiresPositiveValue(t *testing.T) {
	const key = "AGENTBBS_TEST_POSITIVE_INT"

	for _, value := range []string{"", "invalid", "0", "-1"} {
		t.Setenv(key, value)
		if got := envInt(key, 15); got != 15 {
			t.Errorf("envInt(%q, 15) with %q = %d, want 15", key, value, got)
		}
	}

	t.Setenv(key, "30")
	if got := envInt(key, 15); got != 30 {
		t.Errorf("envInt(%q, 15) = %d, want 30", key, got)
	}
}

func TestValidIRCServerPortRange(t *testing.T) {
	for _, server := range []string{
		"irc.example.com",
		"irc.example.com:1",
		"irc.example.com:6667",
		"irc.example.com:65535",
	} {
		if !validIRCServer(server) {
			t.Errorf("validIRCServer(%q) = false, want true", server)
		}
	}

	for _, server := range []string{
		"irc.example.com:0",
		"irc.example.com:65536",
		"irc.example.com:99999",
	} {
		if validIRCServer(server) {
			t.Errorf("validIRCServer(%q) = true, want false", server)
		}
	}
}
