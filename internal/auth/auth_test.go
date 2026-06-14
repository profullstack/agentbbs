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
