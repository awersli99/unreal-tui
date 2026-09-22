# unreal

An interactive terminal coding agent built on the
[Unreal Agent Harness](https://github.com/unreallabsai/unreal-agent) — a pi-style
TUI on top of the harness's async coordinator.

```sh
unreal                 # start in the current directory
unreal "fix the tests" # start with a prompt
unreal -c              # continue the latest session in this directory
unreal -r 53195aac     # resume a session by ID prefix
```

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
`ctrl+o` full transcript · `ctrl+t` toggle thinking summaries · `ctrl+c` twice or
`ctrl+d` exit.

`/new`, `/resume [n|id]`, `/model [id]`, `/provider [name] [model]`,
`/thinking [low|medium|high|xhigh|max]`, `/session`, `/help`, `/quit`.

## Providers

Same as the upstream runner: `openai` (`OPENAI_API_KEY`), `openai-codex` (your
Codex/ChatGPT login in `~/.codex/auth.json`), `openrouter`, `fireworks`,
`ollama`. With no configuration it picks `openai` if `OPENAI_API_KEY` is set,
otherwise your Codex login. The last provider, model and thinking level are
remembered in `~/.unreal-tui/settings.json`.

## Context

The system prompt includes `AGENTS.md` (or `CLAUDE.md`) from the workspace and
its parent directories up to your home directory, plus `~/.unreal-tui/AGENTS.md`.
Skills are loaded from `<workspace>/.harness/skills` and `~/.unreal-tui/skills`.

## Caveats

- Bash runs directly on your machine with your permissions; there is no
  approval prompt (like pi).
- The harness has only Bash, ViewImage and skill tools — no dedicated file-edit
  tool — so the agent edits files through shell commands.
- The harness does not compact context yet, so very long sessions will
  eventually hit the model's context limit; start a `/new` session.
- Model replies arrive whole rather than streamed token by token.

## Development

```sh
make test     # engine tests with a scripted fake model
make install  # build and symlink to ~/.local/bin/unreal
```
