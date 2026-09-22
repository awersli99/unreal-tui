package anthropic

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/awersli99/unreal-tui/internal/config"
)

const anthropicAuthorizeURL = "https://claude.ai/oauth/authorize"

const anthropicOAuthScopes = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

// Endpoints/listener are injectable for offline OAuth tests. Production always
// uses Anthropic's endpoints and a loopback callback, independently of baseUrl.
type LoginConfig struct {
	AuthorizeURL    string
	TokenURL        string
	CallbackAddress string
}

type anthropicAuthorization struct {
	code string
	err  error
}

type Login struct {
	ctx         context.Context
	cancel      context.CancelFunc
	url         string
	redirectURI string
	tokenURL    string
	verifier    string
	state       string
	result      chan anthropicAuthorization
	received    atomic.Bool
}

func NewLogin(parent context.Context, options LoginConfig) (*Login, error) {
	verifier := make([]byte, 32)
	state := make([]byte, 32)
	if _, err := rand.Read(verifier); err != nil {
		return nil, err
	}
	if _, err := rand.Read(state); err != nil {
		return nil, err
	}
	var listen net.ListenConfig
	listener, err := listen.Listen(parent, "tcp4", config.FirstNonEmpty(options.CallbackAddress, "127.0.0.1:53692"))
	if err != nil {
		return nil, fmt.Errorf("start Anthropic login callback (close any other login using port 53692): %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	flow := &Login{
		ctx: ctx, cancel: cancel, tokenURL: config.FirstNonEmpty(options.TokenURL, anthropicTokenURL),
		verifier: base64.RawURLEncoding.EncodeToString(verifier), state: base64.RawURLEncoding.EncodeToString(state),
		redirectURI: fmt.Sprintf("http://localhost:%d/callback", listener.Addr().(*net.TCPAddr).Port),
		result:      make(chan anthropicAuthorization, 1),
	}
	challenge := sha256.Sum256([]byte(flow.verifier))
	parameters := url.Values{
		"code": {"true"}, "client_id": {anthropicOAuthClientID}, "response_type": {"code"},
		"redirect_uri": {flow.redirectURI}, "scope": {anthropicOAuthScopes},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "state": {flow.state},
	}
	flow.url = config.FirstNonEmpty(options.AuthorizeURL, anthropicAuthorizeURL) + "?" + parameters.Encode()
	server := &http.Server{Handler: http.HandlerFunc(flow.callback), ReadHeaderTimeout: 5 * time.Second}
	// Release the port synchronously on esc so a new /login can start at once.
	flow.cancel = func() { cancel(); _ = listener.Close() }
	go func() { _ = server.Serve(listener) }()
	go func() { <-ctx.Done(); _ = server.Close(); _ = listener.Close() }()
	return flow, nil
}

// URL is the authorization page to open in a browser.
func (flow *Login) URL() string { return flow.url }

// Context is canceled when the login finishes, fails, times out or is canceled.
func (flow *Login) Context() context.Context { return flow.ctx }

// Cancel abandons the login and releases the callback port.
func (flow *Login) Cancel() { flow.cancel() }

func (flow *Login) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Path != "/callback" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	if !flow.validState(query.Get("state")) {
		http.Error(w, "OAuth state mismatch. Return to unreal and try again.", http.StatusBadRequest)
		return
	}
	result := anthropicAuthorization{code: query.Get("code")}
	if query.Get("error") != "" {
		result.err = errors.New("Anthropic authorization was declined; run /login anthropic to try again")
	} else if result.code == "" {
		http.Error(w, "Missing authorization code.", http.StatusBadRequest)
		return
	}
	if err := flow.deliver(result); err != nil {
		http.Error(w, "This login is no longer waiting for authorization.", http.StatusConflict)
		return
	}
	fmt.Fprintln(w, "Authorization received. Return to unreal to finish signing in; you can close this window.")
}

func (flow *Login) validState(state string) bool {
	return subtle.ConstantTimeCompare([]byte(state), []byte(flow.state)) == 1
}

// Manual input supports remote browsers: paste the final redirect URL, a
// code#state pair, query parameters, or the code itself. Never print this input.
func (flow *Login) Submit(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("paste the authorization code or redirect URL, or complete login in your browser")
	}
	var code, state string
	switch {
	case strings.Contains(value, "://"):
		parsed, err := url.Parse(value)
		if err != nil {
			return errors.New("invalid authorization redirect URL")
		}
		code, state = parsed.Query().Get("code"), parsed.Query().Get("state")
		if !flow.validState(state) {
			return errors.New("OAuth state mismatch; use the redirect from this login attempt")
		}
	case strings.Contains(value, "#"):
		code, state, _ = strings.Cut(value, "#")
		if !flow.validState(state) {
			return errors.New("OAuth state mismatch; use the code from this login attempt")
		}
	case strings.Contains(value, "code="):
		query, err := url.ParseQuery(strings.TrimPrefix(value, "?"))
		if err != nil {
			return errors.New("invalid authorization parameters")
		}
		code, state = query.Get("code"), query.Get("state")
		if !flow.validState(state) {
			return errors.New("OAuth state mismatch; use the parameters from this login attempt")
		}
	default:
		code = value
	}
	if code == "" || strings.ContainsAny(code, "\r\n\t ") {
		return errors.New("invalid or missing authorization code")
	}
	return flow.deliver(anthropicAuthorization{code: code})
}

func (flow *Login) deliver(result anthropicAuthorization) error {
	if err := flow.ctx.Err(); err != nil {
		return err
	}
	if !flow.received.CompareAndSwap(false, true) {
		return errors.New("authorization already received")
	}
	flow.result <- result
	return nil
}

func (flow *Login) Wait() (Credential, error) {
	defer flow.cancel()
	var result anthropicAuthorization
	select {
	case <-flow.ctx.Done():
		return Credential{}, flow.ctx.Err()
	case result = <-flow.result:
	}
	if result.err != nil {
		return Credential{}, result.err
	}
	client := &http.Client{
		Transport:     http.DefaultTransport.(*http.Transport).Clone(),
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	return requestAnthropicToken(flow.ctx, client, flow.tokenURL, map[string]string{
		"grant_type": "authorization_code", "client_id": anthropicOAuthClientID,
		"code": result.code, "state": flow.state, "code_verifier": flow.verifier, "redirect_uri": flow.redirectURI,
	}, "login")
}

func OpenBrowser(ctx context.Context, address string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "open", address)
	case "windows":
		command = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", address)
	default:
		command = exec.CommandContext(ctx, "xdg-open", address)
	}
	// The URL is also displayed. Failure to launch a browser is not a failed
	// login (SSH/headless users can open it elsewhere and paste the redirect).
	_ = command.Run()
}
