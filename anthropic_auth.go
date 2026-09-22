package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const anthropicTokenURL = "https://platform.claude.com/v1/oauth/token"
const anthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// Refreshes are serialized across clients (including clients retired by /reload).
// Re-read the file each time so /login and /logout are observed too.
var anthropicRefreshLock = make(chan struct{}, 1)

type anthropicAuth struct {
	token string
	path  string
	// Kept separate from baseUrl: a Messages API proxy must never receive a
	// subscription refresh token. Tests can substitute a loopback token server.
	tokenURL string
}

type anthropicCredential struct {
	Type    string `json:"type"`
	Key     string `json:"key,omitempty"`
	Access  string `json:"access,omitempty"`
	Refresh string `json:"refresh,omitempty"`
	Expires int64  `json:"expires,omitempty"` // Milliseconds since epoch, compatible with Pi's format.
}

func isAnthropicOAuth(key string) bool { return strings.HasPrefix(key, "sk-ant-oat") }

func anthropicAuthPath() string {
	if path := strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_FILE")); path != "" {
		return expandHome(path)
	}
	home, err := homeDirectory()
	if err != nil {
		return ""
	}
	return authPath(home)
}

func anthropicAuthAvailable() bool {
	if firstNonEmpty(os.Getenv(providerAPIKeyOverride), os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("ANTHROPIC_OAUTH_TOKEN")) != "" {
		return true
	}
	_, credential, err := readAnthropicCredential(anthropicAuthPath())
	return err == nil && (credential.Key != "" || credential.Access != "")
}

func resolveAnthropicAuth(key string, builtin bool) (anthropicAuth, error) {
	auth := anthropicAuth{token: strings.TrimSpace(key), tokenURL: anthropicTokenURL}
	if auth.token == "" && builtin {
		auth.token = strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_TOKEN"))
	}
	if auth.token != "" {
		return auth, validateAnthropicKey(auth.token)
	}
	if !builtin {
		return auth, errors.New("Anthropic-compatible providers need an apiKey in models.json")
	}
	auth.path = anthropicAuthPath()
	if _, _, err := readAnthropicCredential(auth.path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return auth, errors.New("no Anthropic credentials: set ANTHROPIC_API_KEY or ANTHROPIC_OAUTH_TOKEN, or run /login anthropic in unreal")
		}
		return auth, err
	}
	return auth, nil
}

func validateAnthropicKey(key string) error {
	if key == "" {
		return errors.New("Anthropic credential has an empty key or access token")
	}
	for _, char := range key {
		if char <= ' ' || char > '~' {
			return errors.New("Anthropic credential contains invalid header characters")
		}
	}
	return nil
}

func readAnthropicCredential(path string) (map[string]json.RawMessage, anthropicCredential, error) {
	var credential anthropicCredential
	file, err := os.Open(path)
	if err != nil {
		return nil, credential, fmt.Errorf("open Anthropic auth file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, credential, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, credential, errors.New("Anthropic auth file must be a regular file with private permissions (chmod 600)")
	}
	encoded, err := readAnthropicBody(file, 1<<20)
	if err != nil {
		return nil, credential, err
	}
	var entries map[string]json.RawMessage
	if json.Unmarshal(encoded, &entries) != nil {
		return nil, credential, errors.New("Anthropic auth file must contain a JSON object")
	}
	if _, exists := entries["anthropic"]; !exists {
		return nil, credential, fmt.Errorf("no Anthropic credential: %w", fs.ErrNotExist)
	}
	if json.Unmarshal(entries["anthropic"], &credential) != nil {
		return nil, credential, errors.New("Anthropic auth file must contain an anthropic credential")
	}
	var key string
	switch credential.Type {
	case "api_key":
		key = credential.Key
	case "oauth":
		key = credential.Access
		if !isAnthropicOAuth(key) {
			return nil, credential, errors.New("Anthropic OAuth credential must contain an sk-ant-oat access token")
		}
	default:
		return nil, credential, errors.New("Anthropic auth credential type must be oauth or api_key")
	}
	return entries, credential, validateAnthropicKey(key)
}

func (auth anthropicAuth) key(ctx context.Context, client *http.Client) (string, error) {
	if auth.token != "" {
		return auth.token, nil
	}
	select {
	case anthropicRefreshLock <- struct{}{}:
		defer func() { <-anthropicRefreshLock }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, credential, err := readAnthropicCredential(auth.path)
	if err != nil {
		return "", err
	}
	if credential.Type == "api_key" {
		return credential.Key, nil
	}
	if credential.Expires == 0 || time.Now().UnixMilli() < credential.Expires {
		return credential.Access, nil
	}
	if credential.Refresh == "" {
		return "", errors.New("Anthropic OAuth token expired; run /login anthropic again")
	}

	refreshed, err := requestAnthropicToken(ctx, client, auth.tokenURL, map[string]string{
		"grant_type": "refresh_token", "client_id": anthropicOAuthClientID, "refresh_token": credential.Refresh,
	}, "refresh")
	if err != nil {
		return "", err
	}
	if err := saveRefreshedAnthropicCredential(auth.path, credential, refreshed); err != nil {
		return "", err
	}
	return refreshed.Access, nil
}

// Both login and refresh use the same token validation and expiry margin. Auth
// response bodies are never included in errors because they can carry secrets.
func requestAnthropicToken(ctx context.Context, client *http.Client, endpoint string, fields map[string]string, action string) (anthropicCredential, error) {
	var credential anthropicCredential
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(fields)
	if err != nil {
		return credential, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return credential, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return credential, fmt.Errorf("Anthropic OAuth %s: %w", action, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return credential, fmt.Errorf("Anthropic OAuth %s: HTTP %d; run /login anthropic again", action, response.StatusCode)
	}
	encoded, err := readAnthropicBody(response.Body, 1<<20)
	if err != nil {
		return credential, err
	}
	var result struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn int64  `json:"expires_in"`
	}
	if json.Unmarshal(encoded, &result) != nil || !isAnthropicOAuth(result.Access) || validateAnthropicKey(result.Access) != nil || result.Refresh == "" || result.ExpiresIn <= 0 || result.ExpiresIn > 365*24*3600 {
		return credential, fmt.Errorf("Anthropic OAuth %s returned invalid credentials", action)
	}
	lifetime := time.Duration(result.ExpiresIn) * time.Second
	return anthropicCredential{Type: "oauth", Access: result.Access, Refresh: result.Refresh,
		Expires: time.Now().Add(lifetime - min(5*time.Minute, lifetime/10)).UnixMilli()}, nil
}

func saveAnthropicCredential(home string, credential anthropicCredential) error {
	if credential.Type != "oauth" || !isAnthropicOAuth(credential.Access) || validateAnthropicKey(credential.Access) != nil || credential.Refresh == "" || credential.Expires <= 0 {
		return errors.New("invalid Anthropic OAuth credential")
	}
	return updateCredentials(home, func(entries map[string]json.RawMessage) error {
		encoded, err := json.Marshal(credential)
		if err == nil {
			entries["anthropic"] = encoded
		}
		return err
	})
}

func saveRefreshedAnthropicCredential(path string, previous, refreshed anthropicCredential) error {
	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	// Compare-and-swap: a logout or a new login during the network request must
	// not be undone by a late refresh. Preserve unrelated providers and fields.
	entries, latest, err := readAnthropicCredential(path)
	if err != nil {
		return err
	}
	if latest != previous {
		return errors.New("Anthropic credentials changed during refresh; retry the request")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entries["anthropic"], &fields); err != nil {
		return err
	}
	fields["access"], _ = json.Marshal(refreshed.Access)
	fields["refresh"], _ = json.Marshal(refreshed.Refresh)
	fields["expires"], _ = json.Marshal(refreshed.Expires)
	entries["anthropic"], _ = json.Marshal(fields)
	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAnthropicAuth(path, append(encoded, '\n')); err != nil {
		return fmt.Errorf("save refreshed Anthropic credentials: %w", err)
	}
	return nil
}

func writeAnthropicAuth(path string, body []byte) error {
	// Preserve symlinks to a shared credential file.
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".anthropic-auth-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), target)
}
