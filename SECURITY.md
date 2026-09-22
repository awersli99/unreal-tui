# Security policy

## Reporting a vulnerability

Please do not report security vulnerabilities through public issues, pull
requests or discussions.

Report them privately through GitHub's
[private vulnerability reporting](https://github.com/awersli99/unreal-tui/security/advisories/new)
("Report a vulnerability" on the repository's **Security** tab). Include:

- a description of the issue and its impact
- steps to reproduce, or a proof of concept
- the affected version or commit

You should receive a response within a few days. Please give the maintainers a
reasonable amount of time to fix the issue before you disclose it publicly.

## Scope

By design, unreal lets an LLM run shell commands on your machine with your
permissions and no approval prompt (see the warning in the
[README](README.md)). That behaviour is not a vulnerability in itself. The
following are in scope, for example:

- credentials in `auth.json` being leaked, logged, sent to the wrong host, or
  written with insecure permissions
- flaws in the OAuth login or token refresh flow
- a cloned repository's project files (`.unreal/`, `AGENTS.md`, skills)
  escalating beyond what the README documents, such as choosing the shell
  binary
- session or configuration files exposing secrets
