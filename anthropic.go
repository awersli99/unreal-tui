package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

const (
	anthropicBaseURL      = "https://api.anthropic.com"
	defaultAnthropicModel = "claude-sonnet-5"
)

// anthropicClient implements the harness adapter locally. No changes to the
// harness (or its Responses API clients) are needed for the Messages API.
type anthropicClient struct {
	http        *http.Client
	endpoint    string
	auth        anthropicAuth
	maxAttempts int
}

var _ Client = (*anthropicClient)(nil)

func newAnthropicClient(spec ProviderSpec, key string, maxAttempts int) (*anthropicClient, error) {
	if maxAttempts < 1 {
		return nil, errors.New("Anthropic max attempts must be positive")
	}
	var auth anthropicAuth
	var err error
	if spec.AuthFile != "" {
		auth = anthropicAuth{path: spec.AuthFile, tokenURL: anthropicTokenURL}
		_, _, err = readAnthropicCredential(spec.AuthFile)
	} else {
		auth, err = resolveAnthropicAuth(key, !spec.Custom)
	}
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(firstNonEmpty(spec.BaseURL, anthropicBaseURL), "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Anthropic baseUrl must be an http(s) URL without credentials, query or fragment")
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return &anthropicClient{
		http: &http.Client{
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
			Timeout:   10 * time.Minute,
			// Never forward credentials through an unexpected redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		endpoint: base + "/messages", auth: auth, maxAttempts: maxAttempts,
	}, nil
}

func (client *anthropicClient) Close() error {
	client.http.CloseIdleConnections()
	return nil
}

func (client *anthropicClient) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	key, err := client.auth.key(ctx, client.http)
	if err != nil {
		return llm.Response{}, err
	}
	oauth := isAnthropicOAuth(key)
	version := ""
	if oauth {
		version, err = anthropicClaudeCodeVersion()
		if err != nil {
			return llm.Response{}, err
		}
	}
	payload, err := anthropicRequest(request, oauth, version)
	if err != nil {
		return llm.Response{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return llm.Response{}, fmt.Errorf("encode Anthropic request: %w", err)
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
		if err != nil {
			return llm.Response{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Anthropic-Version", "2023-06-01")
		var betas []string
		if oauth {
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("User-Agent", "claude-cli/"+version)
			req.Header.Set("X-App", "cli")
			betas = append(betas, "claude-code-20250219", "oauth-2025-04-20")
		} else {
			req.Header.Set("X-Api-Key", key)
			req.Header.Set("User-Agent", "unreal-tui")
		}
		if payload.Thinking != nil && payload.Thinking.Type == "enabled" {
			betas = append(betas, "interleaved-thinking-2025-05-14")
		}
		if payload.Thinking != nil && payload.Thinking.BlockBinding != nil {
			betas = append(betas, "mid-conversation-output-config-2026-07-01", "thinking-binding-controls-2026-08-01")
		}
		if len(betas) != 0 {
			req.Header.Set("Anthropic-Beta", strings.Join(betas, ","))
		}
		response, callErr := client.http.Do(req)
		var headers http.Header
		retry := true
		if callErr == nil {
			headers = response.Header
			encoded, readErr := readAnthropicBody(response.Body, 32<<20)
			response.Body.Close()
			callErr = readErr
			if readErr == nil {
				if response.StatusCode == http.StatusOK {
					decoded, err := decodeAnthropicResponse(encoded, request.Tools)
					origin := "anthropic/" + request.Model.ID
					managed := payload.Thinking != nil && payload.Thinking.BlockBinding != nil
					if managed {
						origin += ":" + anthropicEffort(request.Model)
					}
					for index := range decoded.Output {
						// Persist effort even when Claude chooses not to think. Later
						// signed blocks bind the whole prefix, including those turns.
						if decoded.Output[index].Type == llm.ItemReasoning || managed {
							decoded.Output[index].ProviderID = origin
						}
					}
					return decoded, err
				}
				callErr = anthropicHTTPError(response.StatusCode, encoded, key)
				retry = response.StatusCode == 408 || response.StatusCode == 409 || response.StatusCode == 429 || response.StatusCode >= 500
			}
		}
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		if !retry || attempt+1 >= client.maxAttempts {
			return llm.Response{}, callErr
		}
		timer := time.NewTimer(anthropicRetryDelay(attempt, headers))
		select {
		case <-ctx.Done():
			timer.Stop()
			return llm.Response{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func readAnthropicBody(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read Anthropic response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, errors.New("Anthropic response exceeds size limit")
	}
	return body, nil
}

func anthropicHTTPError(status int, body []byte, key string) error {
	var envelope struct {
		Error struct{ Type, Message string }
	}
	message := http.StatusText(status)
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		message = envelope.Error.Type + ": " + envelope.Error.Message
	}
	// An upstream proxy may echo credentials. Never include those in the TUI.
	message = strings.ReplaceAll(message, key, "[redacted]")
	return fmt.Errorf("Anthropic HTTP %d: %s", status, message)
}

func anthropicRetryDelay(attempt int, headers http.Header) time.Duration {
	if value := headers.Get("Retry-After"); value != "" {
		if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
			return time.Duration(min(seconds, 60) * float64(time.Second))
		}
		if until, err := http.ParseTime(value); err == nil {
			return min(time.Minute, max(0, time.Until(until)))
		}
	}
	return min(30*time.Second, 250*time.Millisecond*time.Duration(1<<min(attempt, 7)))
}

func decodeAnthropicResponse(body []byte, tools []llm.Tool) (llm.Response, error) {
	var wire struct {
		ID         string            `json:"id"`
		Type       string            `json:"type"`
		Content    []json.RawMessage `json:"content"`
		StopReason string            `json:"stop_reason"`
		Usage      json.RawMessage   `json:"usage"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return llm.Response{}, fmt.Errorf("decode Anthropic response: %w", err)
	}
	response := llm.Response{ID: wire.ID, Stop: llm.StopComplete}
	if wire.Type != "message" || wire.ID == "" {
		return response, errors.New("expected an Anthropic message response with an id")
	}
	switch wire.StopReason {
	case "end_turn", "tool_use", "stop_sequence":
	case "max_tokens", "model_context_window_exceeded":
		response.Stop = llm.StopMaxOutputTokens
	case "refusal", "sensitive":
		response.Stop = llm.StopRefused
	default:
		return response, fmt.Errorf("unsupported Anthropic stop reason %q", wire.StopReason)
	}
	for _, raw := range wire.Content {
		var block struct {
			Type, ID, Text, Name, Thinking string
			Input                          json.RawMessage
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return response, fmt.Errorf("decode Anthropic content: %w", err)
		}
		item := llm.Item{}
		switch block.Type {
		case "text":
			item.Type, item.Data = llm.ItemMessage, llm.Message{Role: llm.RoleAssistant, Text: block.Text}
		case "tool_use":
			if block.ID == "" || block.Name == "" {
				return response, errors.New("Anthropic tool_use must include an id and name")
			}
			name := block.Name
			for _, tool := range tools {
				if strings.EqualFold(tool.Name, name) {
					name = tool.Name
					break
				}
			}
			item.Type, item.Data = llm.ItemToolCall, llm.ToolCall{CallID: block.ID, Name: name, Arguments: string(block.Input)}
		case "thinking", "redacted_thinking":
			reasoning := llm.Reasoning{Raw: jsontext.Value(raw)}
			if block.Thinking != "" {
				reasoning.Summary = []string{block.Thinking}
			}
			item.Type, item.Data = llm.ItemReasoning, reasoning
		default:
			return response, fmt.Errorf("unsupported Anthropic content block %q", block.Type)
		}
		response.Output = append(response.Output, item)
	}
	var usage struct {
		Input  int64 `json:"input_tokens"`
		Output int64 `json:"output_tokens"`
		Read   int64 `json:"cache_read_input_tokens"`
		Write  int64 `json:"cache_creation_input_tokens"`
	}
	if err := json.Unmarshal(wire.Usage, &usage); err != nil {
		return response, fmt.Errorf("decode Anthropic usage: %w", err)
	}
	response.Usage = llm.Usage{InputTokens: usage.Input + usage.Read + usage.Write,
		CachedInputTokens: usage.Read, CacheWriteInputTokens: usage.Write,
		OutputTokens: usage.Output, Raw: jsontext.Value(wire.Usage)}
	return response, nil
}
