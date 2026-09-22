package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"

	"github.com/awersli99/unreal-tui/internal/anthropic"
	"github.com/awersli99/unreal-tui/internal/testutil"
)

func TestAnthropicEffortMetadataDoesNotLeakToOtherProviders(t *testing.T) {
	input := []llm.Item{
		testutil.MessageItem(llm.RoleUser, "hello"),
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
	if !bytes.Equal(before, after) {
		t.Fatal("persisted history mutated")
	}
	plain := []llm.Item{testutil.MessageItem(llm.RoleUser, "unrelated")}
	if &anthropic.WithoutState(plain)[0] != &plain[0] {
		t.Fatal("unrelated request was copied/changed")
	}
}

func TestAnthropicReasoningDroppedForOtherProviders(t *testing.T) {
	request := llm.Request{Model: llm.Model{ID: anthropic.DefaultModel}, Input: []llm.Item{
		testutil.MessageItem(llm.RoleUser, "Hello"),
		{ProviderID: "anthropic/" + anthropic.DefaultModel, Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: jsontext.Value(`{"type":"thinking","thinking":"plan","signature":"sig"}`)}},
		testutil.MessageItem(llm.RoleAssistant, "previous answer"), testutil.MessageItem(llm.RoleUser, "next"),
	}}
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
	if !bytes.Equal(after, before) {
		t.Fatal("switch mutated persisted history")
	}
}

func TestAnthropicEngineToolRoundTrip(t *testing.T) {
	t.Setenv("PI_ANTHROPIC_AUTH_CLAUDE_CODE_VERSION", "")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content []struct {
					Type      string `json:"type"`
					ToolUseID string `json:"tool_use_id"`
				} `json:"content"`
			} `json:"messages"`
		}
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
		fmt.Fprint(w, `{"id":"msg_1","type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"Hello"}],"usage":{"input_tokens":10,"output_tokens":4}}`)
	}))
	defer server.Close()
	client, err := anthropic.NewClient(anthropic.ClientConfig{BaseURL: server.URL, Key: "sk-ant-oat-test", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	current := newHarness(t, newFakeClient())
	current.engine.SetModel(client, anthropic.DefaultModel)
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
