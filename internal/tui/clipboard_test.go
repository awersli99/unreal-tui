package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func fakeClipboard(t *testing.T, outputs map[string]string) *[]string {
	t.Helper()
	var calls []string
	saved := clipboardCommand
	t.Cleanup(func() { clipboardCommand = saved })
	clipboardCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		output, ok := outputs[call]
		if !ok {
			return nil, errors.New("not found: " + name)
		}
		return []byte(output), nil
	}
	return &calls
}

func TestPreferredImageType(t *testing.T) {
	cases := map[string]string{
		"text/plain\nimage/jpeg\nimage/png\n": "image/png",
		"TARGETS\nimage/webp\nUTF8_STRING":    "image/webp",
		"image/bmp; charset=binary":           "image/bmp; charset=binary",
		"text/plain\nimage/x-icon":            "",
		"":                                    "",
	}
	for types, want := range cases {
		if got := preferredImageType(strings.Split(types, "\n")); got != want {
			t.Errorf("preferredImageType(%q) = %q, want %q", types, got, want)
		}
	}
}

func TestSaveLinuxClipboardImage(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	directory := t.TempDir()

	// Wayland: the listed JPEG is read and saved with its extension.
	fakeClipboard(t, map[string]string{
		"wl-paste --list-types":                   "text/plain\nimage/jpeg\n",
		"wl-paste --type image/jpeg --no-newline": "jpeg bytes",
	})
	path, err := saveClipboardImage("linux", env(map[string]string{"WAYLAND_DISPLAY": "wayland-0"}), directory)
	if err != nil || filepath.Dir(path) != directory || filepath.Ext(path) != ".jpg" {
		t.Fatalf("wayland image saved to %q, %v", path, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "jpeg bytes" {
		t.Fatalf("saved %q", data)
	}

	// An empty Wayland clipboard does not fall back to X11's.
	calls := fakeClipboard(t, map[string]string{"wl-paste --list-types": "text/plain\n"})
	path, err = saveClipboardImage("linux", env(map[string]string{"XDG_SESSION_TYPE": "wayland"}), directory)
	if path != "" || err != nil || len(*calls) != 1 {
		t.Fatalf("text-only wayland clipboard gave %q, %v after %q", path, err, *calls)
	}

	// Without wl-paste, xclip is used.
	fakeClipboard(t, map[string]string{
		"xclip -selection clipboard -t TARGETS -o":   "TARGETS\nimage/png\n",
		"xclip -selection clipboard -t image/png -o": "png bytes",
	})
	path, err = saveClipboardImage("linux", env(map[string]string{"WAYLAND_DISPLAY": "wayland-0"}), directory)
	if err != nil || filepath.Ext(path) != ".png" {
		t.Fatalf("xclip image saved to %q, %v", path, err)
	}

	// No tools at all is an error, which pastes text instead.
	fakeClipboard(t, nil)
	if path, err = saveClipboardImage("linux", env(nil), directory); path != "" || err == nil {
		t.Fatalf("without tools got %q, %v", path, err)
	}
	if path, err = saveClipboardImage("linux", env(map[string]string{"TERMUX_VERSION": "1"}), directory); path != "" || err != nil {
		t.Fatalf("termux got %q, %v", path, err)
	}
}

func TestSaveMacClipboardImageRemovesFileWithoutImage(t *testing.T) {
	directory := t.TempDir()
	saved := clipboardCommand
	t.Cleanup(func() { clipboardCommand = saved })
	clipboardCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("none\n"), nil
	}
	if path, err := saveClipboardImage("darwin", os.Getenv, directory); path != "" || err != nil {
		t.Fatalf("got %q, %v", path, err)
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 0 {
		t.Fatalf("left %v behind", entries)
	}

	clipboardCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "osascript" || len(args) != 3 {
			t.Fatalf("ran %s %q", name, args)
		}
		return []byte("ok\n"), os.WriteFile(args[2], []byte("png"), 0o600)
	}
	if path, err := saveClipboardImage("darwin", os.Getenv, directory); err != nil || filepath.Ext(path) != ".png" {
		t.Fatalf("got %q, %v", path, err)
	}
}

func TestPastedImagePathInsertedAtCursor(t *testing.T) {
	current := newLoginTestModel(t)
	current.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	current.setInput("look at  please")
	current.input.SetCursor(len("look at "))

	if _, command := current.Update(tea.KeyMsg{Type: tea.KeyCtrlV}); command == nil {
		t.Fatal("ctrl+v did not read the clipboard")
	}
	if _, command := current.Update(clipboardImagePastedMsg{path: "/tmp/unreal-clipboard-1.png"}); command != nil {
		t.Fatal("pasting an image also pasted text")
	}
	if got := current.input.Value(); got != "look at /tmp/unreal-clipboard-1.png please" {
		t.Fatalf("input is %q", got)
	}
	// Without an image, the textarea pastes text.
	if _, command := current.Update(clipboardImagePastedMsg{}); command == nil {
		t.Fatal("no text paste without an image")
	}
}
