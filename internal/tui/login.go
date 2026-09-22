package tui

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/awersli99/unreal-tui/internal/config"
	"github.com/awersli99/unreal-tui/internal/engine"
	"github.com/awersli99/unreal-tui/internal/provider"
)

const (
	loginAccount = "account"
	loginAPIKey  = "api_key"
)

// promptInput is a one-line text prompt shown in place of the editor, such
// as the masked API key entry of /login.
type promptInput struct {
	title  string
	hint   string
	input  textinput.Model
	submit func(value string) tea.Cmd
	cancel func()
}

func newPromptInput(title, hint string, secret bool, submit func(string) tea.Cmd) *promptInput {
	input := textinput.New()
	input.Prompt = "  "
	input.CharLimit = 0
	if secret {
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '•'
	}
	input.Focus()
	return &promptInput{title: title, hint: hint, input: input, submit: submit}
}

func (prompt *promptInput) View(width int) string {
	title := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(prompt.title)
	prompt.input.Width = max(10, width-4)
	return title + "\n" + prompt.input.View() + "\n" + dimStyle.Render("  "+firstLine(prompt.hint, max(10, width-3)))
}

func (current *model) handlePromptKey(prompt *promptInput, message tea.KeyMsg) tea.Cmd {
	switch message.String() {
	case "esc", "ctrl+c":
		if prompt.cancel != nil {
			prompt.cancel()
		}
		current.overlay = nil
		return nil
	case "enter":
		current.overlay = nil
		return prompt.submit(prompt.input.Value())
	}
	var command tea.Cmd
	prompt.input, command = prompt.input.Update(message)
	return command
}

// loginOption is a provider that /login can configure.
type loginOption struct {
	provider string
	method   string // loginAccount or loginAPIKey
	label    string
	detail   string
}

// loginOptions lists what /login offers: the ChatGPT account login through
// the official Codex CLI, native Anthropic OAuth, and API keys.
func (current *model) loginOptions(method string) []loginOption {
	app := current.app
	var options []loginOption
	if method == "" || method == loginAccount {
		detail := "ChatGPT Plus/Pro via the official Codex CLI (codex login)"
		if _, err := exec.LookPath("codex"); err != nil {
			detail = "needs the Codex CLI on your PATH"
		} else if provider.CodexAuthExists() {
			detail = "signed in · " + detail
		}
		options = append(options, loginOption{provider: "openai-codex", method: loginAccount, label: "OpenAI Codex (ChatGPT account)", detail: detail})
		if spec, ok := app.Catalog.Provider("anthropic"); ok {
			detail := "Claude subscription via browser OAuth (no external CLI)"
			if spec.AuthFile != "" {
				detail = "signed in · " + detail
			}
			options = append(options, loginOption{provider: "anthropic", method: loginAccount, label: "Anthropic (Claude account)", detail: detail})
		}
	}
	if method == "" || method == loginAPIKey {
		for _, spec := range app.Catalog.Providers {
			if !spec.NeedsKey() {
				continue
			}
			var status string
			switch {
			case spec.StoredKey != "":
				status = "key saved"
			case spec.APIKeyEnv != "" && os.Getenv(spec.APIKeyEnv) != "":
				status = "$" + spec.APIKeyEnv + " set"
			case spec.APIKey != "":
				status = "apiKey in " + config.ModelsFileName
			case spec.Available():
				status = "credentials found elsewhere"
			default:
				status = "not configured"
			}
			options = append(options, loginOption{provider: spec.Name, method: loginAPIKey, label: spec.Name, detail: status})
		}
	}
	return options
}

// openLogin runs /login [provider], following pi: pick a sign-in method,
// then a provider, then sign in.
func (current *model) openLogin(query string) tea.Cmd {
	if query = strings.TrimSpace(query); query != "" {
		var matches []loginOption
		for _, option := range current.loginOptions("") {
			if option.provider == query {
				return current.startLogin(option)
			}
			if provider.FuzzyMatch(option.provider+" "+option.label, query) {
				matches = append(matches, option)
			}
		}
		if len(matches) == 1 {
			return current.startLogin(matches[0])
		}
		current.openLoginProviders("", query)
		return nil
	}
	items := []selectorItem{
		{Label: "Sign in with an account", Detail: "Claude subscription in your browser, or ChatGPT via the Codex CLI", Value: loginAccount},
		{Label: "Sign in with an API key", Detail: "saved to " + shortenHome(config.AuthPath(current.app.Config.Home)), Value: loginAPIKey},
	}
	current.overlay = &overlay{
		selector: newSelector("Select authentication method", "enter select · esc cancel", items, false),
		choose: func(item *selectorItem) tea.Cmd {
			current.openLoginProviders(item.Value.(string), "")
			return nil
		},
	}
	return nil
}

func (current *model) openLoginProviders(method, query string) {
	var items []selectorItem
	for _, option := range current.loginOptions(method) {
		items = append(items, selectorItem{Label: option.label, Detail: option.detail, Value: option})
	}
	picker := newSelector("Select provider to configure", "type to filter · enter select · esc cancel", items, true)
	if query != "" {
		picker.setQuery(query)
	}
	current.overlay = &overlay{
		selector: picker,
		choose: func(item *selectorItem) tea.Cmd {
			return current.startLogin(item.Value.(loginOption))
		},
	}
}

type loginFinishedMsg struct {
	provider string
	logout   bool
	err      error
}

func (current *model) startLogin(option loginOption) tea.Cmd {
	if option.method == loginAccount {
		if option.provider == "anthropic" {
			return current.startAnthropicLogin()
		}
		return current.runCodex("login", option.provider, false)
	}
	home := current.app.Config.Home
	title := "API key for " + option.provider
	hint := "paste the key and press enter · esc cancel · saved to " + shortenHome(config.AuthPath(home))
	current.overlay = &overlay{prompt: newPromptInput(title, hint, true, func(value string) tea.Cmd {
		key := strings.TrimSpace(value)
		if err := config.SaveAPIKey(home, option.provider, key); err != nil {
			return current.print(engine.Block{Kind: engine.BlockError, Text: "Save API key: " + err.Error()})
		}
		return current.finishLogin(option.provider, fmt.Sprintf("Saved API key for %s", option.provider))
	})}
	return nil
}

// runCodex hands the terminal to the official Codex CLI for login or logout.
func (current *model) runCodex(action, name string, logout bool) tea.Cmd {
	path, err := exec.LookPath("codex")
	if err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: "The Codex CLI is not on your PATH; install it to sign in with a ChatGPT account."})
	}
	// The interactive CLI owns the terminal until the user finishes with it.
	return tea.ExecProcess(exec.Command(path, action), func(err error) tea.Msg { //nolint:noctx // Interactive.
		return loginFinishedMsg{provider: name, logout: logout, err: err}
	})
}

func (current *model) handleLoginFinished(message loginFinishedMsg) tea.Cmd {
	action := "codex login"
	if message.logout {
		action = "codex logout"
	}
	if message.err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: fmt.Sprintf("%s failed: %v", action, message.err)})
	}
	if message.logout {
		return current.finishLogout(message.provider, "Signed out of OpenAI Codex")
	}
	return current.finishLogin(message.provider, "Signed in to OpenAI Codex")
}

// finishLogin reloads providers so the new credentials apply right away.
func (current *model) finishLogin(name, text string) tea.Cmd {
	blocks := current.refreshProviders()
	count := 0
	for _, model := range current.app.Catalog.Models {
		if model.Provider == name {
			count++
		}
	}
	switch {
	case current.app.Model.Provider == name:
	case count != 0:
		text += fmt.Sprintf(" · %d models; ctrl+l to switch", count)
	default:
		text += fmt.Sprintf(" · use /model %s/<model-id>, or list its models in %s", name, config.ModelsFileName)
	}
	return current.print(append(blocks, engine.Block{Kind: engine.BlockInfo, Text: text})...)
}

func (current *model) finishLogout(name, text string) tea.Cmd {
	blocks := current.refreshProviders()
	if current.app.Model.Provider == name {
		if spec, ok := current.app.Catalog.Provider(name); ok && !spec.Available() {
			text += "; the current model has no credentials now, so pick another with ctrl+l"
		}
	}
	return current.print(append(blocks, engine.Block{Kind: engine.BlockInfo, Text: text})...)
}

// openLogout lists the credentials /logout can remove, as pi does: keys saved
// by /login (API keys or OAuth), and the Codex CLI login.
func (current *model) openLogout(query string) tea.Cmd {
	home := current.app.Config.Home
	providers, err := config.StoredProviders(home)
	if err != nil {
		return current.print(engine.Block{Kind: engine.BlockError, Text: err.Error()})
	}
	var items []selectorItem
	for _, provider := range providers {
		items = append(items, selectorItem{Label: provider, Detail: "remove the credentials saved in " + shortenHome(config.AuthPath(home)), Value: provider})
	}
	if _, err := exec.LookPath("codex"); err == nil && provider.CodexAuthExists() && !slices.Contains(providers, "openai-codex") {
		items = append(items, selectorItem{Label: "openai-codex", Detail: "runs codex logout, which also signs out the Codex CLI", Value: "openai-codex"})
	}
	if len(items) == 0 {
		current.notice = "No stored credentials to remove. Environment variables and models.json are unchanged by /logout."
		return nil
	}
	remove := func(provider string) tea.Cmd {
		if slices.Contains(providers, provider) {
			if _, err := config.RemoveCredential(home, provider); err != nil {
				return current.print(engine.Block{Kind: engine.BlockError, Text: "Logout: " + err.Error()})
			}
			return current.finishLogout(provider, fmt.Sprintf("Removed the stored credentials for %s. Environment variables, models.json and external auth files are unchanged.", provider))
		}
		return current.runCodex("logout", provider, true)
	}
	if query = strings.TrimSpace(query); query != "" {
		for _, item := range items {
			if item.Label == query {
				return remove(query)
			}
		}
	}
	picker := newSelector("Select provider to log out", "enter log out · esc cancel", items, true)
	if query != "" {
		picker.setQuery(query)
	}
	current.overlay = &overlay{
		selector: picker,
		choose: func(item *selectorItem) tea.Cmd {
			return remove(item.Value.(string))
		},
	}
	return nil
}
