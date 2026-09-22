package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/awersli99/unreal-tui/internal/anthropic"
	"github.com/awersli99/unreal-tui/internal/engine"
)

type anthropicLoginFinishedMsg struct {
	flow       *anthropic.Login
	credential anthropic.Credential
	err        error
}

func (current *model) startAnthropicLogin() tea.Cmd {
	if current.anthropicLogin != nil {
		current.anthropicLogin.Cancel()
		current.anthropicLogin = nil
		current.overlay = nil
	}
	flow, err := anthropic.NewLogin(current.engine.Context(), anthropic.LoginConfig{})
	if err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
	}
	return current.showAnthropicLogin(flow)
}

func (current *model) showAnthropicLogin(flow *anthropic.Login) tea.Cmd {
	current.anthropicLogin = flow
	prompt := newPromptInput("Sign in to Anthropic (Claude account)", "finish in your browser or paste the redirect URL/code · enter submit · esc cancel", true, nil)
	prompt.cancel = func() {
		flow.Cancel()
		current.anthropicLogin = nil
		current.notice = "Anthropic login canceled"
	}
	prompt.submit = func(value string) tea.Cmd {
		prompt.input.SetValue("")
		current.overlay = &overlay{prompt: prompt}
		if err := flow.Submit(value); err != nil {
			return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
		}
		prompt.title = "Completing Anthropic sign-in…"
		return nil
	}
	current.overlay = &overlay{prompt: prompt}
	return tea.Batch(
		current.print(engine.Block{Kind: engine.BlockInfo, Text: "Sign in to Anthropic in your browser (no Pi installation needed):\n" + flow.URL()}),
		func() tea.Msg {
			anthropic.OpenBrowser(flow.Context(), flow.URL())
			credential, err := flow.Wait()
			return anthropicLoginFinishedMsg{flow: flow, credential: credential, err: err}
		},
	)
}

func (current *model) handleAnthropicLoginFinished(message anthropicLoginFinishedMsg) tea.Cmd {
	// A canceled or superseded flow must never save a late-arriving token.
	if message.flow == nil || current.anthropicLogin != message.flow {
		return nil
	}
	current.anthropicLogin = nil
	current.overlay = nil
	message.flow.Cancel()
	if message.err != nil {
		text := "Anthropic login failed: " + message.err.Error()
		if errors.Is(message.err, context.DeadlineExceeded) {
			text = "Anthropic login timed out; run /login anthropic to try again"
		}
		return current.print(engine.Block{Kind: engine.BlockError, Text: text})
	}
	if err := anthropic.SaveCredential(current.app.Config.Home, message.credential); err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: "Save Anthropic login: " + err.Error()})
	}
	return current.finishLogin("anthropic", "Signed in to Anthropic (Claude account)")
}
