package mailclients

import (
	"os"
	"strings"
	"testing"
)

func testBackend() Backend {
	return Backend{
		Name:          "alice",
		Address:       "alice@bbs.profullstack.com",
		DisplayName:   "alice",
		IMAPHost:      "127.0.0.1",
		IMAPPort:      143,
		IMAPPlaintext: true,
		SMTPHost:      "127.0.0.1",
		SMTPPort:      25,
		SMTPPlaintext: true,
		Login:         "alice@bbs.profullstack.com*gateway",
		Password:      "s3cr3t",
	}
}

func TestHimalayaTemplateRenders(t *testing.T) {
	out := render(Himalaya.template, testBackend().placeholders())
	for _, want := range []string{
		`email = "alice@bbs.profullstack.com"`,
		`backend.host = "127.0.0.1"`,
		`backend.port = 143`,
		`backend.encryption.type = "none"`, // plaintext IMAP
		`backend.login = "alice@bbs.profullstack.com*gateway"`,
		`backend.auth.raw = "s3cr3t"`,
		`message.send.backend.port = 25`,
		`message.send.backend.encryption.type = "none"`, // loopback relay
	} {
		if !strings.Contains(out, want) {
			t.Errorf("himalaya config missing %q\n---\n%s", want, out)
		}
	}
	// No placeholder should survive rendering.
	if strings.Contains(out, "{{") {
		t.Errorf("unrendered placeholder in himalaya config:\n%s", out)
	}
}

func TestMeliTemplateRenders(t *testing.T) {
	out := render(Meli.template, testBackend().placeholders())
	for _, want := range []string{
		`identity = "alice@bbs.profullstack.com"`,
		`server_hostname = "127.0.0.1"`,
		`server_username = "alice@bbs.profullstack.com*gateway"`,
		`server_password = "s3cr3t"`,
		`server_port = 143`,
		`port = 25`,
		`type = "none"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("meli config missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "{{") {
		t.Errorf("unrendered placeholder in meli config:\n%s", out)
	}
}

func TestEncryptionSwitchesWithTLS(t *testing.T) {
	be := testBackend()
	be.IMAPPlaintext = false
	be.SMTPPlaintext = false
	out := render(Himalaya.template, be.placeholders())
	if !strings.Contains(out, `backend.encryption.type = "tls"`) {
		t.Errorf("expected tls IMAP encryption, got:\n%s", out)
	}
	if !strings.Contains(out, `message.send.backend.encryption.type = "start-tls"`) {
		t.Errorf("expected start-tls SMTP encryption, got:\n%s", out)
	}
}

func TestConfigTemplateOverride(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tmpl-*.toml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("custom {{address}}\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	t.Setenv(Himalaya.tmplEnv, f.Name())

	tmpl, err := Himalaya.configTemplate()
	if err != nil {
		t.Fatal(err)
	}
	out := render(tmpl, testBackend().placeholders())
	if out != "custom alice@bbs.profullstack.com\n" {
		t.Errorf("override not used, got %q", out)
	}
}

func TestArgvOverride(t *testing.T) {
	// Default argv places -c <config>.
	got := Meli.argv("/usr/bin/meli", "/cfg/x.toml")
	want := []string{"/usr/bin/meli", "-c", "/cfg/x.toml"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("default argv = %v, want %v", got, want)
	}

	// Override with a {{config}} placeholder.
	t.Setenv(Meli.argsEnv, "--config {{config}} envelope list")
	got = Meli.argv("/usr/bin/meli", "/cfg/x.toml")
	want = []string{"/usr/bin/meli", "--config", "/cfg/x.toml", "envelope", "list"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("override argv = %v, want %v", got, want)
	}
}

func TestBinaryEnvOverrideMissing(t *testing.T) {
	t.Setenv(Himalaya.binEnv, "/nonexistent/himalaya-binary")
	if _, err := Himalaya.binary(); err == nil {
		t.Error("expected error for missing binary override")
	}
	if Himalaya.Available() {
		t.Error("Available() should be false when the override path is missing")
	}
}
