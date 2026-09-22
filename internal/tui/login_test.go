package tui

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/unreallabsai/unreal-agent/harness/llm"

	"github.com/awersli99/unreal-tui/internal/anthropic"
	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/engine"
	"github.com/awersli99/unreal-tui/internal/provider"
	"github.com/awersli99/unreal-tui/internal/testutil"
)

func newLoginTestModel(t *testing.T) *model {
	t.Helper()
	testutil.IsolateProviders(t)
	home := t.TempDir()
	t.Setenv("UNREAL_TUI_HOME", home)
	ctx, cancel := context.WithCancel(context.Background())
	agent, err := engine.New(ctx, engine.Config{StoreDirectory: t.TempDir(), Workspace: t.TempDir(),
		Client: provider.UnavailableClient{Err: errors.New("no model in tests")}, Model: "test-model", Emit: func(any) {}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		agent.Close()
		cancel()
	})
	catalog := provider.LoadCatalog(home)
	app := &App{Engine: agent, Config: config.Config{Home: home, Workspace: t.TempDir()}, Catalog: catalog,
		Model: catalog.Lookup("anthropic", anthropic.DefaultModel), Thinking: "high"}
	t.Cleanup(app.Close)
	return newModel(app)
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
	flow := testAnthropicLogin(t)
	// Do not execute the returned command: that would open a real browser.
	current.showAnthropicLogin(flow)
	if current.overlay.prompt.input.EchoMode != textinput.EchoPassword {
		t.Fatal("authorization code is not masked")
	}
	if err := config.SaveAPIKey(current.app.Config.Home, "openrouter", "keep-other-key"); err != nil {
		t.Fatal(err)
	}
	current.Update(anthropicLoginFinishedMsg{flow: flow, credential: nativeTestCredential()})
	if current.overlay != nil || current.anthropicLogin != nil {
		t.Fatal("login prompt did not close")
	}
	entries, warning, err := config.ReadCredentials(current.app.Config.Home)
	if err != nil || warning != "" || len(entries) != 2 {
		t.Fatalf("stored credentials: %v, %q, %v", entries, warning, err)
	}
	_, stored, err := anthropic.ReadCredential(config.AuthPath(current.app.Config.Home))
	if err != nil || stored.Refresh != "local-refresh" {
		t.Fatalf("OAuth credential not stored: %+v, %v", stored, err)
	}
	client, _, _ := current.engine.LiveModel()
	if actual, ok := client.(*anthropic.Client); !ok {
		t.Fatalf("new client not applied: %#v", client)
	} else if key, err := actual.Credential(context.Background()); err != nil || key != "sk-ant-oat-local" {
		t.Fatalf("new client does not use the login: %q, %v", key, err)
	}
	if len(current.app.Catalog.Models) < 5 {
		t.Fatal("login did not expose Claude models")
	}
	if info, err := os.Stat(config.AuthPath(current.app.Config.Home)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("auth permissions: %v, %v", info, err)
	}
	current.openLogout("anthropic")
	entries, _, err = config.ReadCredentials(current.app.Config.Home)
	if err != nil || len(entries) != 1 || entries["anthropic"] != nil || entries["openrouter"] == nil {
		t.Fatalf("logout damaged credentials: %v, %v", entries, err)
	}
	client, _, _ = current.engine.LiveModel()
	if _, ok := client.(provider.UnavailableClient); !ok {
		t.Fatalf("logout retained authenticated client: %T", client)
	}
	if _, err := client.Respond(context.Background(), llm.Request{}, llm.RequestOptions{}); err == nil {
		t.Fatal("logged-out client still works")
	}
}

func TestAnthropicLoginCancelIgnoresLateCompletion(t *testing.T) {
	current := newLoginTestModel(t)
	flow := testAnthropicLogin(t)
	current.showAnthropicLogin(flow)
	current.Update(tea.KeyMsg{Type: tea.KeyEsc})
	current.Update(anthropicLoginFinishedMsg{flow: flow, credential: nativeTestCredential()})
	if current.overlay != nil || current.anthropicLogin != nil {
		t.Fatal("cancel did not clear login")
	}
	if _, err := os.Stat(config.AuthPath(current.app.Config.Home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled login wrote credentials: %v", err)
	}
	if flow.Context().Err() == nil {
		t.Fatal("cancel did not stop the login flow")
	}
	first := testAnthropicLogin(t)
	second := testAnthropicLogin(t)
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

func nativeTestCredential() anthropic.Credential {
	return anthropic.Credential{Type: "oauth", Access: "sk-ant-oat-local", Refresh: "local-refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}
}

func testAnthropicLogin(t *testing.T) *anthropic.Login {
	t.Helper()
	flow, err := anthropic.NewLogin(context.Background(), anthropic.LoginConfig{CallbackAddress: "127.0.0.1:0", TokenURL: "http://127.0.0.1:1/unused"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flow.Cancel)
	return flow
}
