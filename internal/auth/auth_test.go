package auth

import "testing"

func TestIsAdminName(t *testing.T) {
	for _, name := range []string{"admin", "ADMIN", "sysop"} {
		if !IsAdminName(name) {
			t.Errorf("IsAdminName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"bbs", "pod", "anthony", ""} {
		if IsAdminName(name) {
			t.Errorf("IsAdminName(%q) = true, want false", name)
		}
	}
}

func TestAdminsAllowlist(t *testing.T) {
	t.Setenv("AGENTBBS_ADMINS", "anthony, Root  ops")
	admins := Admins()
	for _, want := range []string{"anthony", "root", "ops"} {
		if !admins[want] {
			t.Errorf("expected %q in allowlist, got %v", want, admins)
		}
	}
	if !IsAdmin("ANTHONY") {
		t.Error("IsAdmin should be case-insensitive")
	}
	if IsAdmin("eve") {
		t.Error("eve must not be an admin")
	}
}

func TestAdminsEmpty(t *testing.T) {
	t.Setenv("AGENTBBS_ADMINS", "")
	if len(Admins()) != 0 {
		t.Error("empty env should yield no admins")
	}
	if IsAdmin("anyone") {
		t.Error("nobody is admin when allowlist is empty")
	}
}

func TestSanitizeUsername(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"anthony", "anthony", true},
		{"  Cool_Name 42 ", "cool-name-42", true},
		{"a--b__c", "a-b-c", true},
		{"-Edge--", "edge", true},
		{"MixedCASE", "mixedcase", true},
		{"ab", "ab", false},                                // too short
		{"!!", "", false},                                  // nothing usable
		{"this-name-is-way-too-long-to-accept", "", false}, // >20 after... actually long
		{"admin", "admin", false},                          // reserved (route/infra)
		{"pod", "pod", false},                              // reserved route
		{"video-7f3a", "video-7f3a", false},                // reserved call route
		{"WWW", "www", false},                              // reserved infra label
	}
	for _, c := range cases {
		got, ok := SanitizeUsername(c.in)
		if ok != c.ok {
			t.Errorf("SanitizeUsername(%q) ok=%v, want %v (got name %q)", c.in, ok, c.ok, got)
		}
		// For valid results the cleaned name must match; for invalid ones we
		// only assert the usability flag (the cleaned form is advisory).
		if c.ok && got != c.want {
			t.Errorf("SanitizeUsername(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsReservedName(t *testing.T) {
	for _, n := range []string{"admin", "bbs", "pod", "join", "domain", "agent", "www", "video", "video-abc", "ROOT"} {
		if !IsReservedName(n) {
			t.Errorf("IsReservedName(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"anthony", "cool-name-42", "member-zafztqdk"} {
		if IsReservedName(n) {
			t.Errorf("IsReservedName(%q) = true, want false", n)
		}
	}
}

func TestIsFilesAdminName(t *testing.T) {
	for _, name := range []string{"sftp", "SFTP", "sftpadmin", "filesadmin"} {
		if !IsFilesAdminName(name) {
			t.Errorf("IsFilesAdminName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"files", "bbs", "anthony", ""} {
		if IsFilesAdminName(name) {
			t.Errorf("IsFilesAdminName(%q) = true, want false", name)
		}
	}
}

func TestFilesAdminNamesReserved(t *testing.T) {
	for _, name := range []string{"sftp", "sftpadmin", "filesadmin", "mail"} {
		if !IsReservedName(name) {
			t.Errorf("IsReservedName(%q) = false, want true (route name)", name)
		}
	}
}
