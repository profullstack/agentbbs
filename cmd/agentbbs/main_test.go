package main

import "testing"

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
