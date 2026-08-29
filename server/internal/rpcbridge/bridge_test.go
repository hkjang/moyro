package rpcbridge

import (
	"strings"
	"testing"
)

func TestTrustedPluginCommandEnvironmentContainsOnlyHandshakeCookie(t *testing.T) {
	for _, name := range []string{
		"POSTGRES_DSN",
		"BOOTSTRAP_ADMIN",
		"BOOTSTRAP_ADMIN_PASSWORD",
		"ENCRYPTION_KEY",
	} {
		t.Setenv(name, "must-not-reach-plugin")
	}

	environment := pluginEnvironment()
	if len(environment) != 1 || environment[0] != MagicCookieKey+"="+MagicCookieValue {
		t.Fatalf("plugin environment = %#v, want handshake cookie only", environment)
	}
	for _, entry := range environment {
		for _, secretName := range []string{
			"POSTGRES_DSN", "BOOTSTRAP_ADMIN", "BOOTSTRAP_ADMIN_PASSWORD", "ENCRYPTION_KEY",
		} {
			if strings.HasPrefix(entry, secretName+"=") {
					t.Fatalf("plugin command environment copied %s", secretName)
			}
		}
	}
}
