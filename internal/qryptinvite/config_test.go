package qryptinvite

import (
	"strconv"
	"testing"
	"time"
)

func TestConfigFromEnvRejectsNonPositiveTTL(t *testing.T) {
	for _, value := range []string{"0s", "-1h"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AGENTBBS_QRYPT_INVITE_TTL", value)

			if got := ConfigFromEnv().TTL; got != DefaultTTL {
				t.Fatalf("TTL = %s, want default %s", got, DefaultTTL)
			}
		})
	}
}

func TestConfigFromEnvQuotaValidation(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{value: "-1", want: DefaultQuota},
		{value: "0", want: 0},
		{value: "12", want: 12},
	}

	for _, tt := range tests {
		t.Run(strconv.Quote(tt.value), func(t *testing.T) {
			t.Setenv("AGENTBBS_QRYPT_INVITE_QUOTA", tt.value)

			if got := ConfigFromEnv().Quota; got != tt.want {
				t.Fatalf("Quota = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestConfigFromEnvAcceptsPositiveTTL(t *testing.T) {
	t.Setenv("AGENTBBS_QRYPT_INVITE_TTL", "24h")

	if got := ConfigFromEnv().TTL; got != 24*time.Hour {
		t.Fatalf("TTL = %s, want %s", got, 24*time.Hour)
	}
}
