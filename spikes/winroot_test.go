//go:build windows

package spikes

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// V4: how does os.Root treat the names that are special on Windows? Nothing here
// may create or open anything outside the folder. Run it on a Windows machine.
func TestV4RootOnWindows(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "box")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// These names lead outside the folder, so they must be refused.
	outside := []string{
		`..\escape.txt`,
		`C:\Windows\win.ini`,
		`\\?\C:\Windows\win.ini`,
		`sub\..\..\escape.txt`,
	}
	for _, name := range []string{
		"NUL", "CON", "aux.txt", "COM1", "file:stream", "trailing.", "trailing ", "PROGRA~1",
		`..\escape.txt`, `C:\Windows\win.ini`, `\\?\C:\Windows\win.ini`, `sub\..\..\escape.txt`,
	} {
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o644)
		if err == nil {
			file.Close()
		}
		t.Logf("%-28q -> %v", name, err)
		if slices.Contains(outside, name) && err == nil {
			t.Errorf("%q was accepted, and it leads outside the folder", name)
		}
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "box" {
			t.Errorf("something appeared next to the folder: %s", entry.Name())
		}
	}
	created, _ := os.ReadDir(dir)
	for _, entry := range created {
		t.Logf("created inside the folder: %s", entry.Name())
	}
}
