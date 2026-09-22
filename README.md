# unreal

**An interactive terminal coding agent built on the [Unreal Agent Harness](https://github.com/unreallabsai/unreal-agent).**

`unreal` is a keyboard-driven TUI in the style of [pi](https://github.com/earendil-works/pi).
It puts a chat-style coding agent in your terminal, on top of the harness's
asynchronous coordinator. The agent works in your project directory by running
shell commands. You can steer it mid-task, interrupt it, and resume any past
session later.

```sh
unreal "fix the failing tests"
```

## Contents

- [Features](#features)
- [Requirements](#requirements)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Usage](#usage)
- [Configuration](#configuration)
- [Providers and credentials](#providers-and-credentials)
- [How it works](#how-it-works)
- [Security](#security)
- [Limitations](#limitations)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)
- [Acknowledgements](#acknowledgements)

## Features

- **Steerable, asynchronous agent.** Messages you send while the agent is working
  go straight to it. Tool calls run in parallel, and each running command appears live.
- **Hard interrupts.** `esc` cancels the in-flight model call and any running
  shell commands. Your next message picks the session back up.
- **Persistent sessions.** Sessions are saved per workspace as append-only
  files. Continue the latest one (`-c`), pick one from a list (`-r`), or resume
  one by ID.
- **Many providers.** Supports OpenAI, ChatGPT/Codex login, Anthropic (API key or Claude
  subscription via browser OAuth), OpenRouter, Fireworks, Ollama, and any
  compatible endpoint you define in `models.json`.
- **Fast model switching.** Includes a fuzzy model picker, glob patterns, a scoped
  list you cycle with `ctrl+p`, and per-model thinking levels (`low` to `max`).
- **Project context and skills.** Loads `AGENTS.md`/`CLAUDE.md` files and
  skills, and supports a custom or appended system prompt, globally and per project.
- **pi-compatible configuration.** `settings.json`, `models.json` and `auth.json`
  use pi's key names and formats.

## Requirements

- **Go 1.27.1 or newer** to build from source (see [`go.mod`](go.mod)).
- A terminal with 256-colour or true-colour support.
- Credentials for at least one LLM provider, or a local
  [Ollama](https://ollama.com) server. See [Providers and credentials](#providers-and-credentials).
- Optional: the [Codex CLI](https://github.com/openai/codex), to use a ChatGPT
  plan through the `openai-codex` provider.

**Platforms:** macOS and Linux. Windows is not supported, because the harness's
process primitives are Unix-only.

## Installation

### With `go install`

```sh
go install github.com/awersli99/unreal-tui@latest
```

This command installs a binary called `unreal-tui` into `$(go env GOPATH)/bin`.
To get the `unreal` command name used throughout this document, build from
source instead, or rename the binary.

### From source

```sh
git clone https://github.com/awersli99/unreal-tui.git
cd unreal-tui
make install    # builds bin/unreal and symlinks it to ~/.local/bin/unreal
```

Make sure `~/.local/bin` is on your `PATH`. To build without installing, run
`make build`, which puts the binary in `bin/unreal`.

## Quick start

1. **Pick a provider.** Choose whichever you already have:

   ```sh
   export ANTHROPIC_API_KEY=...   # or OPENAI_API_KEY, OPENROUTER_API_KEY, FIREWORKS_API_KEY
   ```

   Other options: sign in to the Codex CLI (`codex login`), run Ollama locally,
   or skip this step and use `/login` inside unreal.

2. **Start unreal in your project:**

   ```sh
   cd path/to/your/project
   unreal
   ```

3. **Type a request and press `enter`.** Press `ctrl+l` to change the model,
   `shift+tab` to change the thinking level, and `/help` to list everything else.

When you quit, unreal prints the command that resumes the session:

```text
Resume this session with: unreal --session 53195aac
```

## Usage

```sh
unreal                             # start in the current directory
unreal "fix the tests"             # start with a prompt
unreal -c                          # continue the latest session in this directory
unreal -r                          # pick a session to resume
unreal --session 53195aac          # resume a session by ID prefix
unreal --model sol:xhigh           # pick a model by pattern, with a thinking level
unreal --models "gpt-6*,gpt-5.6*"  # the models ctrl+p cycles through this session
unreal --list-models [search]      # list available models and exit
```

### Command-line options

Flags must come before the prompt. Run `unreal --help` for the full list.

| Option | Description |
|---|---|
| `--provider <name>` | Provider: `openai-codex`, `openai`, `anthropic`, `openrouter`, `fireworks`, `ollama`, or one defined in `models.json` |
| `--model <pattern>` | Model as `provider/id`, `id`, or a pattern. Add `:<level>` to set thinking, e.g. `gpt-6-astra:max` |
| `--thinking <level>` | Thinking level: `low`, `medium`, `high`, `xhigh`, `max` |
| `--models <patterns>` | Comma-separated models that `ctrl+p` cycles through this session |
| `--list-models [search]` | List available models and exit |
| `-c`, `--continue` | Continue the most recent session in this workspace |
| `-r`, `--resume` | Pick a session to resume |
| `--session <id>` | Resume the session with this ID or unique prefix |
| `--session-dir <dir>` | Directory for session files |
| `-nc`, `--no-context-files` | Skip `AGENTS.md` / `CLAUDE.md` context files |
| `--workspace <dir>` | Workspace directory where Bash runs (default: current directory) |

### Keys

| Key | Action |
|---|---|
| `enter` | Send message |
| `ctrl+j` / `alt+enter` | Insert newline |
| `↑` / `↓` | Message history |
| `esc` | Interrupt the agent |
| `ctrl+c` | Clear input, or interrupt; press twice to exit |
| `ctrl+d` | Exit |
| `ctrl+l` | Model picker |
| `ctrl+p` / `alt+p` | Next / previous scoped model |
| `shift+tab` | Cycle thinking level |
| `ctrl+o` | Full transcript |
| `ctrl+t` | Show/hide thinking blocks |

In pickers, type to filter, use `↑`/`↓` to move, press `enter` to select and
`esc` to cancel.

### Slash commands

Typing `/` opens command autocomplete under the editor. Use `↑`/`↓` to select,
`tab` to complete, `enter` to run and `esc` to dismiss. `/model` and
`/thinking` also complete their argument.

| Command | Description |
|---|---|
| `/model [pattern]` | Open the model picker, or switch directly when the pattern matches one model |
| `/thinking [level]` | Open the thinking picker, or set `low`/`medium`/`high`/`xhigh`/`max` |
| `/scoped-models` | Choose the models `ctrl+p` cycles through (`ctrl+s` saves them) |
| `/settings` | Toggle settings, saved to `~/.unreal-tui/settings.json` |
| `/reload` | Reload settings, `models.json`, `SYSTEM.md`, `AGENTS.md` and skills |
| `/login [provider]` | Sign in with a Claude account (browser OAuth), ChatGPT (`codex login`), or an API key |
| `/logout [provider]` | Remove saved API-key/OAuth credentials, or sign out of Codex (`codex logout`) |
| `/new` | Start a new session |
| `/resume [n\|id]` | Resume a previous session |
| `/session` | Show session details: file, model, token usage |
| `/help` | Show commands and keys |
| `/quit` | Exit |

### Model patterns

Model patterns can take any of these forms:

- `provider/id`
- a bare ID
- a glob like `gpt-5*`
- any substring

Any of them can end with `:<level>` to set the thinking level. Model IDs missing
from the catalog are accepted too (`/model openrouter/some/model`). The model and
thinking level you pick become the defaults for the next start.

### Layout

Output scrolls above the editor. The editor sits between two rules coloured by
the current thinking level. The footer shows the directory, git branch and
session. It also shows token usage, context fill (`%/window`), the model and the
thinking level. At startup, unreal lists the loaded `[Context]` files and
`[Skills]` unless `quietStartup` is set.

## Configuration

All configuration lives in `~/.unreal-tui`, or in `$UNREAL_TUI_HOME` if set. The
layout mirrors pi's `~/.pi/agent`.

| File | Purpose |
|---|---|
| `settings.json` | Global settings |
| `models.json` | Custom providers and models |
| `auth.json` | Credentials saved by `/login` (private, mode 600) |
| `SYSTEM.md` | Replaces the built-in system prompt |
| `APPEND_SYSTEM.md` | Appended to the system prompt |
| `AGENTS.md` | Global instructions |
| `skills/` | Global skills |
| `sessions/` | Session files, per workspace |

A project can add these files under `.unreal/`:

- `.unreal/settings.json`, merged over the global settings (nested objects included)
- `.unreal/SYSTEM.md`
- `.unreal/APPEND_SYSTEM.md`
- `.unreal/skills/`

### settings.json

```json
{
  "defaultProvider": "openai-codex",
  "defaultModel": "gpt-6-astra",
  "defaultThinkingLevel": "high",
  "modelThinkingLevels": { "openai-codex/gpt-5.6-luna": "low" },
  "enabledModels": ["gpt-6*", "openrouter/*:medium"],
  "hideThinkingBlock": false,
  "quietStartup": false,
  "theme": "dark",
  "shellPath": "/bin/zsh",
  "sessionDir": ".sessions",
  "retry": { "maxRetries": 4 }
}
```

The keys are pi's. A few need explanation:

- `modelThinkingLevels` applies whenever that model is selected.
- `enabledModels` sets the models `ctrl+p` cycles through.
- `shellPath` can only be set globally, so a cloned repository cannot choose
  the binary that runs your commands.

Invalid values produce a warning; startup continues.

### models.json

`models.json` has the same shape as pi's. It can add models to a built-in
provider, or define new providers for any of these APIs:

- `anthropic-messages`
- `openai-responses`
- `openai-codex`
- `openrouter`
- `fireworks`
- `ollama`

The Anthropic adapter lives in this repository; the other adapters come from
the harness.

```json
{
  "providers": {
    "ollama": { "models": [{ "id": "qwen3-coder:30b" }] },
    "lab": {
      "api": "openai-responses",
      "baseUrl": "http://localhost:8000/v1",
      "apiKey": "$LAB_API_KEY",
      "models": [{ "id": "coder", "name": "Lab Coder", "thinkingLevels": ["low", "high"] }]
    }
  }
}
```

`apiKey` follows pi's rules:

- `"$VAR"` or `"${VAR}"` reads the environment.
- `"!command"` runs a command, e.g. `"!op read op://vault/key"`.
- Anything else is used literally.

### Context files and skills

The system prompt includes `AGENTS.md` (or `CLAUDE.md`; `AGENTS.override.md`
takes precedence) from several places, in this order:

1. `~/.unreal-tui`
2. each directory from your home directory down to the workspace

`-nc` skips them. Skills load from these directories:

- `<workspace>/.unreal/skills`
- `<workspace>/.harness/skills`
- `~/.unreal-tui/skills`

### Environment variables

| Variable | Purpose |
|---|---|
| `UNREAL_TUI_HOME` | Configuration directory (default `~/.unreal-tui`) |
| `UNREAL_HARNESS_LLM_PROVIDER` | Default provider, like `--provider` |
| `UNREAL_HARNESS_LLM_MODEL` | Default model, like `--model` |
| `UNREAL_HARNESS_LLM_BASE_URL` | Override the base URL of the built-in providers |
| `UNREAL_HARNESS_LLM_API_KEY` | API key for the built-in providers, overriding their usual variable |
| `OPENAI_API_KEY` | `openai` provider |
| `ANTHROPIC_API_KEY` | `anthropic` provider (API key) |
| `ANTHROPIC_OAUTH_TOKEN` | `anthropic` provider (existing OAuth access token, not refreshed) |
| `ANTHROPIC_AUTH_FILE` | Path to external Pi-format Anthropic credentials |
| `OPENROUTER_API_KEY` | `openrouter` provider |
| `FIREWORKS_API_KEY` | `fireworks` provider |
| `CODEX_HOME` | Codex CLI home (default `~/.codex`) |
| `OPENAI_CODEX_ACCESS_TOKEN`, `OPENAI_CODEX_AUTH_FILE` | Alternative Codex credentials |
| `PI_ANTHROPIC_AUTH_CLAUDE_CODE_VERSION` | Override the Claude Code compatibility version for OAuth requests |
| `SHELL` | Shell used for commands when `shellPath` is not set (fallback `/bin/sh`) |

## Providers and credentials

| Provider | Credentials |
|---|---|
| `openai-codex` | Your Codex/ChatGPT login (`~/.codex/auth.json`). The model list comes from Codex's model cache |
| `openai` | `OPENAI_API_KEY` |
| `anthropic` | `ANTHROPIC_API_KEY`, or a Claude subscription via `/login anthropic` |
| `openrouter` | `OPENROUTER_API_KEY` |
| `fireworks` | `FIREWORKS_API_KEY` |
| `ollama` | None; talks to a local Ollama server |

With no provider configured, unreal checks in this order:

1. `openai`, if `OPENAI_API_KEY` is set
2. your Codex login
3. the first other provider that has credentials (including Anthropic)

### Signing in with `/login`

unreal starts even when no provider is configured, so on a fresh install you can
run `/login` right away. It offers two options:

- **Sign in with an account.** Uses Anthropic browser OAuth, or the official
  Codex CLI for ChatGPT.
- **Sign in with an API key.** You choose a provider and type the key into a
  masked prompt.

Credentials are saved to `~/.unreal-tui/auth.json` in pi's format, readable only
by you:

```json
{"openrouter": {"type": "api_key", "key": "…"}}
```

Anthropic account logins use an `oauth` entry in the same file. Saved
credentials take precedence over environment variables and `apiKey` in
`models.json`, and apply to the next request without a restart. `/logout`
removes them. It leaves environment variables, `models.json` and external auth
files alone.

### Anthropic

The built-in `anthropic` provider uses a Messages API adapter that lives in this
repository. It does not patch or fork `unreal-agent`, and it needs no extra
dependencies.

**With an API key:**

```sh
export ANTHROPIC_API_KEY="your-key"
unreal --provider anthropic
```

**With a Claude subscription,** start unreal and run `/login anthropic`. This
opens Anthropic's sign-in page in your browser. The login uses OAuth with PKCE
and a local callback on `127.0.0.1:53692`.

- If the browser is on another machine, or doesn't open automatically, open the
  displayed URL yourself. Then paste the final redirect URL or the authorization
  code into the masked prompt.
- `esc` cancels the login.
- An unfinished login times out after five minutes.
- Pi is not required.

After signing in, pick a Claude model with `ctrl+l` or
`/model anthropic/claude-sonnet-5`. Later starts can use `unreal --provider anthropic`.

**How OAuth credentials are handled:**

- Expired OAuth credentials are refreshed and written back atomically. Other
  providers' entries and unknown fields are preserved.
- `/logout anthropic` removes the local login. A refresh already in flight
  cannot restore it.
- Refreshes and credential writes are coordinated within one unreal process,
  but not across several running processes.

**Other ways to supply credentials:**

- Existing Pi-format credentials: set `ANTHROPIC_AUTH_FILE` to their path. The
  file must have private permissions (`chmod 600`). unreal does not read Pi's
  directory or `PI_CODING_AGENT_DIR` on its own. `/login` always writes unreal's
  own auth file, and `/logout` does not modify external auth files or revoke
  tokens on Anthropic's servers.
- An existing access token: `ANTHROPIC_OAUTH_TOKEN=sk-ant-oat-...`. Tokens from
  the environment cannot be refreshed; replace them when they expire.

**Credential precedence**, highest first:

1. A credential saved by `/login` (OAuth or API key)
2. `apiKey` in `models.json`
3. `UNREAL_HARNESS_LLM_API_KEY`
4. `ANTHROPIC_API_KEY`
5. `ANTHROPIC_OAUTH_TOKEN`
6. An external `ANTHROPIC_AUTH_FILE`

To replace a saved subscription login with an API key, use `/login` → "Sign in
with an API key" → "anthropic". Or run `/logout anthropic` to fall back to an
environment key. Custom Anthropic-compatible providers never inherit your
subscription login; give them their own key in `models.json` or through `/login`.

**OAuth request shaping.** OAuth compatibility is adapted from
[pi-anthropic-auth](https://github.com/gotgenes/pi-anthropic-auth):

- `sk-ant-oat` tokens use Bearer auth, Claude Code identity and beta headers,
  canonical tool names, and pi-anthropic-auth's hashed billing system block.
- Only the harness-owned prompt identity is replaced. Tool instructions, skills,
  project context and custom prompts stay as they are.
- Plain API keys use `x-api-key`, without any OAuth shaping.

**Thinking blocks.** Signed thinking blocks keep their order and opaque contents
across tool turns and session replay. When you switch provider or model,
incompatible reasoning is dropped rather than forwarded.

#### Models

The bundled catalog was checked against Anthropic's model docs and Pi's catalog
on 2026-09-22. It includes:

- **Sonnet 5** (`claude-sonnet-5`), the default for new setups
- **Opus 5.5** (`claude-opus-5-5`), **Opus 5**, **Fable 5.1** and **Fable 5**
- Opus 4.8, 4.7, 4.6 and 4.5; Sonnet 4.6 and 4.5; Haiku 4.5; plus dated 4.5 snapshots

Saved model defaults are never changed for you. To use a newer model, select it
explicitly:

```text
/model anthropic/claude-opus-5-5:xhigh
```

Capabilities by model:

- **Context windows.** The current adaptive models have 1M-token windows.
  Haiku 4.5 and Opus 4.5 have 200K.
- **Effort levels.** The adaptive models support native
  `low`/`medium`/`high`/`xhigh`/`max` effort. The 4.6 models omit `xhigh`.
- **Per-turn effort.** Opus 5, Opus 5.5 and Fable 5.1 use native per-turn effort
  markers and thinking-binding controls. Changing `/thinking` therefore doesn't
  rewrite the configuration of earlier turns.
- **Token budgets.** Older models still use thinking token budgets.
- **Thinking summaries.** unreal always requests them, including on newer models
  that otherwise omit them.
- **Session replay.** Signed thinking and effort metadata survive it.

The catalog ships with unreal and is not fetched at startup:

- Unlisted IDs can still be selected with `--model anthropic/<id>` or
  `/model anthropic/<id>`.
- `models.json` can add models or override their displayed metadata. Known IDs
  keep their capabilities when renamed or used through a custom
  Anthropic-compatible provider.
- A future model with new API requirements may need an adapter update.
- As with Codex, a listed model is not necessarily available on your account.

#### Anthropic-compatible endpoints

```json
{
  "providers": {
    "claude-proxy": {
      "api": "anthropic-messages",
      "baseUrl": "https://your-proxy.example/v1",
      "apiKey": "$CLAUDE_PROXY_KEY",
      "models": [{ "id": "claude-sonnet-5" }]
    }
  }
}
```

- `anthropic` is also accepted as an API alias.
- `baseUrl` may include or omit `/v1`; requests go to `/v1/messages`.
- The built-in provider also honours `UNREAL_HARNESS_LLM_BASE_URL`.
- OAuth refresh always goes to Anthropic's token endpoint, never to the proxy.

#### `claude_code_version_too_old`

If a new model reports this error, override the bundled Claude Code
compatibility version. The default is `2.1.280`; it is used in both the user
agent and the billing block. Use a bare `X.Y.Z` version:

```sh
export PI_ANTHROPIC_AUTH_CLAUDE_CODE_VERSION=2.1.300
```

This affects only OAuth requests.

> [!IMPORTANT]
> Anthropic controls subscription eligibility, third-party client access and
> billing. Compatibility shaping does not guarantee that requests are covered by
> your plan, or that extra-usage charges won't apply. Check your account's usage
> settings and Anthropic's current terms before using a subscription login.

## How it works

`unreal` is a thin interactive host around the
[Unreal Agent Harness](https://github.com/unreallabsai/unreal-agent):

- **One coordinator per session.** Each active session has one long-lived
  harness coordinator. Messages you send while the agent works go into its
  inbox. If a model call is in flight, it is re-issued with your message;
  running tools keep going.
- **Hard stop.** `esc` cancels the model call and running shell commands. Your
  next message resumes the persisted session.
- **Parallel tools.** Tool calls run asynchronously and in parallel.
- **Session storage.** Sessions are the harness's own append-only session files,
  stored per workspace under `~/.unreal-tui/sessions/`.
- **Tools.** The agent has the harness's `Bash` and `ViewImage` tools, plus
  skills. Bash runs in the workspace with your shell.
- **UI.** The interface is built with
  [Bubble Tea](https://github.com/charmbracelet/bubbletea),
  [Bubbles](https://github.com/charmbracelet/bubbles),
  [Lip Gloss](https://github.com/charmbracelet/lipgloss) and
  [Glamour](https://github.com/charmbracelet/glamour).

## Security

> [!WARNING]
> The agent runs shell commands directly on your machine, with your user's
> permissions and no approval prompt (the same as pi). Use it only in
> directories and on machines where you accept that. For untrusted code, run it
> in a container or VM.

- `auth.json` is created with mode 600. External auth files must have private
  permissions too.
- `shellPath` can only be set in the global settings, so a project's
  `.unreal/settings.json` cannot change which binary runs commands.
- Project files (`AGENTS.md`, `.unreal/SYSTEM.md`, skills) become part of the
  prompt. Treat repositories you don't trust with care.

To report a vulnerability, see [SECURITY.md](SECURITY.md). Please don't open a
public issue.

## Limitations

- **No file-edit tool.** The harness offers only Bash, ViewImage and skill
  tools, so the agent edits files through shell commands.
- **No context compaction.** Very long sessions eventually hit the model's
  context limit. When that happens, start a `/new` session.
- **No token streaming.** Model replies arrive whole.
- **Codex model list.** Codex's model cache can list models your ChatGPT plan
  cannot use. The backend rejects them with a clear error; switch models with `ctrl+l`.

## Development

```sh
make build    # build bin/unreal
make test     # go test -race ./...
make install  # build, then symlink to ~/.local/bin/unreal
```

The tests are offline. They cover the engine, configuration, the model catalog,
the TUI, and the Anthropic adapter and login flow, using local test servers.
No API keys are needed.

### Project layout

Everything lives in a single `main` package:

| File | Contents |
|---|---|
| `main.go` | Flag parsing, startup model selection, `--list-models` |
| `app.go` | Application state outside the Bubble Tea model |
| `engine.go` | Drives the harness coordinator, sessions, tools and interrupts |
| `tui.go`, `editor.go`, `render.go`, `transcript.go` | Bubble Tea model, editor, rendering and transcript |
| `commands.go`, `complete.go` | Slash commands and autocomplete |
| `pickers.go`, `selector.go` | Model, thinking, session, scoped-model and settings pickers |
| `settings.go`, `models.go`, `prompt.go` | Settings, model catalog, system prompt, context files and skills |
| `providers.go` | Built-in providers and adapter construction |
| `auth.go`, `login.go` | `auth.json` storage and the `/login`/`/logout` flows |
| `anthropic*.go` | Anthropic Messages adapter, OAuth login and refresh, model catalog |

## Contributing

Contributions are welcome. Before opening a pull request:

1. Open an issue first for larger changes, so the approach can be discussed.
2. Keep changes focused, and follow the existing code style (`gofmt`, `go vet`).
3. Add or update tests, and make sure `make test` passes.
4. Update this README when behaviour, flags, commands or configuration change.

## License

unreal is released under the [MIT License](LICENSE). Third-party code and
conventions this project adapts are credited in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Acknowledgements

- [Unreal Agent Harness](https://github.com/unreallabsai/unreal-agent) by Unreal
  Labs: the coordinator, sessions, tools and most provider adapters.
- [pi](https://github.com/earendil-works/pi) by Mario Zechner: the interaction
  design, configuration formats and credential conventions unreal follows.
- [pi-anthropic-auth](https://github.com/gotgenes/pi-anthropic-auth) by
  Christopher D. Lasher: the basis of the Anthropic OAuth request shaping.
- [Charm](https://charm.sh): the TUI libraries.
