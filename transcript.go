package main

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type BlockKind int

const (
	BlockUser BlockKind = iota
	BlockAssistant
	BlockReasoning
	BlockTool
	BlockInfo
	BlockError
)

// Block is one finished piece of the conversation, ready to print.
type Block struct {
	Kind BlockKind
	Text string
	Tool *ToolView
}

type ToolState int

const (
	ToolRunning ToolState = iota
	ToolSucceeded
	ToolFailed
	ToolCanceled
)

type ToolView struct {
	CallID  string
	Name    string
	Title   string
	Output  string
	State   ToolState
	Started time.Time
}

type Usage struct {
	Input, Cached, Output int64
	// Context is the input size of the latest model call.
	Context int64
}

type callKey struct {
	turn session.TurnID
	call string
}

// Transcript folds persisted session items into display blocks and tracks
// what the agent is doing right now.
type Transcript struct {
	resolve  func(string) (tool.ResultTranslator, bool)
	calls    map[callKey]*ToolView
	order    []callKey
	turn     session.TurnID
	turnOpen bool
	Usage    Usage
}

func NewTranscript(resolve func(string) (tool.ResultTranslator, bool)) *Transcript {
	return &Transcript{resolve: resolve, calls: make(map[callKey]*ToolView)}
}

// Busy reports whether a model call or tool call is in flight.
func (transcript *Transcript) Busy() bool {
	return transcript.turnOpen || len(transcript.calls) != 0
}

func (transcript *Transcript) Thinking() bool {
	return transcript.turnOpen
}

// Running returns in-flight tool calls in the order they were issued.
func (transcript *Transcript) Running() []*ToolView {
	running := make([]*ToolView, 0, len(transcript.order))
	for _, key := range transcript.order {
		running = append(running, transcript.calls[key])
	}
	return running
}

// Abort drops in-flight state after the coordinator exits. Tool calls that did
// not report a result are returned as canceled so they still appear.
func (transcript *Transcript) Abort() []Block {
	var blocks []Block
	for _, view := range transcript.Running() {
		view.State = ToolCanceled
		if view.Output == "" {
			view.Output = "(stopped before completion)"
		}
		blocks = append(blocks, Block{Kind: BlockTool, Tool: view})
	}
	clear(transcript.calls)
	transcript.order = nil
	transcript.turnOpen = false
	return blocks
}

func (transcript *Transcript) Apply(item sessionstore.Item) []Block {
	switch data := item.Data.(type) {
	case inbox.Input:
		return transcript.applyInput(data)
	case session.Turn:
		transcript.turn = data.ID
		transcript.turnOpen = true
	case sessionstore.ModelResponse:
		return transcript.applyResponse(data, item.RecordedAt)
	case sessionstore.ToolCallStatus:
		return transcript.applyToolStatus(data)
	case sessionstore.Fork:
		return []Block{{Kind: BlockInfo, Text: "Forked from session " + string(data.ParentID)}}
	}
	return nil
}

func (transcript *Transcript) applyInput(input inbox.Input) []Block {
	switch input.Kind {
	case inbox.InputExternal:
		var text string
		if err := json.Unmarshal(input.Payload, &text); err != nil {
			text = string(input.Payload)
		}
		return []Block{{Kind: BlockUser, Text: text}}
	case inbox.InputControl:
		request, err := input.DecodeControlMessage()
		if err != nil {
			return nil
		}
		if request.Mode == inbox.StopHard {
			// A hard stop abandons the in-flight model call; no response follows.
			transcript.turnOpen = false
			if request.Reason == interruptReason {
				return []Block{{Kind: BlockInfo, Text: "Interrupted"}}
			}
		}
	}
	return nil
}

func (transcript *Transcript) applyResponse(response sessionstore.ModelResponse, recordedAt time.Time) []Block {
	if response.TurnID == transcript.turn {
		transcript.turnOpen = false
	}
	usage := response.Response.Usage
	transcript.Usage.Input += usage.InputTokens
	transcript.Usage.Cached += usage.CachedInputTokens
	transcript.Usage.Output += usage.OutputTokens
	transcript.Usage.Context = usage.InputTokens + usage.OutputTokens

	var blocks []Block
	for _, output := range response.Response.Output {
		switch data := output.Data.(type) {
		case llm.Reasoning:
			if summary := strings.TrimSpace(strings.Join(data.Summary, "\n\n")); summary != "" {
				blocks = append(blocks, Block{Kind: BlockReasoning, Text: summary})
			}
		case llm.Message:
			if text := strings.TrimSpace(data.Text); text != "" {
				blocks = append(blocks, Block{Kind: BlockAssistant, Text: text})
			}
		case llm.ToolCall:
			key := callKey{turn: response.TurnID, call: data.CallID}
			if _, exists := transcript.calls[key]; !exists {
				transcript.order = append(transcript.order, key)
			}
			transcript.calls[key] = &ToolView{
				CallID:  data.CallID,
				Name:    data.Name,
				Title:   toolTitle(data.Name, data.Arguments),
				Started: recordedAt,
			}
		}
	}
	if failure := response.Response.Failure; failure != nil {
		blocks = append(blocks, Block{Kind: BlockError, Text: fmt.Sprintf("Model error (%s): %s", failure.Code, failure.Message)})
	}
	switch response.Response.Stop {
	case llm.StopMaxOutputTokens:
		blocks = append(blocks, Block{Kind: BlockInfo, Text: "Response stopped at the output token limit."})
	case llm.StopRefused:
		blocks = append(blocks, Block{Kind: BlockError, Text: "The model refused to respond."})
	}
	return blocks
}

func (transcript *Transcript) applyToolStatus(status sessionstore.ToolCallStatus) []Block {
	key := callKey{turn: status.TurnID, call: status.CallID}
	view, exists := transcript.calls[key]
	if !exists {
		return nil // A result for a call from before a fork, or a duplicate.
	}

	operations := make(map[operation.ID]operation.Operation, len(status.Operations))
	for _, value := range status.Operations {
		operations[value.ID] = value
	}
	waiting := make([]operation.Operation, 0, len(status.Status.WaitingFor))
	state := ToolSucceeded
	if status.Status.Error != "" {
		state = ToolFailed
	}
	for _, id := range status.Status.WaitingFor {
		value, ok := operations[id]
		if !ok || !operationTerminal(value.Status) {
			return nil // Still running.
		}
		waiting = append(waiting, value)
		switch {
		case value.Status == operation.StatusCanceled:
			state = ToolCanceled
		case value.Status == operation.StatusFailed || shellExitCode(value) != 0:
			state = ToolFailed
		}
	}

	view.State = state
	view.Output = transcript.toolOutput(view.Name, status, waiting)
	if state == ToolCanceled {
		view.Output = canceledOutput(view.Output)
	}
	delete(transcript.calls, key)
	for index, candidate := range transcript.order {
		if candidate == key {
			transcript.order = append(transcript.order[:index], transcript.order[index+1:]...)
			break
		}
	}
	return []Block{{Kind: BlockTool, Tool: view}}
}

func (transcript *Transcript) toolOutput(name string, status sessionstore.ToolCallStatus, operations []operation.Operation) string {
	translator, ok := transcript.resolve(name)
	if !ok {
		if status.Status.Error != "" {
			return "Error: " + status.Status.Error
		}
		return ""
	}
	result, err := translator.TranslateResult(status.CallID, status.Status, operations)
	if err != nil {
		return "Error: " + err.Error()
	}
	var parts []string
	for _, output := range result.Output {
		switch output.Kind {
		case llm.ToolResultText:
			parts = append(parts, output.Value)
		case llm.ToolResultImage:
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

// canceledOutput drops the capture-file paths and cancellation error the Bash
// translator reports for the model, keeping any output the command produced.
func canceledOutput(output string) string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Stdout capture: ") || strings.HasPrefix(line, "Stderr capture: ") ||
			line == "Error: shell operation canceled" {
			continue
		}
		kept = append(kept, line)
	}
	if text := strings.TrimSpace(strings.Join(kept, "\n")); text != "" {
		return text
	}
	return "(canceled)"
}

func operationTerminal(status operation.Status) bool {
	switch status {
	case operation.StatusCompleted, operation.StatusFailed, operation.StatusCanceled:
		return true
	}
	return false
}

func shellExitCode(value operation.Operation) int {
	if value.Type != operation.TypeShell {
		return 0
	}
	state, err := operation.DecodeShellState(value)
	if err != nil || state.Result == nil {
		return 0
	}
	return state.Result.ExitCode
}

// toolTitle is the one-line summary shown next to the tool name.
func toolTitle(name, arguments string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return arguments
	}
	for _, field := range []string{"command", "path", "name"} {
		if value, ok := parsed[field].(string); ok {
			return value
		}
	}
	return arguments
}
