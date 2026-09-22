# unreal

A terminal coding agent built on the [Unreal Agent Harness](https://github.com/unreallabsai/unreal-agent),
inspired by [pi](https://github.com/earendil-works/pi).

You can message the agent while it works to steer it, press `esc` to stop it,
and resume any past session later. It works with OpenAI, ChatGPT/Codex,
Anthropic (API key or Claude subscription), OpenRouter, Fireworks and Ollama.

## Install

It needs Go 1.27.1+ and runs on macOS or Linux.

```sh
go install github.com/awersli99/unreal-tui/cmd/unreal@latest
```

Or build from source:

```sh
git clone https://github.com/awersli99/unreal-tui.git
cd unreal-tui
make install    # symlinks the binary to ~/.local/bin/unreal
```

## Quick start

```sh
export ANTHROPIC_API_KEY=...   # or OPENAI_API_KEY, OPENROUTER_API_KEY, FIREWORKS_API_KEY
unreal                         # or skip the key and run /login inside unreal
```

```sh
unreal "fix the tests"                        # start with a prompt
unreal -c                                     # continue the latest session here
unreal -r                                     # pick a session to resume
unreal --model anthropic/claude-sonnet-5:high # model and thinking level
unreal --list-models                          # see what's available
```

Run `unreal --help` for every flag.

## Keys and commands

| Key | Action |
|---|---|
| `enter` / `ctrl+j` | Send / newline |
| `esc` | Interrupt the agent |
| `ctrl+l` / `ctrl+p` | Model picker / next model |
| `shift+tab` | Cycle thinking level |
| `ctrl+o` | Full transcript |
| `ctrl+v` | Paste an image (saved to a temp file, path inserted) or text |
| `ctrl+c` twice / `ctrl+d` | Exit |

Type `/` to see commands: `/model`, `/thinking`, `/login`, `/logout`, `/new`,
`/resume`, `/settings`, `/reload` and `/help`.

## Configuration

Configuration lives in `~/.unreal-tui`, or `$UNREAL_TUI_HOME` if set:

- `settings.json`: settings
- `models.json`: custom providers and models
- `SYSTEM.md`: replaces the system prompt
- `AGENTS.md`: global instructions
- `skills/`: global skills

These follow pi's formats. Projects can override them in `.unreal/`, and
`AGENTS.md`/`CLAUDE.md` files are loaded as context.

See [docs/configuration.md](docs/configuration.md) for all settings, providers,
environment variables, and Anthropic subscription login details.

> [!WARNING]
> The agent runs shell commands on your machine with your permissions, and
> asks for no approval. Use a container or VM for untrusted code.

## Limitations

- The agent has no file-edit tool; it edits files with shell commands.
- Context is never compacted. Run `/new` when a session gets long.
- Replies arrive whole, not streamed.

## Development

```sh
make test     # offline tests, no API keys needed
make lint     # golangci-lint (pinned version, see .golangci.yml)
make fmt      # gofmt + goimports
make build    # outputs bin/unreal
```

The code is organised as:

- `cmd/unreal`: flags and startup
- `internal/tui`: the Bubble Tea interface
- `internal/engine`: drives the harness coordinator and builds the transcript
- `internal/provider`: providers, their clients and the model catalog
- `internal/anthropic`: the Anthropic Messages client and Claude OAuth login
- `internal/config`: settings, saved credentials, system prompt and skills

Issues and PRs are welcome. Please run `make test` and `make lint` before
submitting. To report a security issue, see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE). Third-party credits are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
