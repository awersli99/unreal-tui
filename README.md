# unreal

An interactive terminal coding agent built on the
[Unreal Agent Harness](https://github.com/unreallabsai/unreal-agent) — a pi-style
TUI on top of the harness's async coordinator.

```sh
unreal                          # start in the current directory
unreal "fix the tests"          # start with a prompt
unreal -c                       # continue the latest session in this directory
unreal -r                       # pick a session to resume
unreal --session 53195aac       # resume a session by ID prefix
unreal --model sol:xhigh        # pick a model by pattern, with a thinking level
unreal --models "gpt-6*,gpt-5.6*"  # models ctrl+p cycles through this session
unreal --list-models [search]   # list available models
```

Other flags: `--provider`, `--thinking`, `--session-dir`, `-nc`/`--no-context-files`,
`--workspace`. Flags go before the prompt; `unreal --help` lists them all.

## How it uses the harness

- One long-lived coordinator per active session. Messages you send while the
  agent works go straight into its inbox and steer it: an in-flight model call
  is re-issued with your message, while running tools keep going.
- `esc` submits a hard stop: the model call and running shell commands are
  cancelled. Your next message resumes the persisted session.
- Tool calls run asynchronously and in parallel; the live area shows each
  running command.
- Sessions are the harness's own append-only session files, stored per
  workspace under `~/.unreal-tui/sessions/`.

## Keys and commands

`enter` send · `ctrl+j`/`alt+enter` newline · `↑`/`↓` history · `esc` interrupt ·
`ctrl+l` model picker · `ctrl+p`/`alt+p` next/previous model · `shift+tab` cycle
thinking level · `ctrl+o` full transcript · `ctrl+t` toggle thinking blocks ·
`ctrl+c` twice or `ctrl+d` exit.

Typing `/` opens command autocomplete under the editor, as in pi: `↑`/`↓`
select, `tab` completes, `enter` runs, `esc` dismisses. `/model ` and
`/thinking ` also complete their argument.

The layout follows pi: output scrolls above, the editor sits between two rules
coloured by thinking level, and the footer below shows the directory, git
branch and session, then token usage, context fill (`%/window`) and the model
and thinking level. At startup the loaded `[Context]` files and `[Skills]` are
listed unless `quietStartup` is set.

| Command | |
|---|---|
| `/model [pattern]` | model picker, or switch directly when the pattern matches one model |
| `/thinking [level]` | thinking picker, or set `low`/`medium`/`high`/`xhigh`/`max` |
| `/scoped-models` | choose the models `ctrl+p` cycles through (`ctrl+s` saves them) |
| `/settings` | toggle settings, saved to `~/.unreal-tui/settings.json` |
| `/reload` | reload settings, `models.json`, `SYSTEM.md`, `AGENTS.md` and skills |
| `/new`, `/resume [n\|id]`, `/session`, `/help`, `/quit` | |

Model patterns work as in pi: `provider/id`, a bare id, a glob like `gpt-5*`, or
any substring, optionally followed by `:<level>`. Model IDs the catalog does not
list are accepted too (`/model openrouter/some/model`). Like pi, the model and
thinking level you pick become the defaults for the next start. The input
border is coloured by the thinking level.

## Configuration

Everything lives in `~/.unreal-tui` (or `$UNREAL_TUI_HOME`), mirroring pi's
`~/.pi/agent`:

| File | |
|---|---|
| `settings.json` | global settings |
| `models.json` | custom providers and models |
| `SYSTEM.md` | replaces the built-in system prompt |
| `APPEND_SYSTEM.md` | appended to the system prompt |
| `AGENTS.md` | global instructions |
| `skills/` | global skills |
| `sessions/` | session files, per workspace |

A project can add `.unreal/settings.json` (merged over the global settings,
nested objects included), `.unreal/SYSTEM.md`, `.unreal/APPEND_SYSTEM.md` and
`.unreal/skills/`.

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

The keys are pi's. `modelThinkingLevels` applies whenever that model is
selected. `shellPath` is global-only, so a cloned repository cannot choose the
binary that runs your commands. Invalid values produce a warning instead of
stopping startup.

### models.json

Same shape as pi's. Add models to a built-in provider or define new providers
for any API the harness speaks (`openai-responses`, `openai-codex`, `openrouter`,
`fireworks`, `ollama`):

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

`apiKey` follows pi's rules: `"$VAR"`/`"${VAR}"` read the environment,
`"!command"` runs a command (e.g. `"!op read op://vault/key"`), and anything
else is literal.

## Providers

`openai-codex` uses your Codex/ChatGPT login (`~/.codex/auth.json`), and its
model list comes from Codex's own model cache. `openai` uses `OPENAI_API_KEY`,
`openrouter` uses `OPENROUTER_API_KEY`, and `fireworks` uses
`FIREWORKS_API_KEY`. `ollama` is local. With nothing configured, unreal picks
`openai` if `OPENAI_API_KEY` is set, otherwise your Codex login.

## Context

The system prompt includes `AGENTS.md` (or `CLAUDE.md`; `AGENTS.override.md`
wins) from `~/.unreal-tui`, then from each directory from your home down to the
workspace. `-nc` skips them. Skills are loaded from `<workspace>/.unreal/skills`,
`<workspace>/.harness/skills` and `~/.unreal-tui/skills`.

## Caveats

- Bash runs directly on your machine with your permissions; there is no
  approval prompt (like pi).
- The harness has only Bash, ViewImage and skill tools — no dedicated file-edit
  tool — so the agent edits files through shell commands.
- The harness does not compact context yet, so very long sessions will
  eventually hit the model's context limit; start a `/new` session.
- Model replies arrive whole rather than streamed token by token.
- Codex's model cache can list models your ChatGPT plan cannot use; the backend
  rejects them with a clear error, and you can switch with `ctrl+l`.

## Development

```sh
make test     # engine, config and model catalog tests
make install  # build and symlink to ~/.local/bin/unreal
```
