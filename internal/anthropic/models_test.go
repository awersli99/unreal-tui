package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"

	"github.com/awersli99/unreal-tui/internal/testutil"
)

func TestAnthropicCurrentModelThinking(t *testing.T) {
	ids := append([]string(nil), Snapshots...)
	for _, spec := range Models {
		ids = append(ids, spec.ID)
	}
	for _, id := range ids {
		for _, level := range Levels(id) {
			t.Run(id+"/"+level, func(t *testing.T) {
				request := anthropicTestRequest()
				request.Model.ID, request.Model.ReasoningEffort = id, llm.ReasoningEffort(level)
				payload, err := anthropicRequest(request, false, "")
				if err != nil {
					t.Fatal(err)
				}
				capabilities, _ := ModelForID(id)
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
				request.Input = append(request.Input, testutil.MessageItem(llm.RoleUser, "next"))
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
				if !bytes.Equal(firstJSON, prefixJSON) {
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

func TestAnthropicManagedEffortLegacyHistory(t *testing.T) {
	request := anthropicTestRequest()
	request.Model.ID = "claude-fable-5-1"
	request.Input = append(request.Input,
		llm.Item{ProviderID: "anthropic/claude-fable-5-1", Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: jsontext.Value(`{"type":"thinking","thinking":"Plan","signature":"old"}`)}},
		testutil.MessageItem(llm.RoleAssistant, "old reply"), testutil.MessageItem(llm.RoleUser, "next"))
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
