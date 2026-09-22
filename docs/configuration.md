# Configuration and providers

The full reference for configuring unreal. See the [README](../README.md) for an overview.

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
2. each directory from your home directory down to the workspace (for a
   workspace outside your home directory, from the filesystem root)

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
