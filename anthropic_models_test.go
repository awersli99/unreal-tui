package main

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestAnthropicCurrentCatalog(t *testing.T) {
	isolateProviders(t)
	t.Setenv("ANTHROPIC_API_KEY", "offline-test")
	t.Setenv("UNREAL_HARNESS_LLM_PROVIDER", "")
	t.Setenv("UNREAL_HARNESS_LLM_MODEL", "")
	catalog := loadCatalog(t.TempDir())
	for _, id := range []string{
		"claude-opus-5-5", "claude-opus-5", "claude-sonnet-5", "claude-fable-5-1", "claude-fable-5",
		"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6", "claude-sonnet-4-5",
	} {
		matches, _ := catalog.Match("anthropic/" + id)
		if len(matches) != 1 || matches[0].ContextWindow != 1000000 {
			t.Errorf("catalog metadata for %s = %+v", id, matches)
		}
		info, level, err := catalog.Resolve("anthropic/"+id+":max", "")
		if err != nil || info.Name == "" || level != "max" {
			t.Errorf("resolve %s: %+v %s %v", id, info, level, err)
		}
	}
	for _, id := range []string{"claude-haiku-4-5", "claude-opus-4-5", "claude-haiku-4-5-20251001", "claude-opus-4-5-20251101"} {
		if model := catalog.Lookup("anthropic", id); model.ContextWindow != 200000 || model.Name == "" {
			t.Errorf("legacy metadata = %+v", model)
		}
	}
	if model := catalog.Lookup("anthropic", "claude-sonnet-4-5-20250929"); model.ContextWindow != 1000000 {
		t.Fatalf("snapshot metadata = %+v", model)
	}
	model, _, err := startupModel(catalog, Settings{}, options{provider: "anthropic"})
	if err != nil || model.ID != "claude-sonnet-5" {
		t.Fatalf("new default = %+v, %v", model, err)
	}
	model, _, err = startupModel(catalog, Settings{DefaultProvider: "anthropic", DefaultModel: "claude-sonnet-4-6"}, options{})
	if err != nil || model.ID != "claude-sonnet-4-6" {
		t.Fatalf("saved default changed = %+v, %v", model, err)
	}
	seen := map[string]bool{}
	for _, info := range builtinAnthropicModels("anthropic") {
		if seen[info.ID] {
			t.Errorf("duplicate model %s", info.ID)
		}
		seen[info.ID] = true
		if info.DefaultLevel == "" || info.ContextWindow == 0 {
			t.Errorf("incomplete metadata: %+v", info)
		}
	}
}

func TestAnthropicModelOverridesKeepCapabilities(t *testing.T) {
	isolateProviders(t)
	t.Setenv("ANTHROPIC_API_KEY", "offline-test")
	home := t.TempDir()
	writeFile(t, filepath.Join(home, modelsFileName), `{"providers":{
		"anthropic":{"models":[{"id":"claude-opus-5-5","name":"My Opus"},{"id":"claude-fable-5-1","contextWindow":500000,"thinkingLevels":["low","high"]}]},
		"proxy":{"api":"anthropic-messages","baseUrl":"http://localhost:1","apiKey":"key","models":[{"id":"claude-sonnet-4-6"}]}
	}}`)
	catalog := loadCatalog(home)
	if info := catalog.Lookup("anthropic", "claude-opus-5-5"); info.Name != "My Opus" || info.ContextWindow != 1000000 || !slices.Contains(info.Levels, "xhigh") {
		t.Fatalf("metadata lost on name override: %+v", info)
	}
	if info := catalog.Lookup("anthropic", "claude-fable-5-1"); info.ContextWindow != 500000 || !reflect.DeepEqual(info.Levels, []string{"low", "high"}) {
		t.Fatalf("explicit override ignored: %+v", info)
	}
	if info := catalog.Lookup("proxy", "claude-sonnet-4-6"); info.ContextWindow != 1000000 || slices.Contains(info.Levels, "xhigh") {
		t.Fatalf("proxy lost known model capabilities: %+v", info)
	}
	// Resolve an unlisted snapshot even before credentials are configured.
	bare := &Catalog{Providers: builtinProviders()}
	if info := bare.Lookup("anthropic", "claude-opus-5-5-20260922"); info.ContextWindow != 1000000 || !slices.Contains(info.Levels, "xhigh") {
		t.Fatalf("unlisted snapshot = %+v", info)
	}
	for _, id := range []string{"claude-opus-5-50", "claude-sonnet-4-60", "claude-opus-5-5-bogus", "claude-future"} {
		if _, known := anthropicModelForID(id); known {
			t.Errorf("invented capabilities for %s", id)
		}
	}
}

func TestAnthropicCurrentModelThinking(t *testing.T) {
	for _, model := range builtinAnthropicModels("anthropic") {
		for _, level := range model.SupportedLevels() {
			t.Run(model.ID+"/"+level, func(t *testing.T) {
				request := anthropicTestRequest()
				request.Model.ID, request.Model.ReasoningEffort = model.ID, llm.ReasoningEffort(level)
				payload, err := anthropicRequest(request, false, "")
				if err != nil {
					t.Fatal(err)
				}
				capabilities, _ := anthropicModelForID(model.ID)
				thinking := payload.Thinking
				if thinking == nil || thinking.Display != "summarized" {
					t.Fatalf("thinking = %+v", thinking)
				}
				if !capabilities.Adaptive {
					if thinking.Type != "enabled" || thinking.Budget < 1024 || payload.OutputConfig != nil {
						t.Fatalf("legacy thinking = %+v", payload)
					}
					return
				}
				if thinking.Type != "adaptive" || thinking.Budget != 0 {
					t.Fatalf("adaptive thinking = %+v", thinking)
				}
				if !capabilities.ManagedEffort {
					if payload.OutputConfig["effort"] != level || thinking.BlockBinding != nil || payload.Messages[len(payload.Messages)-1].Role == "system" {
						t.Fatalf("ordinary effort = %+v", payload)
					}
					return
				}
				last := payload.Messages[len(payload.Messages)-1]
				if payload.OutputConfig["effort"] != "high" || last.Role != "system" || last.OutputConfig["effort"] != level || len(last.Content) != 0 || last.Content == nil || thinking.BlockBinding.PrefixMismatchBehavior != "drop_block" {
					t.Fatalf("managed effort = %+v", payload)
				}
				request.Model.ReasoningEffort = ""
				payload, err = anthropicRequest(request, false, "")
				if err != nil || payload.Thinking.Type != "adaptive" {
					t.Fatalf("always-on adaptive thinking: %+v, %v", payload, err)
				}
			})
		}
	}
	for id, limit := range map[string]int64{"claude-opus-5-5": 128000, "claude-sonnet-5": 128000, "claude-haiku-4-5": 64000} {
		request := anthropicTestRequest()
		request.Model.ID, request.Model.MaxOutputTokens = id, &limit
		if _, err := anthropicRequest(request, false, ""); err != nil {
			t.Fatal(err)
		}
		limit++
		if _, err := anthropicRequest(request, false, ""); err == nil {
			t.Errorf("%s accepted too many output tokens", id)
		}
	}
}

func TestAnthropicManagedEffortReplay(t *testing.T) {
	for _, withThinking := range []bool{false, true} {
		for _, oauth := range []bool{false, true} {
			t.Run(fmt.Sprintf("thinking=%t/oauth=%t", withThinking, oauth), func(t *testing.T) {
				t.Setenv(anthropicCCVersionEnv, "")
				captured := make(chan anthropicPayload, 2)
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var payload anthropicPayload
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					captured <- payload
					betas := r.Header.Get("Anthropic-Beta")
					if !strings.Contains(betas, "mid-conversation-output-config-2026-07-01") || !strings.Contains(betas, "thinking-binding-controls-2026-08-01") {
						t.Error("missing effort/binding beta headers")
					}
					if oauth {
						if r.Header.Get("User-Agent") != "claude-cli/2.1.280" || !strings.Contains(payload.System[0].Text, "cc_version=2.1.280.") {
							t.Error("outdated OAuth identity")
						}
					} else if strings.Contains(betas, "oauth-") || r.Header.Get("X-App") != "" {
						t.Error("OAuth compatibility leaked into API-key request")
					}
					calls++
					if calls == 1 && withThinking {
						fmt.Fprint(w, `{"id":"msg_tools","type":"message","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"Plan","signature":"signed"},{"type":"tool_use","id":"call_1","name":"Bash","input":{"command":"echo hi"}},{"type":"text","text":"trailing text"}],"usage":{"input_tokens":10,"output_tokens":20}}`)
					} else {
						fmt.Fprint(w, anthropicReply)
					}
				}))
				defer server.Close()
				key := "sk-ant-api-test"
				if oauth {
					key = "sk-ant-oat-test"
				}
				client := testAnthropicClient(t, server.URL, key, 1)
				request := anthropicTestRequest()
				request.Model.ID, request.Model.ReasoningEffort = "claude-opus-5-5", "low"
				response, err := client.Respond(context.Background(), request, llm.RequestOptions{})
				if err != nil {
					t.Fatal(err)
				}
				first := <-captured
				for _, item := range response.Output {
					if item.ProviderID != "anthropic/claude-opus-5-5:low" {
						t.Fatalf("lost effort metadata on %s", item.Type)
					}
				}
				// Persist/reload using the harness's llm.Item JSON codec.
				stored, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				var replay llm.Response
				if err := json.Unmarshal(stored, &replay); err != nil {
					t.Fatal(err)
				}
				request.Input = append(request.Input, replay.Output...)
				request.Input = append(request.Input, messageItem(llm.RoleUser, "next"))
				if withThinking {
					request.Input = append(request.Input, toolResultItem("call_1", "complete"))
				}
				request.Model.ReasoningEffort = "xhigh"
				if _, err := client.Respond(context.Background(), request, llm.RequestOptions{}); err != nil {
					t.Fatal(err)
				}
				second := <-captured
				prefixJSON, _ := json.Marshal(second.Messages[:len(first.Messages)])
				// Cache breakpoints move, but the prompt and effort prefix do not.
				first.Messages[0].Content[0].CacheControl = nil
				firstJSON, _ := json.Marshal(first.Messages)
				if string(firstJSON) != string(prefixJSON) {
					t.Fatalf("prefix changed: %s != %s", firstJSON, prefixJSON)
				}
				last := second.Messages[len(second.Messages)-1]
				if last.OutputConfig["effort"] != "xhigh" || second.OutputConfig["effort"] != "high" {
					t.Fatal("effort change not applied as a message")
				}
				if withThinking {
					assistant, user := second.Messages[2], second.Messages[3]
					if assistant.Role != "assistant" || len(assistant.Content) != 3 || assistant.Content[0].Type != "thinking" || assistant.Content[1].Type != "tool_use" || assistant.Content[2].Type != "text" || user.Role != "user" || user.Content[0].ToolUseID != "call_1" {
						t.Fatalf("effort markers broke tool history: %+v", second.Messages)
					}
				}
			})
		}
	}
}

func TestAnthropicEffortMetadataDoesNotLeakToOtherProviders(t *testing.T) {
	input := []llm.Item{
		messageItem(llm.RoleUser, "hello"),
		{ProviderID: "anthropic/claude-opus-5-5:low", Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: jsontext.Value(`{"type":"thinking","thinking":"Plan","signature":"signed"}`)}},
		{ProviderID: "anthropic/claude-opus-5-5:low", Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "calling a tool"}},
		{ProviderID: "anthropic/claude-opus-5-5:low", Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "toolu_1", Name: "Bash", Arguments: `{}`}},
	}
	before, _ := json.Marshal(input)
	client := newFakeClient(replyWith("switched"))
	engine := &Engine{client: client}
	if _, err := (liveAdapter{engine: engine}).Respond(context.Background(), llm.Request{Input: input}, llm.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	converted := client.request(0).Input
	if len(converted) != 3 || converted[1].ProviderID != "" || converted[2].ProviderID != "" || converted[2].Data.(llm.ToolCall).CallID != "toolu_1" {
		t.Fatalf("foreign provider input = %+v", converted)
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("persisted history mutated")
	}
	plain := []llm.Item{messageItem(llm.RoleUser, "unrelated")}
	if &withoutAnthropicState(plain)[0] != &plain[0] {
		t.Fatal("unrelated request was copied/changed")
	}
}

func TestAnthropicManagedEffortLegacyHistory(t *testing.T) {
	request := anthropicTestRequest()
	request.Model.ID = "claude-fable-5-1"
	request.Input = append(request.Input,
		llm.Item{ProviderID: "anthropic/claude-fable-5-1", Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: jsontext.Value(`{"type":"thinking","thinking":"Plan","signature":"old"}`)}},
		messageItem(llm.RoleAssistant, "old reply"), messageItem(llm.RoleUser, "next"))
	payload, err := anthropicRequest(request, false, "")
	if err != nil {
		t.Fatal(err)
	}
	markers := 0
	for _, message := range payload.Messages {
		if message.Role == "system" {
			markers++
		}
	}
	if markers != 1 || payload.Messages[1].Content[0].Type != "thinking" {
		t.Fatal("legacy history lost thinking or acquired invented effort markers")
	}
	if payload.Thinking.BlockBinding.PrefixMismatchBehavior != "drop_block" {
		t.Fatal("legacy signatures cannot recover from prefix changes")
	}
}
