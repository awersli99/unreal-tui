package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// fakeClient answers model calls with scripted handlers, one per call.
type fakeClient struct {
	mu       sync.Mutex
	handlers []func(context.Context, llm.Request) (llm.Response, error)
	requests []llm.Request
	called   chan struct{}
}

func newFakeClient(handlers ...func(context.Context, llm.Request) (llm.Response, error)) *fakeClient {
	return &fakeClient{handlers: handlers, called: make(chan struct{}, 16)}
}

func (client *fakeClient) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	client.mu.Lock()
	client.requests = append(client.requests, request)
	var handler func(context.Context, llm.Request) (llm.Response, error)
	if len(client.handlers) != 0 {
		handler, client.handlers = client.handlers[0], client.handlers[1:]
	}
	client.mu.Unlock()
	client.called <- struct{}{}
	if handler == nil {
		return reply("(unscripted)"), nil
	}
	return handler(ctx, request)
}

func (client *fakeClient) Close() error { return nil }

func (client *fakeClient) request(index int) llm.Request {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.requests[index]
}

func reply(text string) llm.Response {
	return llm.Response{Stop: llm.StopComplete, Output: []llm.Item{{
		Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: text},
	}}, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}
}

func replyWith(text string) func(context.Context, llm.Request) (llm.Response, error) {
	return func(context.Context, llm.Request) (llm.Response, error) { return reply(text), nil }
}

func callBash(command string) func(context.Context, llm.Request) (llm.Response, error) {
	return func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Stop: llm.StopComplete, Output: []llm.Item{{
			Type: llm.ItemToolCall,
			Data: llm.ToolCall{CallID: "call_1", Name: "Bash", Arguments: `{"command":"` + command + `"}`},
		}}}, nil
	}
}

func blockUntilCanceled(ctx context.Context, _ llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

// harness wires an Engine to a Transcript the way the TUI does.
type harness struct {
	t          *testing.T
	engine     *Engine
	client     *fakeClient
	events     chan any
	transcript *Transcript
	blocks     []Block
}

func newHarness(t *testing.T, client *fakeClient) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	current := &harness{t: t, client: client, events: make(chan any, 1024)}
	engine, err := NewEngine(ctx, EngineConfig{
		StoreDirectory: t.TempDir(),
		Workspace:      t.TempDir(),
		Client:         client,
		Model:          "test-model",
		Effort:         llm.ReasoningEffortLow,
		SystemPrompt:   "test prompt",
		Emit:           func(event any) { current.events <- event },
	})
	if err != nil {
		t.Fatal(err)
	}
	current.engine = engine
	current.transcript = NewTranscript(engine.ResultTranslator)
	t.Cleanup(func() {
		engine.Close()
		cancel()
	})
	return current
}

// waitFor consumes events until done reports true.
func (current *harness) waitFor(what string, done func(event any) bool) {
	current.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case event := <-current.events:
			switch event := event.(type) {
			case ItemEvent:
				current.blocks = append(current.blocks, current.transcript.Apply(event.Item)...)
			case RunEndedEvent:
				current.blocks = append(current.blocks, current.transcript.Abort()...)
				if event.Err != nil {
					current.t.Fatalf("run ended with error: %v", event.Err)
				}
			}
			if done(event) {
				return
			}
		case <-timeout:
			current.t.Fatalf("timed out waiting for %s; blocks so far: %+v", what, current.blocks)
		}
	}
}

func (current *harness) waitForAssistant(text string) {
	current.t.Helper()
	current.waitFor("assistant "+text, func(any) bool {
		for _, block := range current.blocks {
			if block.Kind == BlockAssistant && block.Text == text {
				return true
			}
		}
		return false
	})
}

func (current *harness) waitForCall() {
	current.t.Helper()
	select {
	case <-current.client.called:
	case <-time.After(10 * time.Second):
		current.t.Fatal("model was not called")
	}
}

func userMessages(request llm.Request) []string {
	var messages []string
	for _, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok && message.Role == llm.RoleUser {
			messages = append(messages, message.Text)
		}
	}
	return messages
}

func TestToolRoundTrip(t *testing.T) {
	current := newHarness(t, newFakeClient(callBash("echo hello from bash"), replyWith("done")))
	if err := current.engine.Send("run it"); err != nil {
		t.Fatal(err)
	}
	current.waitForAssistant("done")

	var kinds []BlockKind
	var tool *ToolView
	for _, block := range current.blocks {
		kinds = append(kinds, block.Kind)
		if block.Kind == BlockTool {
			tool = block.Tool
		}
	}
	if want := []BlockKind{BlockUser, BlockTool, BlockAssistant}; !equalKinds(kinds, want) {
		t.Fatalf("block kinds = %v, want %v", kinds, want)
	}
	if tool.Title != "echo hello from bash" || tool.State != ToolSucceeded || strings.TrimSpace(tool.Output) != "hello from bash" {
		t.Fatalf("tool = %+v", tool)
	}
	if current.transcript.Busy() {
		t.Fatal("transcript still busy after final reply")
	}
	request := current.client.request(0)
	if request.Model.ID != "test-model" || request.Model.ReasoningEffort != llm.ReasoningEffortLow {
		t.Fatalf("model = %+v", request.Model)
	}
}

func TestSteeringInterruptsInFlightCall(t *testing.T) {
	current := newHarness(t, newFakeClient(blockUntilCanceled, replyWith("steered")))
	if err := current.engine.Send("first"); err != nil {
		t.Fatal(err)
	}
	current.waitForCall()
	if err := current.engine.Send("actually, second"); err != nil {
		t.Fatal(err)
	}
	current.waitForAssistant("steered")
	if got := userMessages(current.client.request(1)); !equalStrings(got, []string{"first", "actually, second"}) {
		t.Fatalf("second request user messages = %q", got)
	}
}

func TestInterruptThenContinue(t *testing.T) {
	current := newHarness(t, newFakeClient(blockUntilCanceled, replyWith("resumed")))
	if err := current.engine.Send("first"); err != nil {
		t.Fatal(err)
	}
	current.waitForCall()
	if !current.engine.Interrupt() {
		t.Fatal("Interrupt reported no running coordinator")
	}
	current.waitFor("run end", func(event any) bool { _, ok := event.(RunEndedEvent); return ok })
	if current.transcript.Busy() {
		t.Fatal("transcript busy after interrupt")
	}
	select {
	case <-current.client.called:
		t.Fatal("model called again after interrupt without a new message")
	case <-time.After(300 * time.Millisecond):
	}

	if err := current.engine.Send("second"); err != nil {
		t.Fatal(err)
	}
	current.waitForAssistant("resumed")
	if got := userMessages(current.client.request(1)); !equalStrings(got, []string{"first", "second"}) {
		t.Fatalf("request after interrupt has user messages %q", got)
	}
	var sawInterrupted bool
	for _, block := range current.blocks {
		sawInterrupted = sawInterrupted || (block.Kind == BlockInfo && block.Text == "Interrupted")
	}
	if !sawInterrupted {
		t.Fatalf("no Interrupted block in %+v", current.blocks)
	}
}

func TestInterruptKillsRunningTool(t *testing.T) {
	current := newHarness(t, newFakeClient(callBash("sleep 30"), replyWith("after")))
	if err := current.engine.Send("sleep"); err != nil {
		t.Fatal(err)
	}
	current.waitFor("tool running", func(any) bool { return len(current.transcript.Running()) == 1 })
	started := time.Now()
	current.engine.Interrupt()
	current.waitFor("run end", func(event any) bool { _, ok := event.(RunEndedEvent); return ok })
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("interrupt took %s", elapsed)
	}
	var tool *ToolView
	for _, block := range current.blocks {
		if block.Kind == BlockTool {
			tool = block.Tool
		}
	}
	if tool == nil || tool.State != ToolCanceled || tool.Output != "(canceled)" {
		t.Fatalf("tool after interrupt = %+v", tool)
	}
}

func TestResumeReplaysSameTranscript(t *testing.T) {
	current := newHarness(t, newFakeClient(callBash("echo replay"), replyWith("done")))
	if err := current.engine.Send("go"); err != nil {
		t.Fatal(err)
	}
	current.waitForAssistant("done")
	id := current.engine.SessionID()

	current.engine.NewSession()
	sessions, err := current.engine.Sessions()
	if err != nil || len(sessions) != 1 || sessions[0].ID != id || sessions[0].Title != "go" {
		t.Fatalf("sessions = %+v, err %v", sessions, err)
	}
	items, err := current.engine.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	replayed := NewTranscript(current.engine.ResultTranslator)
	var blocks []Block
	for _, item := range items {
		blocks = append(blocks, replayed.Apply(item)...)
	}
	if len(blocks) != len(current.blocks) {
		t.Fatalf("replayed %d blocks, live had %d", len(blocks), len(current.blocks))
	}
	for index := range blocks {
		if blocks[index].Kind != current.blocks[index].Kind || blocks[index].Text != current.blocks[index].Text {
			t.Fatalf("block %d: replayed %+v, live %+v", index, blocks[index], current.blocks[index])
		}
	}
	if replayed.Usage != current.transcript.Usage {
		t.Fatalf("usage replayed %+v, live %+v", replayed.Usage, current.transcript.Usage)
	}
}

func TestEffortChangeReachesModel(t *testing.T) {
	current := newHarness(t, newFakeClient(replyWith("one"), replyWith("two")))
	if err := current.engine.Send("first"); err != nil {
		t.Fatal(err)
	}
	current.waitForAssistant("one")
	if err := current.engine.SetEffort(llm.ReasoningEffortMax); err != nil {
		t.Fatal(err)
	}
	if err := current.engine.Send("second"); err != nil {
		t.Fatal(err)
	}
	current.waitForAssistant("two")
	if effort := current.client.request(1).Model.ReasoningEffort; effort != llm.ReasoningEffortMax {
		t.Fatalf("effort = %q, want max", effort)
	}
}

// A model switch while a tool runs applies to the very next model call, with
// no restart: the follow-up request goes to the new client with the new model
// and prompt, and still carries the tool result.
func TestModelSwitchMidTurn(t *testing.T) {
	current := newHarness(t, newFakeClient(callBash("sleep 1; echo slept")))
	if err := current.engine.Send("go"); err != nil {
		t.Fatal(err)
	}
	current.waitForCall()
	next := newFakeClient(replyWith("from next"))
	current.engine.SetModel(next, "next-model")
	current.engine.SetSystemPrompt("next prompt")
	current.waitForAssistant("from next")

	request := next.request(0)
	if request.Model.ID != "next-model" {
		t.Fatalf("model = %q, want next-model", request.Model.ID)
	}
	var sawPrompt, sawResult bool
	for _, item := range request.Input {
		switch data := item.Data.(type) {
		case llm.Message:
			sawPrompt = sawPrompt || strings.Contains(data.Text, "next prompt")
		case llm.ToolResult:
			sawResult = true
		}
	}
	if !sawPrompt || !sawResult {
		t.Fatalf("prompt updated %v, tool result carried %v; input %+v", sawPrompt, sawResult, request.Input)
	}
	if calls := len(current.client.requests); calls != 1 {
		t.Fatalf("old client got %d calls, want 1", calls)
	}
}

func equalKinds(left, right []BlockKind) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}
