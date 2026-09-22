package config

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
var CredentialsMu sync.Mutex

type StoredCredential struct {
	Type string `json:"type"`
	Key  string `json:"key,omitempty"`
}

func AuthPath(home string) string {
	return filepath.Join(home, authFileName)
}

// ReadCredentials returns the raw entries of auth.json; a missing file is
// empty. A file readable by other users is still loaded, with a warning.
func ReadCredentials(home string) (map[string]json.RawMessage, string, error) {
	path := AuthPath(home)
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
	if strings.TrimSpace(string(encoded)) != "" {
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

// StoredAPIKeys returns the API keys in auth.json by provider.
func StoredAPIKeys(entries map[string]json.RawMessage) map[string]string {
	keys := make(map[string]string)
	for provider, raw := range entries {
		var credential StoredCredential
		if json.Unmarshal(raw, &credential) == nil && credential.Type == "api_key" && strings.TrimSpace(credential.Key) != "" {
			keys[provider] = credential.Key
		}
	}
	return keys
}

func StoredProviders(home string) ([]string, error) {
	entries, _, err := ReadCredentials(home)
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

func SaveAPIKey(home, provider, key string) error {
	if err := validateAPIKey(key); err != nil {
		return err
	}
	return UpdateCredentials(home, func(entries map[string]json.RawMessage) error {
		encoded, err := json.Marshal(StoredCredential{Type: "api_key", Key: key})
		if err != nil {
			return err
		}
		entries[provider] = encoded
		return nil
	})
}

// RemoveCredential deletes a provider's entry and reports whether it existed.
func RemoveCredential(home, provider string) (bool, error) {
	removed := false
	err := UpdateCredentials(home, func(entries map[string]json.RawMessage) error {
		_, removed = entries[provider]
		delete(entries, provider)
		return nil
	})
	return removed, err
}

func UpdateCredentials(home string, change func(map[string]json.RawMessage) error) error {
	CredentialsMu.Lock()
	defer CredentialsMu.Unlock()
	entries, _, err := ReadCredentials(home)
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
	return WritePrivateFile(AuthPath(home), append(encoded, '\n'))
}

// WritePrivateFile replaces path with body through a private temporary file
// renamed into place, so the file is never briefly readable or half-written.
// A symlinked path (such as a shared credential file) keeps its link.
func WritePrivateFile(path string, body []byte) error {
	target, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		target = path
	} else if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".auth-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), target)
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
