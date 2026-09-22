package anthropic

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/unreallabsai/unreal-agent/harness/llm"

	"github.com/awersli99/unreal-tui/internal/config"
)

// OAuth compatibility is adapted from gotgenes/pi-anthropic-auth at
// d83b174f52a00fd7a0d89fbcd4abb4253f3c4e44. See THIRD_PARTY_NOTICES.md.
const anthropicCCVersion = "2.1.280"

const anthropicCCVersionEnv = "PI_ANTHROPIC_AUTH_CLAUDE_CODE_VERSION"

const anthropicCCIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

var anthropicVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

var anthropicToolIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	ID           string                 `json:"id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        json.RawMessage        `json:"input,omitempty"`
	ToolUseID    string                 `json:"tool_use_id,omitempty"`
	Content      []anthropicBlock       `json:"content,omitempty"`
	Source       *anthropicImageSource  `json:"source,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
	// Signed thinking blocks are opaque. Keep every field, including future
	// fields, and never rebuild their contents from a displayed summary.
	Raw json.RawMessage `json:"-"`
}

func (block anthropicBlock) MarshalJSON() ([]byte, error) {
	if len(block.Raw) != 0 {
		return block.Raw, nil
	}
	type plain anthropicBlock
	return json.Marshal(plain(block))
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type anthropicMessage struct {
	Role         string            `json:"role"`
	Content      []anthropicBlock  `json:"content"`
	OutputConfig map[string]string `json:"output_config,omitempty"`
	// Local-only metadata used to reconstruct per-turn effort markers.
	Effort string `json:"-"`
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  map[string]any         `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicThinking struct {
	Type         string                    `json:"type"`
	Budget       int64                     `json:"budget_tokens,omitempty"`
	Display      string                    `json:"display,omitempty"`
	BlockBinding *anthropicThinkingBinding `json:"block_binding,omitempty"`
}

type anthropicThinkingBinding struct {
	PrefixMismatchBehavior string `json:"prefix_mismatch_behavior"`
}

type anthropicPayload struct {
	Model        string             `json:"model"`
	MaxTokens    int64              `json:"max_tokens"`
	Stream       bool               `json:"stream"`
	System       []anthropicBlock   `json:"system,omitempty"`
	Messages     []anthropicMessage `json:"messages"`
	Tools        []anthropicTool    `json:"tools,omitempty"`
	Thinking     *anthropicThinking `json:"thinking,omitempty"`
	OutputConfig map[string]string  `json:"output_config,omitempty"`
}

func anthropicText(text string) anthropicBlock { return anthropicBlock{Type: "text", Text: text} }

func anthropicRequest(request llm.Request, oauth bool, version string) (anthropicPayload, error) {
	payload := anthropicPayload{Model: request.Model.ID, MaxTokens: 32768}
	if strings.TrimSpace(payload.Model) == "" {
		return payload, errors.New("Anthropic model must be set")
	}
	if request.Model.MaxOutputTokens != nil {
		payload.MaxTokens = *request.Model.MaxOutputTokens
	}
	if payload.MaxTokens <= 0 {
		return payload, errors.New("Anthropic max output tokens must be positive")
	}
	capabilities, known := ModelForID(payload.Model)
	if known && payload.MaxTokens > capabilities.MaxOutputTokens {
		return payload, fmt.Errorf("%s supports at most %d output tokens", payload.Model, capabilities.MaxOutputTokens)
	}
	effort := request.Model.ReasoningEffort
	if effort != "" && !effort.Valid() {
		return payload, fmt.Errorf("unsupported Anthropic reasoning effort %q", effort)
	}
	if capabilities.ManagedEffort {
		// Newer models bind thinking to the preceding prompt/configuration.
		// Keep the initial effort stable and append changes inside messages,
		// matching Pi's native effort/binding protocol instead of invalidating
		// historical signatures when /thinking changes.
		payload.Thinking = &anthropicThinking{Type: "adaptive", Display: "summarized", BlockBinding: &anthropicThinkingBinding{PrefixMismatchBehavior: "drop_block"}}
		payload.OutputConfig = map[string]string{"effort": "high"}
	} else if effort != "" {
		if anthropicAdaptive(payload.Model) {
			payload.Thinking = &anthropicThinking{Type: "adaptive", Display: "summarized"}
			payload.OutputConfig = map[string]string{"effort": anthropicEffort(request.Model)}
		} else {
			budget := map[llm.ReasoningEffort]int64{"low": 1024, "medium": 4096, "high": 8192, "xhigh": 16384, "max": 24576}[effort]
			budget = min(budget, payload.MaxTokens-1)
			if budget < 1024 {
				return payload, errors.New("Anthropic extended thinking requires max output tokens greater than 1024")
			}
			payload.Thinking = &anthropicThinking{Type: "enabled", Budget: budget, Display: "summarized"}
		}
	}
	appendBlock := func(role string, block anthropicBlock) {
		last := len(payload.Messages) - 1
		if last < 0 || payload.Messages[last].Role != role {
			payload.Messages = append(payload.Messages, anthropicMessage{Role: role})
			last++
		}
		payload.Messages[last].Content = append(payload.Messages[last].Content, block)
	}
	for index, item := range request.Input {
		if err := item.Validate(); err != nil {
			return payload, fmt.Errorf("Anthropic input %d: %w", index, err)
		}
		originModel, historicalEffort := anthropicOrigin(item.ProviderID)
		switch data := item.Data.(type) {
		case llm.Message:
			if strings.TrimSpace(data.Text) == "" {
				continue
			}
			switch data.Role {
			case llm.RoleSystem:
				text := data.Text
				if oauth {
					// Only replace the harness-owned identity at the start. Keep
					// all instructions, skills and user/project context intact.
					const identity = "You run on Unreal Agent Harness built by Unreal Labs."
					if strings.HasPrefix(text, identity) {
						text = "You are an expert coding assistant." + strings.TrimPrefix(text, identity)
					}
				}
				payload.System = append(payload.System, anthropicText(text))
			case llm.RoleUser, llm.RoleAssistant:
				appendBlock(string(data.Role), anthropicText(data.Text))
			default:
				return payload, fmt.Errorf("unsupported Anthropic message role %q", data.Role)
			}
		case llm.ToolCall:
			arguments := json.RawMessage(data.Arguments)
			if !json.Valid(arguments) || !strings.HasPrefix(strings.TrimSpace(data.Arguments), "{") {
				arguments, _ = json.Marshal(map[string]string{"invalid_arguments": data.Arguments})
			}
			name := data.Name
			if oauth {
				name = anthropicToolName(name)
			}
			appendBlock("assistant", anthropicBlock{Type: "tool_use", ID: anthropicToolID(data.CallID), Name: name, Input: arguments})
		case llm.ToolResult:
			content, err := anthropicToolOutput(data.Output)
			if err != nil {
				return payload, fmt.Errorf("tool result %s: %w", data.CallID, err)
			}
			appendBlock("user", anthropicBlock{Type: "tool_result", ToolUseID: anthropicToolID(data.CallID), Content: content})
		case llm.Reasoning:
			if isAnthropicReasoning(data) && (item.ProviderID == "" || originModel == request.Model.ID) {
				var block anthropicBlock
				if err := json.Unmarshal(data.Raw, &block); err != nil {
					return payload, err
				}
				block.Raw = data.Raw
				appendBlock("assistant", block)
			}
			// Other providers' encrypted reasoning cannot be replayed here.
		}
		if originModel == request.Model.ID && historicalEffort != "" && len(payload.Messages) != 0 {
			last := &payload.Messages[len(payload.Messages)-1]
			if last.Role == "assistant" && last.Effort == "" {
				last.Effort = historicalEffort
			}
		}
	}
	payload.Messages = anthropicToolMessages(payload.Messages)
	if len(payload.Messages) == 0 || payload.Messages[0].Role != "user" {
		return payload, errors.New("Anthropic conversation must start with a user message")
	}
	for _, tool := range request.Tools {
		if tool.Type != llm.ToolFunction {
			return payload, fmt.Errorf("Anthropic does not support harness tool type %q", tool.Type)
		}
		name := tool.Name
		if oauth {
			name = anthropicToolName(name)
		}
		schema := tool.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		payload.Tools = append(payload.Tools, anthropicTool{Name: name, Description: tool.Description, InputSchema: schema})
	}
	if oauth {
		prefix := []anthropicBlock{anthropicText(anthropicCCIdentity)}
		if text := anthropicFirstUserText(payload.Messages); text != "" {
			prefix = append([]anthropicBlock{anthropicText(anthropicBillingHeader(text, version))}, prefix...)
		}
		payload.System = append(prefix, payload.System...)
	}
	// At most three cache breakpoints. The billing block is deliberately never
	// cache-controlled, matching pi-anthropic-auth.
	cache := &anthropicCacheControl{Type: "ephemeral"}
	if len(payload.System) != 0 {
		payload.System[len(payload.System)-1].CacheControl = cache
	}
	if len(payload.Tools) != 0 {
		payload.Tools[len(payload.Tools)-1].CacheControl = cache
	}
	last := &payload.Messages[len(payload.Messages)-1]
	if last.Role == "user" && len(last.Content) != 0 {
		last.Content[len(last.Content)-1].CacheControl = cache
	}
	if capabilities.ManagedEffort {
		payload.Messages = anthropicEffortMessages(payload.Messages, anthropicEffort(request.Model))
	}
	return payload, nil
}

func isAnthropicReasoning(reasoning llm.Reasoning) bool {
	var block struct{ Type string }
	return json.Unmarshal(reasoning.Raw, &block) == nil && (block.Type == "thinking" || block.Type == "redacted_thinking")
}

// Anthropic requires exactly one tool_result per tool_use, at the start of
// the immediately following user message. The harness can deliver steering
// first, incomplete batches, or later updates to a running call. Keep those
// updates as labelled user content, rather than illegal duplicate results.
// Never reorder assistant blocks: their thinking signatures bind that order.
func anthropicToolMessages(messages []anthropicMessage) []anthropicMessage {
	var result []anthropicMessage
	var pending []string
	for _, message := range messages {
		if message.Role == "assistant" {
			for _, block := range message.Content {
				if block.Type == "tool_use" {
					pending = append(pending, block.ID)
				}
			}
			result = append(result, message)
			continue
		}
		used := make(map[int]bool)
		var content []anthropicBlock
		for _, id := range pending {
			block := anthropicBlock{Type: "tool_result", ToolUseID: id, Content: []anthropicBlock{anthropicText("Tool result not yet available (the call may still be running or was interrupted).")}}
			for index, candidate := range message.Content {
				if !used[index] && candidate.Type == "tool_result" && candidate.ToolUseID == id {
					block = candidate
					used[index] = true
					break
				}
			}
			content = append(content, block)
		}
		for index, block := range message.Content {
			if used[index] {
				continue
			}
			if block.Type == "tool_result" {
				content = append(content, anthropicText("Tool result update ("+block.ToolUseID+"):"))
				content = append(content, block.Content...)
			} else {
				content = append(content, block)
			}
		}
		result = append(result, anthropicMessage{Role: "user", Content: content})
		pending = nil
	}
	if len(pending) != 0 {
		// Also repair interrupted histories ending directly after tool_use.
		return anthropicToolMessages(append(messages, anthropicMessage{Role: "user"}))
	}
	return result
}

func anthropicToolOutput(outputs []llm.ToolResultOutput) ([]anthropicBlock, error) {
	var blocks []anthropicBlock
	for _, output := range outputs {
		switch output.Kind {
		case llm.ToolResultText:
			if output.Value != "" {
				blocks = append(blocks, anthropicText(output.Value))
			}
		case llm.ToolResultImage:
			var source anthropicImageSource
			if strings.HasPrefix(output.Value, "data:") {
				media, data, ok := strings.Cut(strings.TrimPrefix(output.Value, "data:"), ";base64,")
				if !ok || data == "" {
					return nil, errors.New("invalid base64 image data URL")
				}
				switch media {
				case "image/png", "image/jpeg", "image/gif", "image/webp":
				default:
					return nil, fmt.Errorf("unsupported Anthropic image type %q", media)
				}
				source = anthropicImageSource{Type: "base64", MediaType: media, Data: data}
			} else {
				parsed, err := url.Parse(output.Value)
				if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
					return nil, errors.New("image must be a data URL or http(s) URL")
				}
				source = anthropicImageSource{Type: "url", URL: output.Value}
			}
			blocks = append(blocks, anthropicBlock{Type: "image", Source: &source})
		default:
			return nil, fmt.Errorf("unsupported tool result kind %q", output.Kind)
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, anthropicText("(no output)"))
	}
	return blocks, nil
}

func anthropicToolID(id string) string {
	if anthropicToolIDPattern.MatchString(id) {
		return id
	}
	return fmt.Sprintf("toolu_%x", sha256.Sum256([]byte(id)))[:64]
}

func anthropicToolName(name string) string {
	for _, canonical := range []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob", "AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "KillShell", "NotebookEdit", "Skill", "Task", "TaskOutput", "TodoWrite", "WebFetch", "WebSearch"} {
		if strings.EqualFold(name, canonical) {
			return canonical
		}
	}
	return name
}

func anthropicClaudeCodeVersion() (string, error) {
	version := config.FirstNonEmpty(os.Getenv(anthropicCCVersionEnv), anthropicCCVersion)
	if !anthropicVersionPattern.MatchString(version) {
		return "", fmt.Errorf("%s must be a bare X.Y.Z version", anthropicCCVersionEnv)
	}
	return version, nil
}

func anthropicFirstUserText(messages []anthropicMessage) string {
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		for _, block := range message.Content {
			if block.Type == "text" {
				return block.Text
			}
		}
		return ""
	}
	return ""
}

func anthropicBillingHeader(text, version string) string {
	// JavaScript samples UTF-16 code units, not UTF-8 bytes or Unicode runes.
	// Decode the sampled sequence together so surrogate pairs match Node's
	// UTF-8 encoding (unpaired surrogates become U+FFFD).
	units := utf16.Encode([]rune(text))
	var sample []uint16
	for _, index := range []int{4, 7, 20} {
		unit := uint16('0')
		if index < len(units) {
			unit = units[index]
		}
		sample = append(sample, unit)
	}
	suffix := fmt.Sprintf("%x", sha256.Sum256([]byte("59cf53e54c78"+string(utf16.Decode(sample))+version)))[:3]
	cch := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))[:5]
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=sdk-cli; cch=%s;", version, suffix, cch)
}

// Remove Anthropic's opaque reasoning and local effort metadata when switching
// to a harness client. Requests that never used Anthropic are unchanged.
func WithoutState(input []llm.Item) []llm.Item {
	var filtered []llm.Item
	for index, item := range input {
		reasoning, ok := item.Data.(llm.Reasoning)
		drop := ok && isAnthropicReasoning(reasoning)
		origin, _ := anthropicOrigin(item.ProviderID)
		if (drop || origin != "") && filtered == nil {
			filtered = make([]llm.Item, 0, len(input))
			filtered = append(filtered, input[:index]...)
		}
		if !drop && filtered != nil {
			if origin != "" {
				item.ProviderID = ""
			}
			filtered = append(filtered, item)
		}
	}
	if filtered == nil {
		return input
	}
	return filtered
}

func anthropicEffort(model llm.Model) string {
	effort := config.FirstNonEmpty(string(model.ReasoningEffort), "high")
	if effort == "xhigh" && !anthropicXHigh(model.ID) {
		return "high"
	}
	return effort
}

func anthropicOrigin(id string) (model, effort string) {
	model, ok := strings.CutPrefix(id, "anthropic/")
	if !ok {
		return "", ""
	}
	if index := strings.LastIndex(model, ":"); index > 0 && config.ValidThinking(model[index+1:]) {
		return model[:index], model[index+1:]
	}
	return model, "" // Sessions recorded before effort metadata was added.
}

func anthropicEffortMessages(messages []anthropicMessage, current string) []anthropicMessage {
	marker := func(effort string) anthropicMessage {
		return anthropicMessage{Role: "system", Content: []anthropicBlock{}, OutputConfig: map[string]string{"effort": effort}}
	}
	var result []anthropicMessage
	for _, message := range messages {
		if message.Role == "assistant" && message.Effort != "" {
			result = append(result, marker(message.Effort))
		}
		result = append(result, message)
	}
	return append(result, marker(current))
}
