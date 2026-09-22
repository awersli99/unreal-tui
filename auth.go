package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// auth.json stores credentials saved by /login, in pi's format:
// {"openai": {"type": "api_key", "key": "sk-..."}}. Anthropic account login
// stores an OAuth entry; unknown providers and fields are preserved.
const authFileName = "auth.json"

// Serialize local read-modify-write operations, including OAuth refresh CAS.
// Network requests never hold this lock, so /login and /logout stay responsive.
var credentialsMu sync.Mutex

type storedCredential struct {
	Type string `json:"type"`
	Key  string `json:"key,omitempty"`
}

func authPath(home string) string {
	return filepath.Join(home, authFileName)
}

// readCredentials returns the raw entries of auth.json; a missing file is
// empty. A file readable by other users is still loaded, with a warning.
func readCredentials(home string) (map[string]json.RawMessage, string, error) {
	path := authPath(home)
	entries := make(map[string]json.RawMessage)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return entries, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if len(strings.TrimSpace(string(encoded))) != 0 {
		if err := json.Unmarshal(encoded, &entries); err != nil {
			return nil, "", fmt.Errorf("parse %s: %w", path, err)
		}
	}
	warning := ""
	if info.Mode().Perm()&0o077 != 0 {
		warning = fmt.Sprintf("%s is readable by other users; run chmod 600 %s", path, path)
	}
	return entries, warning, nil
}

// storedAPIKeys returns the API keys in auth.json by provider.
func storedAPIKeys(entries map[string]json.RawMessage) map[string]string {
	keys := make(map[string]string)
	for provider, raw := range entries {
		var credential storedCredential
		if json.Unmarshal(raw, &credential) == nil && credential.Type == "api_key" && strings.TrimSpace(credential.Key) != "" {
			keys[provider] = credential.Key
		}
	}
	return keys
}

func storedProviders(home string) ([]string, error) {
	entries, _, err := readCredentials(home)
	if err != nil {
		return nil, err
	}
	providers := make([]string, 0, len(entries))
	for provider := range entries {
		providers = append(providers, provider)
	}
	slices.Sort(providers)
	return providers, nil
}

func saveAPIKey(home, provider, key string) error {
	if err := validateAPIKey(key); err != nil {
		return err
	}
	return updateCredentials(home, func(entries map[string]json.RawMessage) error {
		encoded, err := json.Marshal(storedCredential{Type: "api_key", Key: key})
		if err != nil {
			return err
		}
		entries[provider] = encoded
		return nil
	})
}

// removeCredential deletes a provider's entry and reports whether it existed.
func removeCredential(home, provider string) (bool, error) {
	removed := false
	err := updateCredentials(home, func(entries map[string]json.RawMessage) error {
		_, removed = entries[provider]
		delete(entries, provider)
		return nil
	})
	return removed, err
}

func updateCredentials(home string, change func(map[string]json.RawMessage) error) error {
	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	entries, _, err := readCredentials(home)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = make(map[string]json.RawMessage)
	}
	if err := change(entries); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	// Write a private temporary file and rename it into place, so the file is
	// never briefly readable or half-written.
	file, err := os.CreateTemp(home, ".auth-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(encoded, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), authPath(home))
}

func validateAPIKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("the API key is empty")
	}
	for _, char := range key {
		if char <= ' ' || char > '~' {
			return errors.New("the API key contains spaces or control characters")
		}
	}
	return nil
}
