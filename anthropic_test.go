package main

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

const anthropicReply = `{"id":"msg_1","type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"Hello"}],"usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":4}}`

func anthropicTestRequest() llm.Request {
	return llm.Request{Model: llm.Model{ID: defaultAnthropicModel, ReasoningEffort: llm.ReasoningEffortHigh},
		Input: []llm.Item{messageItem(llm.RoleSystem, "Follow project instructions."), messageItem(llm.RoleUser, "Hello")},
		Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "bash", Description: "Run a command", Parameters: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}}}}
}

func messageItem(role llm.Role, text string) llm.Item {
	return llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: role, Text: text}}
}

func toolCallItem(id string) llm.Item {
	return llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: "bash", Arguments: `{"command":"echo hello"}`}}
}

func toolResultItem(id, text string) llm.Item {
	return llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}}
}

func testAnthropicClient(t *testing.T, endpoint, key string, attempts int) *anthropicClient {
	t.Helper()
	client, err := newAnthropicClient(ProviderSpec{Name: "anthropic", API: apiAnthropic, BaseURL: endpoint}, key, attempts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestAnthropicTransportAuthModes(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(fmt.Sprint(oauth), func(t *testing.T) {
			key := "sk-ant-api-test"
			if oauth {
				key = "sk-ant-oat-test"
			}
			t.Setenv(anthropicCCVersionEnv, "2.1.300")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/messages" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Anthropic-Version") != "2023-06-01" {
					t.Error("missing API version")
				}
				var body anthropicPayload
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body.Model != defaultAnthropicModel || body.Stream || body.MaxTokens <= 0 || body.Thinking.Type != "adaptive" || body.OutputConfig["effort"] != "high" {
					t.Errorf("payload = %+v", body)
				}
				if oauth {
					if r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("X-Api-Key") != "" {
						t.Error("wrong OAuth auth mode")
					}
					if r.Header.Get("User-Agent") != "claude-cli/2.1.300" || r.Header.Get("X-App") != "cli" || r.Header.Get("Anthropic-Beta") != "claude-code-20250219,oauth-2025-04-20" {
						t.Errorf("OAuth headers = %v", r.Header)
					}
					if len(body.System) != 3 || body.System[0].Text != anthropicBillingHeader("Hello", "2.1.300") || body.System[0].CacheControl != nil || body.System[1].Text != anthropicCCIdentity || body.System[2].Text != "Follow project instructions." {
						t.Errorf("OAuth system = %+v", body.System)
					}
					if body.Tools[0].Name != "Bash" {
						t.Errorf("OAuth tool = %+v", body.Tools[0])
					}
				} else {
					if r.Header.Get("X-Api-Key") != key || r.Header.Get("Authorization") != "" {
						t.Error("wrong API key auth mode")
					}
					if r.Header.Get("X-App") != "" || r.Header.Get("Anthropic-Beta") != "" {
						t.Error("OAuth headers leaked into API key request")
					}
					if len(body.System) != 1 || body.System[0].Text != "Follow project instructions." || body.Tools[0].Name != "bash" {
						t.Errorf("API key request was shaped: %+v", body)
					}
				}
				if body.Tools[0].InputSchema["properties"] == nil || body.System[len(body.System)-1].CacheControl == nil {
					t.Error("lost schema or caching")
				}
				fmt.Fprint(w, anthropicReply)
			}))
			defer server.Close()
			client := testAnthropicClient(t, server.URL+"/v1/", key, 1)
			response, err := client.Respond(context.Background(), anthropicTestRequest(), llm.RequestOptions{CacheKey: "session"})
			if err != nil {
				t.Fatal(err)
			}
			if response.ID != "msg_1" || response.Stop != llm.StopComplete || response.Output[0].Data.(llm.Message).Text != "Hello" {
				t.Fatalf("response = %+v", response)
			}
			if u := response.Usage; u.InputTokens != 60 || u.CachedInputTokens != 20 || u.CacheWriteInputTokens != 30 || u.OutputTokens != 4 || !json.Valid(u.Raw) {
				t.Fatalf("usage = %+v", u)
			}
		})
	}
}

func TestAnthropicRequestReplayAndAsyncResults(t *testing.T) {
	raw := jsontext.Value(`{"type":"thinking","thinking":"inspect","signature":"signed","future_field":1234567890123456789}`)
	request := anthropicTestRequest()
	request.Input = append(request.Input,
		llm.Item{Type: llm.ItemReasoning, Data: llm.Reasoning{Summary: []string{"not the original"}, Raw: raw}},
		toolCallItem("call_1"),
		llm.Item{Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: jsontext.Value(`{"type":"redacted_thinking","data":"opaque"}`)}},
		toolCallItem("call_2"), messageItem(llm.RoleAssistant, "trailing text"),
		messageItem(llm.RoleUser, "steering before results"), toolResultItem("call_2", "done"), toolResultItem("call_1", "still running"),
		messageItem(llm.RoleAssistant, "waiting"), toolResultItem("call_1", "now complete"))
	original, _ := json.Marshal(request)
	payload, err := anthropicRequest(request, true, anthropicCCVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages) != 5 {
		t.Fatalf("messages = %+v", payload.Messages)
	}
	assistant := payload.Messages[1].Content
	var types []string
	for _, block := range assistant {
		types = append(types, block.Type)
	}
	if !reflect.DeepEqual(types, []string{"thinking", "tool_use", "redacted_thinking", "tool_use", "text"}) {
		t.Fatalf("assistant order = %v", types)
	}
	encoded, err := json.Marshal(assistant[0])
	if err != nil || string(encoded) != string(raw) {
		t.Fatalf("signed block changed: %s (%v)", encoded, err)
	}
	user := payload.Messages[2].Content
	if len(user) != 3 || user[0].ToolUseID != "call_1" || user[1].ToolUseID != "call_2" || user[2].Text != "steering before results" {
		t.Fatalf("user result order = %+v", user)
	}
	update := payload.Messages[4].Content
	if len(update) != 2 || update[0].Type != "text" || !strings.Contains(update[0].Text, "call_1") || update[1].Text != "now complete" {
		t.Fatalf("late result = %+v", update)
	}
	after, _ := json.Marshal(request)
	if string(original) != string(after) {
		t.Fatal("request input was mutated")
	}
}

func TestAnthropicMissingResultsImagesAndForeignReasoning(t *testing.T) {
	request := anthropicTestRequest()
	request.Input = append(request.Input,
		llm.Item{Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: jsontext.Value(`{"type":"reasoning","encrypted_content":"openai"}`)}},
		toolCallItem("call|foreign"), toolCallItem("other"), messageItem(llm.RoleUser, "keep going"),
		llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "call|foreign", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultImage, Value: "data:image/png;base64,aGVsbG8="}}}})
	payload, err := anthropicRequest(request, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages[1].Content) != 2 {
		t.Fatal("foreign reasoning was replayed")
	}
	call := payload.Messages[1].Content[0]
	if !anthropicToolIDPattern.MatchString(call.ID) || call.ID == "call|foreign" {
		t.Fatalf("unnormalized id %q", call.ID)
	}
	results := payload.Messages[2].Content
	if results[0].ToolUseID != call.ID || results[0].Content[0].Source.MediaType != "image/png" || results[0].Content[0].Source.Data != "aGVsbG8=" {
		t.Fatalf("image result = %+v", results[0])
	}
	if results[1].ToolUseID != "other" || !strings.Contains(results[1].Content[0].Text, "not yet available") {
		t.Fatalf("missing result = %+v", results[1])
	}
	request.Input = request.Input[:4] // history ending at first tool call
	payload, err = anthropicRequest(request, false, "")
	if err != nil || payload.Messages[len(payload.Messages)-1].Role != "user" {
		t.Fatalf("unanswered tool call: %+v, %v", payload, err)
	}
}

func TestAnthropicThinkingAndValidation(t *testing.T) {
	for _, test := range []struct {
		model       string
		effort      llm.ReasoningEffort
		kind, level string
		budget      int64
	}{
		{"claude-sonnet-4-6", "xhigh", "adaptive", "high", 0},
		{"claude-opus-4-6", "max", "adaptive", "max", 0},
		{"claude-opus-4-8", "xhigh", "adaptive", "xhigh", 0},
		{"claude-haiku-4-5", "low", "enabled", "", 1024},
		{"claude-sonnet-4-5", "high", "enabled", "", 8192},
	} {
		request := anthropicTestRequest()
		request.Model.ID, request.Model.ReasoningEffort = test.model, test.effort
		payload, err := anthropicRequest(request, false, "")
		if err != nil || payload.Thinking.Type != test.kind || payload.Thinking.Budget != test.budget || payload.OutputConfig["effort"] != test.level {
			t.Errorf("%s/%s = %+v, %v", test.model, test.effort, payload, err)
		}
	}
	for _, change := range []func(*llm.Request){
		func(r *llm.Request) { r.Model.ID = "" },
		func(r *llm.Request) { r.Model.ReasoningEffort = "invalid" },
		func(r *llm.Request) { r.Model.MaxOutputTokens = new(int64(-1)) },
		func(r *llm.Request) { r.Model.ID = "claude-haiku-4-5"; r.Model.MaxOutputTokens = new(int64(1000)) },
		func(r *llm.Request) { r.Input = nil },
		func(r *llm.Request) { r.Input[0].Data = "bad" },
		func(r *llm.Request) { r.Tools[0].Type = llm.ToolHosted },
	} {
		request := anthropicTestRequest()
		change(&request)
		if _, err := anthropicRequest(request, false, ""); err == nil {
			t.Error("invalid request accepted")
		}
	}
}

func TestAnthropicOAuthPromptAndVersion(t *testing.T) {
	request := anthropicTestRequest()
	const suffix = "\n\nKeep all tool instructions.\n<project_context>pi documentation and useful information</project_context>"
	request.Input[0] = messageItem(llm.RoleSystem, "You run on Unreal Agent Harness built by Unreal Labs."+suffix)
	payload, err := anthropicRequest(request, true, anthropicCCVersion)
	if err != nil {
		t.Fatal(err)
	}
	if payload.System[2].Text != "You are an expert coding assistant."+suffix {
		t.Fatal("OAuth shaping lost instructions/context")
	}
	payload, _ = anthropicRequest(request, false, "")
	if payload.System[0].Text != request.Input[0].Data.(llm.Message).Text {
		t.Fatal("API key prompt changed")
	}
	for _, bad := range []string{"latest", "v2.1.260", "2.1", "2.1.260-beta", "2.1.260\nheader"} {
		t.Setenv(anthropicCCVersionEnv, bad)
		if _, err := anthropicClaudeCodeVersion(); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	t.Setenv(anthropicCCVersionEnv, " 2.1.300 ")
	if version, err := anthropicClaudeCodeVersion(); err != nil || version != "2.1.300" {
		t.Fatalf("version = %q, %v", version, err)
	}
	if isAnthropicOAuth("plain-sk-ant-oat-not-a-token") {
		t.Error("OAuth token detection must be prefix gated")
	}
}

func TestAnthropicDecodePreservesThinkingAndStops(t *testing.T) {
	body := `{"id":"msg","type":"message","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"Plan","signature":"sig"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo hello"}},{"type":"redacted_thinking","data":"opaque"},{"type":"text","text":"after"}],"usage":{"input_tokens":1,"output_tokens":2}}`
	response, err := decodeAnthropicResponse([]byte(body), anthropicTestRequest().Tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 4 || response.Output[1].Data.(llm.ToolCall).Name != "bash" || response.Output[1].Data.(llm.ToolCall).Arguments != `{"command":"echo hello"}` {
		t.Fatalf("output = %+v", response.Output)
	}
	thinking := response.Output[0].Data.(llm.Reasoning)
	if thinking.Summary[0] != "Plan" || !strings.Contains(string(thinking.Raw), `"signature":"sig"`) || !strings.Contains(string(response.Output[2].Data.(llm.Reasoning).Raw), "opaque") {
		t.Fatal("lost signed thinking")
	}
	// Exercise the harness's session JSON codecs, not just in-memory replay.
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var replay llm.Response
	if err := json.Unmarshal(encoded, &replay); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response, replay) {
		t.Fatal("session round-trip changed response")
	}
	for reason, want := range map[string]llm.StopReason{"end_turn": llm.StopComplete, "tool_use": llm.StopComplete, "max_tokens": llm.StopMaxOutputTokens, "model_context_window_exceeded": llm.StopMaxOutputTokens, "refusal": llm.StopRefused} {
		response, err := decodeAnthropicResponse([]byte(strings.Replace(anthropicReply, "end_turn", reason, 1)), nil)
		if err != nil || response.Stop != want {
			t.Errorf("stop %s = %s, %v", reason, response.Stop, err)
		}
	}
	for _, body := range []string{`{`, `{}`, strings.Replace(anthropicReply, "end_turn", "unknown_stop", 1), strings.Replace(anthropicReply, `"type":"text"`, `"type":"unknown"`, 1)} {
		if _, err := decodeAnthropicResponse([]byte(body), nil); err == nil {
			t.Errorf("accepted invalid response %s", body)
		}
	}
}

func TestAnthropicRetriesAndCancellation(t *testing.T) {
	for _, status := range []int{400, 401, 403, 408, 429, 500, 529} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"type":"test_error","message":"failed sk-ant-api-test"}}`)
			}))
			defer server.Close()
			client := testAnthropicClient(t, server.URL, "sk-ant-api-test", 3)
			_, err := client.Respond(context.Background(), anthropicTestRequest(), llm.RequestOptions{})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), "sk-ant-api-test") {
				t.Fatalf("error = %v", err)
			}
			want := int32(1)
			if status == 408 || status == 429 || status >= 500 {
				want = 3
			}
			if calls.Load() != want {
				t.Fatalf("attempts = %d, want %d", calls.Load(), want)
			}
		})
	}
	for _, duringBackoff := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel-backoff-%v", duringBackoff), func(t *testing.T) {
			called := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				called <- struct{}{}
				if duringBackoff {
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(429)
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client := testAnthropicClient(t, server.URL, "test", 3)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := client.Respond(ctx, anthropicTestRequest(), llm.RequestOptions{}); done <- err }()
			<-called
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation blocked")
			}
		})
	}
}

func writeAnthropicCredential(t *testing.T, path, body string) {
	t.Helper()
	writeFile(t, path, body)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicAuthPrecedenceCatalogAndCustomProvider(t *testing.T) {
	isolateProviders(t)
	home := t.TempDir()
	if spec, ok := loadCatalog(home).Provider("anthropic"); !ok || spec.Available() {
		t.Fatalf("unconfigured provider = %+v", spec)
	}
	path := os.Getenv("ANTHROPIC_AUTH_FILE")
	writeAnthropicCredential(t, path, `{"anthropic":{"type":"oauth","access":"sk-ant-oat-stored","refresh":"refresh","expires":9999999999999}}`)
	catalog := loadCatalog(home)
	if provider, err := detectProvider(catalog); err != nil || provider != "anthropic" {
		t.Fatalf("detected = %s, %v", provider, err)
	}
	model, _, err := startupModel(catalog, Settings{}, options{provider: "anthropic"})
	if err != nil || model.ID != defaultAnthropicModel || model.ContextWindow == 0 {
		t.Fatalf("default = %+v, %v", model, err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "api-env")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "sk-ant-oat-env")
	for _, test := range []struct{ configured, override, apiKey, token, want string }{
		{"config", "override", "api-env", "sk-ant-oat-env", "config"},
		{"", "override", "api-env", "sk-ant-oat-env", "override"},
		{"", "", "api-env", "sk-ant-oat-env", "api-env"},
		{"", "", "", "sk-ant-oat-env", "sk-ant-oat-env"},
		{"", "", "", "", "sk-ant-oat-stored"},
	} {
		t.Setenv(providerAPIKeyOverride, test.override)
		t.Setenv("ANTHROPIC_API_KEY", test.apiKey)
		t.Setenv("ANTHROPIC_OAUTH_TOKEN", test.token)
		spec, _ := catalog.Provider("anthropic")
		spec.APIKey = test.configured
		client, err := spec.newClient(1)
		if err != nil {
			t.Fatal(err)
		}
		got, err := client.(*anthropicClient).auth.key(context.Background(), http.DefaultClient)
		client.Close()
		if err != nil || got != test.want {
			t.Errorf("resolved %q, %v; want %q", got, err, test.want)
		}
	}
	writeFile(t, filepath.Join(home, modelsFileName), `{"providers":{"proxy":{"api":"anthropic-messages","baseUrl":"http://localhost:1/v1","apiKey":"test","models":[{"id":"custom-claude"}]},"no-key":{"api":"anthropic","baseUrl":"http://localhost:2","models":[{"id":"must-not-inherit-login"}]}}}`)
	catalog = loadCatalog(home)
	proxy, ok := catalog.Provider("proxy")
	if !ok || proxy.API != apiAnthropic || !proxy.Available() || len(catalog.Warnings) != 0 {
		t.Fatalf("custom = %+v, warnings %v", proxy, catalog.Warnings)
	}
	missing, _ := catalog.Provider("no-key")
	if missing.Available() {
		t.Fatal("custom endpoint inherited built-in credentials")
	}
	if _, err := missing.newClient(1); err == nil {
		t.Fatal("custom endpoint accepted missing key")
	}
	if _, _, err := catalog.Resolve("anthropic/new-model", ""); err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicOAuthRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAnthropicCredential(t, path, `{"anthropic":{"type":"oauth","access":"sk-ant-oat-old","refresh":"old-refresh","expires":1,"custom":"keep"},"other":{"type":"api_key","key":"untouched"}}`)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "old-refresh" || body["client_id"] != anthropicOAuthClientID {
			t.Errorf("refresh body = %v", body)
		}
		fmt.Fprint(w, `{"access_token":"sk-ant-oat-new","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	defer server.Close()
	auth := anthropicAuth{path: path, tokenURL: server.URL}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			key, err := auth.key(context.Background(), server.Client())
			if err != nil || key != "sk-ant-oat-new" {
				t.Errorf("key = %q, %v", key, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refreshed %d times", calls.Load())
	}
	entries, credential, err := readAnthropicCredential(path)
	if err != nil || credential.Refresh != "new-refresh" || credential.Expires < time.Now().Add(50*time.Minute).UnixMilli() {
		t.Fatalf("saved = %+v, %v", credential, err)
	}
	if !strings.Contains(string(entries["anthropic"]), `"custom": "keep"`) || !strings.Contains(string(entries["other"]), "untouched") {
		t.Fatalf("lost other fields: %v", entries)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %v, %v", info, err)
	}
	// An external logout/login must affect the next call, not a cached token.
	writeAnthropicCredential(t, path, `{"anthropic":{"type":"api_key","key":"new-api-key"}}`)
	if key, err := auth.key(context.Background(), server.Client()); err != nil || key != "new-api-key" {
		t.Fatalf("external update ignored: %s, %v", key, err)
	}
}

func TestAnthropicAuthFailures(t *testing.T) {
	isolateProviders(t)
	path := os.Getenv("ANTHROPIC_AUTH_FILE")
	if _, err := resolveAnthropicAuth("", true); err == nil || !strings.Contains(err.Error(), "/login anthropic") {
		t.Fatalf("missing credentials: %v", err)
	}
	for _, body := range []string{`{`, `{}`, `{"anthropic":null}`, `{"anthropic":{"type":"oauth","access":"api-key"}}`, `{"anthropic":{"type":"api_key","key":"header\ninjection"}}`} {
		writeAnthropicCredential(t, path, body)
		if _, err := resolveAnthropicAuth("", true); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	writeAnthropicCredential(t, path, `{"anthropic":{"type":"oauth","access":"sk-ant-oat-expired","expires":1}}`)
	auth, err := resolveAnthropicAuth("", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.key(context.Background(), http.DefaultClient); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired credential = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAnthropicAuth("", true); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("public credentials = %v", err)
	}
	// Explicit API keys work even with an invalid stored OAuth credential.
	if _, err := resolveAnthropicAuth("explicit-key", true); err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicBillingHeaderMatchesNode(t *testing.T) {
	// Golden values generated with pi-anthropic-auth's Node/UTF-16 algorithm.
	for text, want := range map[string]string{
		"Hello": "x-anthropic-billing-header: cc_version=2.1.260.7a1; cc_entrypoint=sdk-cli; cch=185f8;",
		"Please summarize this repository status.": "x-anthropic-billing-header: cc_version=2.1.260.9bc; cc_entrypoint=sdk-cli; cch=772da;",
		"abc😀de😀abcdefghijkl😀xyz":                  "x-anthropic-billing-header: cc_version=2.1.260.730; cc_entrypoint=sdk-cli; cch=4ec2e;",
		"界界界界界界界界界界界界界界界界界界界界界":                    "x-anthropic-billing-header: cc_version=2.1.260.ba1; cc_entrypoint=sdk-cli; cch=da4df;",
		"abc😀xx😀abcdefghijk😀end":                   "x-anthropic-billing-header: cc_version=2.1.260.5c6; cc_entrypoint=sdk-cli; cch=6072d;",
	} {
		if got := anthropicBillingHeader(text, "2.1.260"); got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
}

func TestAnthropicProviderSwitch(t *testing.T) {
	request := anthropicTestRequest()
	request.Input = append(request.Input,
		llm.Item{ProviderID: "anthropic/" + request.Model.ID, Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: jsontext.Value(`{"type":"thinking","thinking":"plan","signature":"sig"}`)}},
		messageItem(llm.RoleAssistant, "previous answer"), messageItem(llm.RoleUser, "next"))
	before, _ := json.Marshal(request)
	client := newFakeClient(replyWith("switched"))
	engine := &Engine{client: client}
	if _, err := (liveAdapter{engine: engine}).Respond(context.Background(), request, llm.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(client.request(0).Input) != len(request.Input)-1 {
		t.Fatal("Anthropic reasoning leaked to another provider")
	}
	after, _ := json.Marshal(request)
	if string(after) != string(before) {
		t.Fatal("switch mutated persisted history")
	}
	request.Model.ID = "claude-haiku-4-5"
	payload, err := anthropicRequest(request, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages[1].Content) != 1 || payload.Messages[1].Content[0].Type != "text" {
		t.Fatal("thinking signature from another model was replayed")
	}
}

func TestAnthropicEngineToolRoundTrip(t *testing.T) {
	t.Setenv(anthropicCCVersionEnv, "")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload anthropicPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"id":"msg_tool","type":"message","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"Use Bash","signature":"sig"},{"type":"tool_use","id":"toolu_test","name":"Bash","input":{"command":"echo anthropic-tool-test"}}],"usage":{"input_tokens":10,"output_tokens":20}}`)
			return
		}
		found := false
		for _, message := range payload.Messages {
			for _, block := range message.Content {
				if block.Type == "tool_result" && block.ToolUseID == "toolu_test" {
					found = true
				}
			}
		}
		if !found {
			t.Error("engine lost tool result")
		}
		fmt.Fprint(w, anthropicReply)
	}))
	defer server.Close()
	client := testAnthropicClient(t, server.URL, "sk-ant-oat-test", 1)
	current := newHarness(t, newFakeClient())
	current.engine.SetModel(client, defaultAnthropicModel)
	if err := current.engine.Send("run a command"); err != nil {
		t.Fatal(err)
	}
	current.waitForAssistant("Hello")
	found := false
	for _, block := range current.blocks {
		if block.Kind == BlockTool && block.Tool.Name == "Bash" {
			found = true
		}
	}
	if !found {
		t.Fatal("no tool in transcript")
	}
	items, err := current.engine.loadItems(current.engine.SessionID())
	if err != nil || len(items) == 0 {
		t.Fatalf("persisted history: %v", err)
	}
	if calls.Load() < 2 {
		t.Fatal("engine did not continue after tool use")
	}
}

func TestAnthropicRefreshFailuresPreserveCredentials(t *testing.T) {
	for _, result := range []struct {
		status int
		body   string
	}{
		{401, `{"error":"do not print secret-refresh"}`},
		{200, `not json`},
		{200, `{"access_token":"sk-ant-oat-new","refresh_token":"r","expires_in":-1}`},
	} {
		path := filepath.Join(t.TempDir(), "auth.json")
		const initial = `{"anthropic":{"type":"oauth","access":"sk-ant-oat-expired","refresh":"secret-refresh","expires":1}}`
		writeAnthropicCredential(t, path, initial)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(result.status); fmt.Fprint(w, result.body) }))
		auth := anthropicAuth{path: path, tokenURL: server.URL}
		_, err := auth.key(context.Background(), server.Client())
		server.Close()
		if err == nil || strings.Contains(err.Error(), "secret-refresh") {
			t.Fatalf("refresh error = %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil || string(body) != initial {
			t.Fatal("failed refresh changed credentials")
		}
	}
}

func TestAnthropicRedirectAndInvalidVersionDoNotSendCredentials(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := testAnthropicClient(t, server.URL, "sk-ant-api-test", 1)
	t.Setenv(anthropicCCVersionEnv, "invalid")
	if _, err := client.Respond(context.Background(), anthropicTestRequest(), llm.RequestOptions{}); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("API key path affected by OAuth version or followed redirect: %v", err)
	}
	if forwarded.Load() != 0 {
		t.Fatal("credentials followed redirect")
	}
	client.auth.token = "sk-ant-oat-test"
	if _, err := client.Respond(context.Background(), anthropicTestRequest(), llm.RequestOptions{}); err == nil || !strings.Contains(err.Error(), "X.Y.Z") {
		t.Fatalf("OAuth version not validated: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("invalid OAuth version reached network")
	}
}

func TestAnthropicRefreshLockIsCancelable(t *testing.T) {
	anthropicRefreshLock <- struct{}{}
	defer func() { <-anthropicRefreshLock }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (anthropicAuth{path: "unused"}).key(ctx, http.DefaultClient)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting for refresh: %v", err)
	}
}

func TestAnthropicConfigurationValidation(t *testing.T) {
	isolateProviders(t)
	for _, endpoint := range []string{"ftp://example.com", "relative", "https://example.com?secret=x", "http://user:secret@example.com", "http://example.com#fragment"} {
		if _, err := newAnthropicClient(ProviderSpec{BaseURL: endpoint}, "test", 1); err == nil {
			t.Errorf("accepted endpoint %q", endpoint)
		}
	}
	if _, err := newAnthropicClient(ProviderSpec{}, "test", 0); err == nil {
		t.Fatal("accepted zero attempts")
	}
	if _, err := newAnthropicClient(ProviderSpec{}, "bad\nkey", 1); err == nil {
		t.Fatal("accepted malformed key")
	}
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "sk-ant-oat-available")
	spec := ProviderSpec{Name: "anthropic", API: apiAnthropic, APIKey: "!printf ''"}
	if _, err := spec.newClient(1); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty explicit key fell back to OAuth: %v", err)
	}
	if got := anthropicRetryDelay(0, http.Header{"Retry-After": []string{"120"}}); got != time.Minute {
		t.Fatalf("Retry-After not capped: %v", got)
	}
}
