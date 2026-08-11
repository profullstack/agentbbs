package main

import (
	"testing"

	"github.com/profullstack/agentbbs/internal/auth"
)

func TestParseProvisionKind(t *testing.T) {
	tests := []struct {
		input string
		want  auth.Kind
		ok    bool
	}{
		{input: "member", want: auth.Member, ok: true},
		{input: " Agent ", want: auth.Agent, ok: true},
		{input: "guest", ok: false},
		{input: "admin", ok: false},
		{input: "", ok: false},
	}

	for _, test := range tests {
		got, ok := parseProvisionKind(test.input)
		if got != test.want || ok != test.ok {
			t.Errorf("parseProvisionKind(%q) = (%q, %t), want (%q, %t)", test.input, got, ok, test.want, test.ok)
		}
	}
}
