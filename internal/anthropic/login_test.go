package anthropic

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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/testutil"
)

func testAnthropicLogin(t *testing.T, tokenURL string) *Login {
	t.Helper()
	flow, err := NewLogin(context.Background(), LoginConfig{CallbackAddress: "127.0.0.1:0", TokenURL: tokenURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flow.cancel)
	return flow
}

func oauthCallback(t *testing.T, flow *Login, values url.Values) int {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, flow.redirectURI+"?"+values.Encode(), http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func TestAnthropicNativeOAuthLogin(t *testing.T) {
	var exchange map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
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
	credential, err := flow.Wait()
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

func assertAnthropicCallbackClosed(t *testing.T, flow *Login) {
	t.Helper()
	address, _ := url.Parse(flow.redirectURI)
	deadline := time.Now().Add(time.Second)
	for {
		connection, err := (&net.Dialer{Timeout: 50 * time.Millisecond}).DialContext(t.Context(), "tcp", address.Host)
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
			if err := flow.Submit(input); err != nil {
				t.Fatal(err)
			}
			if result := <-flow.result; result.code != code || result.err != nil {
				t.Fatalf("authorization = %+v", result)
			}
			if err := flow.Submit(input); err == nil {
				t.Fatal("duplicate authorization accepted after delivery")
			}
		})
	}
	flow := testAnthropicLogin(t, "unused")
	for _, bad := range []string{"", "private-code#wrong-state", flow.redirectURI + "?code=private-code&state=wrong", "code=private-code&state=wrong", "code=private-code", "private-code with spaces"} {
		if err := flow.Submit(bad); err == nil || strings.Contains(err.Error(), "private-code") {
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
	if _, err := flow.Wait(); err == nil || !strings.Contains(err.Error(), "declined") || strings.Contains(err.Error(), "untrusted text") {
		t.Fatalf("denied = %v", err)
	}
	assertAnthropicCallbackClosed(t, flow)
	flow = testAnthropicLogin(t, "must-not-be-contacted")
	flow.cancel()
	if _, err := flow.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	assertAnthropicCallbackClosed(t, flow)
	if err := flow.Submit("code"); !errors.Is(err, context.Canceled) {
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
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(response.status)
			fmt.Fprint(w, response.body)
		}))
		flow := testAnthropicLogin(t, server.URL)
		if err := flow.Submit("secret-code"); err != nil {
			t.Fatal(err)
		}
		_, err := flow.Wait()
		server.Close()
		if err == nil || strings.Contains(err.Error(), "secret-code") || strings.Contains(err.Error(), "secret-refresh") {
			t.Fatalf("exchange failure = %v", err)
		}
	}
	called := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(called)
		<-r.Context().Done()
	}))
	defer server.Close()
	flow := testAnthropicLogin(t, server.URL)
	if err := flow.Submit("secret-code"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := flow.Wait(); done <- err }()
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

func nativeTestCredential() Credential {
	return Credential{Type: "oauth", Access: "sk-ant-oat-local", Refresh: "local-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}
}

func TestAnthropicDefaultAuthPathDoesNotUsePi(t *testing.T) {
	testutil.IsolateProviders(t)
	home, pi := t.TempDir(), t.TempDir()
	t.Setenv("UNREAL_TUI_HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", pi)
	t.Setenv("ANTHROPIC_AUTH_FILE", "")
	testutil.WritePrivateFile(t, filepath.Join(pi, "auth.json"), `{"anthropic":{"type":"oauth","access":"sk-ant-oat-pi","refresh":"pi","expires":9999999999999}}`)
	if got := anthropicAuthPath(); got != config.AuthPath(home) {
		t.Fatalf("auth path = %s", got)
	}
	if AuthAvailable() {
		t.Fatal("implicit Pi credentials were used")
	}
}

func TestAnthropicRefreshCannotUndoLogoutOrLogin(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprint(replacement), func(t *testing.T) {
			home := t.TempDir()
			credential := nativeTestCredential()
			credential.Expires = 1
			if err := SaveCredential(home, credential); err != nil {
				t.Fatal(err)
			}
			called, proceed := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(called)
				<-proceed
				fmt.Fprint(w, `{"access_token":"sk-ant-oat-late","refresh_token":"late-refresh","expires_in":3600}`)
			}))
			defer server.Close()
			done := make(chan error, 1)
			go func() {
				_, err := (anthropicAuth{path: config.AuthPath(home), tokenURL: server.URL}).key(context.Background(), server.Client())
				done <- err
			}()
			<-called
			var err error
			if replacement {
				err = config.SaveAPIKey(home, "anthropic", "new-key")
			} else {
				_, err = config.RemoveCredential(home, "anthropic")
			}
			if err != nil {
				close(proceed)
				t.Fatal(err)
			}
			if err := config.SaveAPIKey(home, "other", "other-key"); err != nil {
				close(proceed)
				t.Fatal(err)
			}
			close(proceed)
			if err := <-done; err == nil {
				t.Fatal("late refresh was saved")
			}
			entries, _, err := config.ReadCredentials(home)
			if err != nil {
				t.Fatal(err)
			}
			if config.StoredAPIKeys(entries)["other"] != "other-key" {
				t.Fatal("refresh lost another provider")
			}
			if replacement && config.StoredAPIKeys(entries)["anthropic"] != "new-key" {
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
	if _, err := NewLogin(context.Background(), LoginConfig{CallbackAddress: address}); err == nil {
		t.Fatal("two flows bound the same callback port")
	}
	flow.cancel()
	next, err := NewLogin(context.Background(), LoginConfig{CallbackAddress: address})
	if err != nil {
		t.Fatalf("callback port not immediately reusable: %v", err)
	}
	next.cancel()
}

func TestAnthropicCallbackParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	flow, err := NewLogin(ctx, LoginConfig{CallbackAddress: "127.0.0.1:0"})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer flow.cancel()
	cancel()
	if _, err := flow.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation: %v", err)
	}
	assertAnthropicCallbackClosed(t, flow)
}
