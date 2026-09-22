package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/awersli99/unreal-tui/internal/testutil"
)

func TestAPIKeyStore(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := SaveAPIKey(home, "openrouter", "sk-or-1"); err != nil {
		t.Fatal(err)
	}
	// Entries /login does not own are kept.
	testutil.WriteFile(t, AuthPath(home), `{"openrouter": {"type": "api_key", "key": "sk-or-1"}, "other": {"type": "oauth", "access": "x"}}`)
	if err := os.Chmod(AuthPath(home), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveAPIKey(home, "fireworks", "fw-2"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(AuthPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("auth.json permissions = %o, want 600", perm)
	}
	entries, warning, err := ReadCredentials(home)
	if err != nil || warning != "" {
		t.Fatalf("read: %v, %q", err, warning)
	}
	keys := StoredAPIKeys(entries)
	if len(entries) != 3 || keys["openrouter"] != "sk-or-1" || keys["fireworks"] != "fw-2" || keys["other"] != "" {
		t.Fatalf("entries %v, keys %v", entries, keys)
	}

	if removed, err := RemoveCredential(home, "openrouter"); err != nil || !removed {
		t.Fatalf("remove: %v, %v", removed, err)
	}
	if removed, _ := RemoveCredential(home, "openrouter"); removed {
		t.Fatal("second remove reported success")
	}
	if providers, _ := StoredProviders(home); !slices.Equal(providers, []string{"fireworks", "other"}) {
		t.Fatalf("providers = %v", providers)
	}

	for _, bad := range []string{"", "  ", "has space", "tab\there"} {
		if SaveAPIKey(home, "x", bad) == nil {
			t.Errorf("saveAPIKey accepted %q", bad)
		}
	}
}

func TestReadableAuthFileWarns(t *testing.T) {
	home := t.TempDir()
	testutil.WriteFile(t, AuthPath(home), `{}`)
	if err := os.Chmod(AuthPath(home), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, warning, err := ReadCredentials(home); err != nil || !strings.Contains(warning, "chmod 600") {
		t.Fatalf("warning %q, err %v", warning, err)
	}
}
