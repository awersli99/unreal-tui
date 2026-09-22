package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// contextFileNames are checked in order in each directory; the first match wins.
var contextFileNames = []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md"}

const basePrompt = `You are a coding agent working with the user in an interactive terminal session on their own machine. This is not a sandbox: Bash commands run directly on the user's computer with their permissions.

## Interaction
- The user reads your replies in a terminal. Be concise and direct; use Markdown sparingly.
- Ending a turn without tool calls hands control back to the user, who can reply. When the request is ambiguous or an action is risky, ask instead of guessing.
- The user can send new messages while you work. Treat them as steering: adjust course and address them.

## Working on code
- Read the relevant code before changing it, and follow the conventions already in the project.
- There is no dedicated file-editing tool. Create files with heredocs (cat > path <<'EOF') and make targeted edits with exact string replacement (for example a short python3 script); never rewrite a large file just to change a few lines. Re-read the changed region afterwards to confirm the edit.
- Verify your work: build, run the tests, or run the program when you can.
- Do not run destructive or hard-to-reverse commands (rm -rf, git reset --hard, git push --force, dropping data) or anything that affects systems beyond this machine unless the user explicitly asked for it.
- Do not commit or push unless asked.`

// PromptOptions control how the system prompt is assembled.
type PromptOptions struct {
	Workspace    string
	Home         string
	Shell        string
	ContextFiles bool
}

// systemPrompt assembles the system prompt the way pi does: SYSTEM.md (project
// .unreal/SYSTEM.md, else ~/.unreal-tui/SYSTEM.md) replaces the built-in
// prompt, APPEND_SYSTEM.md files are appended, then the environment and the
// AGENTS.md/CLAUDE.md context files. It also returns the files it used.
func systemPrompt(options PromptOptions) (string, []string) {
	workspace := options.Workspace
	var used []string
	base := basePrompt
	for _, path := range []string{
		filepath.Join(workspace, projectDirName, "SYSTEM.md"),
		filepath.Join(options.Home, "SYSTEM.md"),
	} {
		if contents, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(contents)) != "" {
			base = strings.TrimSpace(string(contents))
			used = append(used, path)
			break
		}
	}
	var prompt strings.Builder
	prompt.WriteString(base)
	for _, path := range []string{
		filepath.Join(options.Home, "APPEND_SYSTEM.md"),
		filepath.Join(workspace, projectDirName, "APPEND_SYSTEM.md"),
	} {
		if contents, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(contents)) != "" {
			prompt.WriteString("\n\n" + strings.TrimSpace(string(contents)))
			used = append(used, path)
		}
	}
	shell := firstNonEmpty(options.Shell, os.Getenv("SHELL"), "/bin/sh")
	fmt.Fprintf(&prompt, "\n\n## Environment\n- Working directory: %s\n- Platform: %s/%s\n- Shell: %s\n- Date: %s\n",
		workspace, runtime.GOOS, runtime.GOARCH, shell, time.Now().Format("2006-01-02"))
	if options.ContextFiles {
		for _, file := range contextFiles(workspace, options.Home) {
			fmt.Fprintf(&prompt, "\n## Project instructions from %s\n\n%s\n", file.path, strings.TrimSpace(file.contents))
			used = append(used, file.path)
		}
	}
	return prompt.String(), used
}

type contextFile struct {
	path     string
	contents string
}

// contextFiles collects AGENTS.md (or CLAUDE.md) from the global config
// directory and from each directory between the home directory and the
// workspace, outermost first so more specific instructions come last. As in
// pi, AGENTS.override.md takes precedence within a directory.
func contextFiles(workspace, configHome string) []contextFile {
	var directories []string
	home, _ := os.UserHomeDir()
	for directory := workspace; ; directory = filepath.Dir(directory) {
		directories = append(directories, directory)
		if directory == home || directory == filepath.Dir(directory) {
			break
		}
	}
	directories = append(directories, configHome)

	var files []contextFile
	seen := make(map[string]bool)
	for index := len(directories) - 1; index >= 0; index-- {
		for _, name := range contextFileNames {
			path := filepath.Join(directories[index], name)
			if seen[path] {
				break
			}
			contents, err := os.ReadFile(path)
			if err != nil || strings.TrimSpace(string(contents)) == "" {
				continue
			}
			seen[path] = true
			files = append(files, contextFile{path: path, contents: string(contents)})
			break
		}
	}
	return files
}
