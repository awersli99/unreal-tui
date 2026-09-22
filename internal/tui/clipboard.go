package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// Pasting works like pi's: ctrl+v saves an image on the clipboard to a
// temporary file and inserts its path at the cursor, where the agent can open
// it with its ViewImage tool. Without an image it pastes text as usual.

const (
	clipboardListTimeout = time.Second
	clipboardReadTimeout = 5 * time.Second
)

// clipboardImageTypes are the image types the ViewImage tool can decode, in
// order of preference, with the extension each is saved under.
var clipboardImageTypes = []struct{ mime, extension string }{
	{"image/png", "png"},
	{"image/jpeg", "jpg"},
	{"image/webp", "webp"},
	{"image/gif", "gif"},
	{"image/bmp", "bmp"},
	{"image/tiff", "tiff"},
}

// clipboardImagePastedMsg carries the saved image's path, or "" if the
// clipboard held no image.
type clipboardImagePastedMsg struct{ path string }

// clipboardCommand runs a clipboard tool and returns its standard output. It
// is a variable so tests can fake the tools.
var clipboardCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = &stdout
	err := command.Run()
	return stdout.Bytes(), err
}

// pasteClipboard reads the clipboard in the background, since the tools
// take a moment to run.
func pasteClipboard() tea.Cmd {
	return func() tea.Msg {
		path, err := saveClipboardImage(runtime.GOOS, os.Getenv, os.TempDir())
		if err != nil {
			path = "" // Paste text instead, as pi does.
		}
		return clipboardImagePastedMsg{path: path}
	}
}

func (current *model) handleClipboardImage(message clipboardImagePastedMsg) tea.Cmd {
	if message.path == "" {
		return textarea.Paste
	}
	current.editInput(func() { current.input.InsertString(message.path) })
	current.syncSuggestions()
	return nil
}

// saveClipboardImage writes the clipboard's image to a new file in directory
// and returns its path, or "" when the clipboard holds no image.
func saveClipboardImage(goos string, getenv func(string) string, directory string) (string, error) {
	switch goos {
	case "darwin":
		return saveMacClipboardImage(directory)
	case "linux":
		if getenv("TERMUX_VERSION") != "" {
			return "", nil
		}
		data, mime, err := readLinuxClipboardImage(getenv)
		if err != nil || data == nil {
			return "", err
		}
		return writeClipboardImage(directory, data, mime)
	}
	return "", nil
}

// macClipboardScript saves the clipboard as PNG to the path in its argument;
// macOS converts screenshots and copied images to PNG on request.
const macClipboardScript = `on run argv
	try
		set image to the clipboard as «class PNGf»
	on error
		return "none"
	end try
	set output to open for access (POSIX file (item 1 of argv)) with write permission
	try
		set eof output to 0
		write image to output
	on error message
		close access output
		error message
	end try
	close access output
	return "ok"
end run`

func saveMacClipboardImage(directory string) (string, error) {
	file, err := os.CreateTemp(directory, "unreal-clipboard-*.png")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
	defer cancel()
	output, err := clipboardCommand(ctx, "osascript", "-e", macClipboardScript, path)
	if err == nil && strings.TrimSpace(string(output)) == "ok" {
		if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
			return path, nil
		}
	}
	return "", errors.Join(err, os.Remove(path))
}

// readLinuxClipboardImage tries wl-paste on Wayland, then xclip, the way pi
// does. An empty Wayland clipboard does not fall through to X11's, which
// may be stale.
func readLinuxClipboardImage(getenv func(string) string) ([]byte, string, error) {
	if getenv("WAYLAND_DISPLAY") != "" || getenv("XDG_SESSION_TYPE") == "wayland" {
		data, mime, err := readClipboardImageWith("wl-paste", []string{"--list-types"}, func(mime string) []string {
			return []string{"--type", mime, "--no-newline"}
		})
		if err == nil {
			return data, mime, nil
		}
	}
	return readClipboardImageWith("xclip", []string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, func(mime string) []string {
		return []string{"-selection", "clipboard", "-t", mime, "-o"}
	})
}

// readClipboardImageWith lists the clipboard's types with a tool and reads the
// preferred image type. It returns nil data when there is no image, and an
// error when the tool is unavailable or fails.
func readClipboardImageWith(tool string, list []string, read func(mime string) []string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardListTimeout)
	defer cancel()
	types, err := clipboardCommand(ctx, tool, list...)
	if err != nil {
		return nil, "", err
	}
	mime := preferredImageType(strings.Split(string(types), "\n"))
	if mime == "" {
		return nil, "", nil
	}
	ctx, cancel = context.WithTimeout(context.Background(), clipboardReadTimeout)
	defer cancel()
	data, err := clipboardCommand(ctx, tool, read(mime)...)
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", nil
	}
	return data, mime, nil
}

// preferredImageType picks the best supported image type from a clipboard's
// type list, or "" if there is none.
func preferredImageType(types []string) string {
	available := make(map[string]string, len(types))
	for _, raw := range types {
		raw = strings.TrimSpace(raw)
		base, _, _ := strings.Cut(raw, ";")
		available[strings.ToLower(strings.TrimSpace(base))] = raw
	}
	for _, supported := range clipboardImageTypes {
		if raw, ok := available[supported.mime]; ok {
			return raw
		}
	}
	return ""
}

func writeClipboardImage(directory string, data []byte, mime string) (string, error) {
	extension := "png"
	base, _, _ := strings.Cut(mime, ";")
	for _, supported := range clipboardImageTypes {
		if strings.EqualFold(strings.TrimSpace(base), supported.mime) {
			extension = supported.extension
		}
	}
	file, err := os.CreateTemp(directory, "unreal-clipboard-*."+extension)
	if err != nil {
		return "", err
	}
	path := file.Name()
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	return filepath.Clean(path), nil
}
