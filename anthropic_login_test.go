package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func testAnthropicLogin(t *testing.T, tokenURL string) *anthropicLogin {
	t.Helper()
	flow, err := newAnthropicLogin(context.Background(), anthropicLoginConfig{callbackAddress: "127.0.0.1:0", tokenURL: tokenURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flow.cancel)
	return flow
}

func oauthCallback(t *testing.T, flow *anthropicLogin, values url.Values) int {
	t.Helper()
	response, err := http.Get(flow.redirectURI + "?" + values.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func TestAnthropicNativeOAuthLogin(t *testing.T) {
	var exchange map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("exchange request: %s %v", r.Method, r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&exchange); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{"access_token":"sk-ant-oat-native","refresh_token":"native-refresh","expires_in":3600}`)
	}))
	defer server.Close()
	flow := testAnthropicLogin(t, server.URL)
	address, err := url.Parse(flow.url)
	if err != nil {
		t.Fatal(err)
	}
	params := address.Query()
	challenge := sha256.Sum256([]byte(flow.verifier))
	if address.Scheme != "https" || address.Host != "claude.ai" || address.Path != "/oauth/authorize" ||
		params.Get("client_id") != anthropicOAuthClientID || params.Get("response_type") != "code" ||
		params.Get("code_challenge_method") != "S256" || params.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) ||
		params.Get("state") != flow.state || params.Get("redirect_uri") != flow.redirectURI ||
		!strings.Contains(params.Get("scope"), "user:inference") || len(flow.verifier) != 43 || len(flow.state) != 43 {
		t.Fatalf("invalid authorization parameters: %v", params)
	}
	if strings.Contains(flow.url, flow.verifier) {
		t.Fatal("PKCE verifier exposed in authorization URL")
	}
	if status := oauthCallback(t, flow, url.Values{"code": {"wrong"}, "state": {"wrong-state"}}); status != http.StatusBadRequest {
		t.Fatalf("state mismatch status = %d", status)
	}
	if status := oauthCallback(t, flow, url.Values{"state": {flow.state}}); status != http.StatusBadRequest {
		t.Fatalf("missing code status = %d", status)
	}
	if status := oauthCallback(t, flow, url.Values{"code": {"authorization-code"}, "state": {flow.state}}); status != http.StatusOK {
		t.Fatalf("callback status = %d", status)
	}
	if status := oauthCallback(t, flow, url.Values{"code": {"duplicate"}, "state": {flow.state}}); status != http.StatusConflict {
		t.Fatalf("duplicate status = %d", status)
	}
	credential, err := flow.wait()
	if err != nil {
		t.Fatal(err)
	}
	if credential.Type != "oauth" || credential.Access != "sk-ant-oat-native" || credential.Refresh != "native-refresh" || credential.Expires < time.Now().Add(50*time.Minute).UnixMilli() {
		t.Fatalf("credential = %+v", credential)
	}
	if exchange["grant_type"] != "authorization_code" || exchange["client_id"] != anthropicOAuthClientID || exchange["code"] != "authorization-code" || exchange["state"] != flow.state || exchange["code_verifier"] != flow.verifier || exchange["redirect_uri"] != flow.redirectURI {
		t.Fatalf("exchange = %+v", exchange)
	}
	assertAnthropicCallbackClosed(t, flow)
}

func assertAnthropicCallbackClosed(t *testing.T, flow *anthropicLogin) {
	t.Helper()
	address, _ := url.Parse(flow.redirectURI)
	deadline := time.Now().Add(time.Second)
	for {
		connection, err := net.DialTimeout("tcp", address.Host, 50*time.Millisecond)
		if err != nil {
			return
		}
		connection.Close()
		if time.Now().After(deadline) {
			t.Fatal("callback listener was not closed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAnthropicManualAuthorization(t *testing.T) {
	for _, kind := range []string{"url", "fragment", "parameters", "bare"} {
		t.Run(kind, func(t *testing.T) {
			flow := testAnthropicLogin(t, "unused")
			code := "private-code"
			values := url.Values{"code": {code}, "state": {flow.state}}
			input := map[string]string{"url": flow.redirectURI + "?" + values.Encode(), "fragment": code + "#" + flow.state, "parameters": values.Encode(), "bare": code}[kind]
			if err := flow.submit(input); err != nil {
				t.Fatal(err)
			}
			if result := <-flow.result; result.code != code || result.err != nil {
				t.Fatalf("authorization = %+v", result)
			}
			if err := flow.submit(input); err == nil {
				t.Fatal("duplicate authorization accepted after delivery")
			}
		})
	}
	flow := testAnthropicLogin(t, "unused")
	for _, bad := range []string{"", "private-code#wrong-state", flow.redirectURI + "?code=private-code&state=wrong", "code=private-code&state=wrong", "code=private-code", "private-code with spaces"} {
		if err := flow.submit(bad); err == nil || strings.Contains(err.Error(), "private-code") {
			t.Fatalf("bad input accepted or leaked: %v", err)
		}
	}
	if flow.received.Load() {
		t.Fatal("invalid manual input consumed the login attempt")
	}
}

func TestAnthropicOAuthDenialAndCancel(t *testing.T) {
	flow := testAnthropicLogin(t, "must-not-be-contacted")
	if status := oauthCallback(t, flow, url.Values{"error": {"untrusted text"}, "state": {flow.state}}); status != 200 {
		t.Fatalf("denial status = %d", status)
	}
	if _, err := flow.wait(); err == nil || !strings.Contains(err.Error(), "declined") || strings.Contains(err.Error(), "untrusted text") {
		t.Fatalf("denied = %v", err)
	}
	assertAnthropicCallbackClosed(t, flow)
	flow = testAnthropicLogin(t, "must-not-be-contacted")
	flow.cancel()
	if _, err := flow.wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	assertAnthropicCallbackClosed(t, flow)
	if err := flow.submit("code"); !errors.Is(err, context.Canceled) {
		t.Fatalf("submit after cancel = %v", err)
	}
}

func TestAnthropicExchangeFailureAndCancellation(t *testing.T) {
	for _, response := range []struct {
		status int
		body   string
	}{
		{400, `{"error":"secret-code secret-refresh"}`},
		{200, `not json`},
		{200, `{"access_token":"sk-ant-oat-test","refresh_token":"r","expires_in":0}`},
		{200, `{"access_token":"sk-ant-oat-test","expires_in":3600}`},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(response.status)
			fmt.Fprint(w, response.body)
		}))
		flow := testAnthropicLogin(t, server.URL)
		if err := flow.submit("secret-code"); err != nil {
			t.Fatal(err)
		}
		_, err := flow.wait()
		server.Close()
		if err == nil || strings.Contains(err.Error(), "secret-code") || strings.Contains(err.Error(), "secret-refresh") {
			t.Fatalf("exchange failure = %v", err)
		}
	}
	called := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(called)
		<-r.Context().Done()
	}))
	defer server.Close()
	flow := testAnthropicLogin(t, server.URL)
	if err := flow.submit("secret-code"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := flow.wait(); done <- err }()
	<-called
	flow.cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("token exchange did not cancel")
	}
}

func newLoginTestModel(t *testing.T) *model {
	t.Helper()
	isolateProviders(t)
	home := t.TempDir()
	t.Setenv("UNREAL_TUI_HOME", home)
	current := newHarness(t, newFakeClient())
	catalog := loadCatalog(home)
	app := &App{Engine: current.engine, Config: Config{Home: home, Workspace: t.TempDir()}, Catalog: catalog,
		Model: catalog.Lookup("anthropic", defaultAnthropicModel), Thinking: "high"}
	t.Cleanup(app.Close)
	return newModel(app)
}

func nativeTestCredential() anthropicCredential {
	return anthropicCredential{Type: "oauth", Access: "sk-ant-oat-local", Refresh: "local-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}
}

func TestAnthropicLoginUIAndNativeStore(t *testing.T) {
	current := newLoginTestModel(t)
	account, apiKey := false, false
	for _, option := range current.loginOptions("") {
		if option.provider != "anthropic" {
			continue
		}
		account = account || option.method == loginAccount
		apiKey = apiKey || option.method == loginAPIKey
	}
	if !account || !apiKey {
		t.Fatal("Anthropic must offer both account and API key methods")
	}
	current.openLogin("")
	if current.overlay.selector.items[0].Value != loginAccount {
		t.Fatal("account login is missing")
	}
	current.openLoginProviders(loginAccount, "")
	found := false
	for _, item := range current.overlay.selector.items {
		found = found || item.Value.(loginOption).provider == "anthropic"
	}
	if !found {
		t.Fatal("account picker is missing Anthropic")
	}
	flow := testAnthropicLogin(t, "unused")
	// Do not execute the returned command: that would open a real browser.
	current.showAnthropicLogin(flow)
	if current.overlay.prompt.input.EchoMode != textinput.EchoPassword {
		t.Fatal("authorization code is not masked")
	}
	if err := saveAPIKey(current.app.Config.Home, "openrouter", "keep-other-key"); err != nil {
		t.Fatal(err)
	}
	current.Update(anthropicLoginFinishedMsg{flow: flow, credential: nativeTestCredential()})
	if current.overlay != nil || current.anthropicLogin != nil {
		t.Fatal("login prompt did not close")
	}
	entries, warning, err := readCredentials(current.app.Config.Home)
	if err != nil || warning != "" || len(entries) != 2 {
		t.Fatalf("stored credentials: %v, %q, %v", entries, warning, err)
	}
	_, stored, err := readAnthropicCredential(authPath(current.app.Config.Home))
	if err != nil || stored.Refresh != "local-refresh" {
		t.Fatalf("OAuth credential not stored: %+v, %v", stored, err)
	}
	client, _, _ := current.engine.liveModel()
	if actual, ok := client.(*anthropicClient); !ok || actual.auth.path != authPath(current.app.Config.Home) {
		t.Fatalf("new client not applied: %#v", client)
	}
	if len(current.app.Catalog.Models) < 5 {
		t.Fatal("login did not expose Claude models")
	}
	if info, err := os.Stat(authPath(current.app.Config.Home)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("auth permissions: %v, %v", info, err)
	}
	current.openLogout("anthropic")
	entries, _, err = readCredentials(current.app.Config.Home)
	if err != nil || len(entries) != 1 || entries["anthropic"] != nil || entries["openrouter"] == nil {
		t.Fatalf("logout damaged credentials: %v, %v", entries, err)
	}
	client, _, _ = current.engine.liveModel()
	if _, ok := client.(unavailableClient); !ok {
		t.Fatalf("logout retained authenticated client: %T", client)
	}
	if _, err := client.Respond(context.Background(), anthropicTestRequest(), llm.RequestOptions{}); err == nil {
		t.Fatal("logged-out client still works")
	}
}

func TestAnthropicLoginCancelIgnoresLateCompletion(t *testing.T) {
	current := newLoginTestModel(t)
	flow := testAnthropicLogin(t, "unused")
	current.showAnthropicLogin(flow)
	current.Update(tea.KeyMsg{Type: tea.KeyEsc})
	current.Update(anthropicLoginFinishedMsg{flow: flow, credential: nativeTestCredential()})
	if current.overlay != nil || current.anthropicLogin != nil {
		t.Fatal("cancel did not clear login")
	}
	if _, err := os.Stat(authPath(current.app.Config.Home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled login wrote credentials: %v", err)
	}
	assertAnthropicCallbackClosed(t, flow)
	first := testAnthropicLogin(t, "unused")
	second := testAnthropicLogin(t, "unused")
	current.showAnthropicLogin(second)
	current.Update(anthropicLoginFinishedMsg{flow: first, credential: nativeTestCredential()})
	if current.anthropicLogin != second || current.overlay == nil {
		t.Fatal("stale completion replaced current flow")
	}
	current.Update(anthropicLoginFinishedMsg{flow: second, err: context.DeadlineExceeded})
	if current.anthropicLogin != nil || current.overlay != nil {
		t.Fatal("failed login left prompt open")
	}
}

func TestAnthropicNativeCredentialPrecedence(t *testing.T) {
	isolateProviders(t)
	home := t.TempDir()
	t.Setenv("UNREAL_TUI_HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "sk-ant-oat-env")
	t.Setenv(providerAPIKeyOverride, "override-key")
	writeFile(t, filepath.Join(home, modelsFileName), `{"providers":{"anthropic":{"apiKey":"!exit 1"},"proxy":{"api":"anthropic-messages","baseUrl":"http://localhost:1"}}}`)
	writeAnthropicCredential(t, os.Getenv("ANTHROPIC_AUTH_FILE"), `{"anthropic":{"type":"oauth","access":"sk-ant-oat-external","refresh":"external","expires":9999999999999}}`)
	if err := saveAnthropicCredential(home, nativeTestCredential()); err != nil {
		t.Fatal(err)
	}
	spec, _ := loadCatalog(home).Provider("anthropic")
	if !spec.Available() || spec.AuthFile != authPath(home) {
		t.Fatalf("OAuth not discovered: %+v", spec)
	}
	client, err := spec.newClient(1)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	key, err := client.(*anthropicClient).auth.key(context.Background(), http.DefaultClient)
	if err != nil || key != "sk-ant-oat-local" {
		t.Fatalf("native OAuth did not win: %s, %v", key, err)
	}
	proxy, _ := loadCatalog(home).Provider("proxy")
	if proxy.Available() || proxy.AuthFile != "" {
		t.Fatal("custom provider inherited native OAuth")
	}
	if err := saveAPIKey(home, "anthropic", "local-api-key"); err != nil {
		t.Fatal(err)
	}
	spec, _ = loadCatalog(home).Provider("anthropic")
	if spec.AuthFile != "" || spec.StoredKey != "local-api-key" {
		t.Fatalf("API key did not replace OAuth: %+v", spec)
	}
	client, err = spec.newClient(1)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if key, err := client.(*anthropicClient).auth.key(context.Background(), http.DefaultClient); err != nil || key != "local-api-key" {
		t.Fatalf("local key did not win: %s, %v", key, err)
	}
}

func TestAnthropicDefaultAuthPathDoesNotUsePi(t *testing.T) {
	isolateProviders(t)
	home, pi := t.TempDir(), t.TempDir()
	t.Setenv("UNREAL_TUI_HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", pi)
	t.Setenv("ANTHROPIC_AUTH_FILE", "")
	writeAnthropicCredential(t, filepath.Join(pi, "auth.json"), `{"anthropic":{"type":"oauth","access":"sk-ant-oat-pi","refresh":"pi","expires":9999999999999}}`)
	if got := anthropicAuthPath(); got != authPath(home) {
		t.Fatalf("auth path = %s", got)
	}
	if anthropicAuthAvailable() {
		t.Fatal("implicit Pi credentials were used")
	}
	t.Setenv("UNREAL_HARNESS_LLM_PROVIDER", "")
	t.Setenv("UNREAL_HARNESS_LLM_MODEL", "")
	model, _, err := startupModel(loadCatalog(home), Settings{}, options{})
	if err != nil || model.Provider != "anthropic" {
		t.Fatalf("fresh install cannot reach /login: %+v, %v", model, err)
	}
}

func TestAnthropicRefreshCannotUndoLogoutOrLogin(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			home := t.TempDir()
			credential := nativeTestCredential()
			credential.Expires = 1
			if err := saveAnthropicCredential(home, credential); err != nil {
				t.Fatal(err)
			}
			called, proceed := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(called)
				<-proceed
				fmt.Fprint(w, `{"access_token":"sk-ant-oat-late","refresh_token":"late-refresh","expires_in":3600}`)
			}))
			defer server.Close()
			done := make(chan error, 1)
			go func() {
				_, err := (anthropicAuth{path: authPath(home), tokenURL: server.URL}).key(context.Background(), server.Client())
				done <- err
			}()
			<-called
			var err error
			if replacement {
				err = saveAPIKey(home, "anthropic", "new-key")
			} else {
				_, err = removeCredential(home, "anthropic")
			}
			if err != nil {
				close(proceed)
				t.Fatal(err)
			}
			if err := saveAPIKey(home, "other", "other-key"); err != nil {
				close(proceed)
				t.Fatal(err)
			}
			close(proceed)
			if err := <-done; err == nil {
				t.Fatal("late refresh was saved")
			}
			entries, _, err := readCredentials(home)
			if err != nil {
				t.Fatal(err)
			}
			if storedAPIKeys(entries)["other"] != "other-key" {
				t.Fatal("refresh lost another provider")
			}
			if replacement && storedAPIKeys(entries)["anthropic"] != "new-key" {
				t.Fatal("refresh undid login")
			}
			if !replacement && entries["anthropic"] != nil {
				t.Fatal("refresh resurrected logged-out credentials")
			}
		})
	}
}

func TestAnthropicCallbackPortCanBeReusedAfterCancel(t *testing.T) {
	flow := testAnthropicLogin(t, "unused")
	redirect, _ := url.Parse(flow.redirectURI)
	address := net.JoinHostPort("127.0.0.1", redirect.Port())
	if _, err := newAnthropicLogin(context.Background(), anthropicLoginConfig{callbackAddress: address}); err == nil {
		t.Fatal("two flows bound the same callback port")
	}
	flow.cancel()
	next, err := newAnthropicLogin(context.Background(), anthropicLoginConfig{callbackAddress: address})
	if err != nil {
		t.Fatalf("callback port not immediately reusable: %v", err)
	}
	next.cancel()
}

func TestAnthropicCallbackParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	flow, err := newAnthropicLogin(ctx, anthropicLoginConfig{callbackAddress: "127.0.0.1:0"})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer flow.cancel()
	cancel()
	if _, err := flow.wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation: %v", err)
	}
	assertAnthropicCallbackClosed(t, flow)
}
