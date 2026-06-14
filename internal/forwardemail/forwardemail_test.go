package forwardemail

import "testing"

func TestConfiguredAndAddress(t *testing.T) {
	var empty Config
	if empty.Configured() {
		t.Fatal("empty config must not be Configured")
	}
	if (Config{APIKey: "k"}).Configured() {
		t.Fatal("API key without domain must not be Configured")
	}
	c := Config{APIKey: "k", Domain: "bbs.profullstack.com", Webmail: "https://webmail.example"}
	if !c.Configured() {
		t.Fatal("API key + domain should be Configured")
	}
	if got := c.Address("alice"); got != "alice@bbs.profullstack.com" {
		t.Fatalf("Address = %q", got)
	}
	if c.WebmailURL() != "https://webmail.example" {
		t.Fatalf("WebmailURL = %q", c.WebmailURL())
	}
	// Creating an alias without config is a clean error, not a panic.
	if err := empty.CreateAlias("alice", "alice@x.com"); err == nil {
		t.Fatal("CreateAlias on unconfigured must error")
	}
}
