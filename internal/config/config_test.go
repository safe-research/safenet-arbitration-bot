package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// setup isolates a test from the real environment: it changes into an empty
// working directory and points $HOME and $XDG_CONFIG_HOME at empty
// directories.
func setup(t *testing.T) (cwd, home, xdg string) {
	cwd, home, xdg = t.TempDir(), t.TempDir(), t.TempDir()
	t.Chdir(cwd)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	return cwd, home, xdg
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaultsWithoutFile(t *testing.T) {
	setup(t)
	if _, err := Load(""); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadExplicitPath(t *testing.T) {
	cwd, _, _ := setup(t)
	path := filepath.Join(cwd, "custom.json")
	write(t, path, `{}`)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadExplicitPathMustExist(t *testing.T) {
	setup(t)
	_, err := Load("missing.json")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load: got %v, want %v", err, fs.ErrNotExist)
	}
}

func TestLoadSearchOrder(t *testing.T) {
	cwd, _, xdg := setup(t)
	write(t, filepath.Join(xdg, "arbot", "config.json"), `{}`)
	if _, err := Load(""); err != nil {
		t.Fatalf("Load from $XDG_CONFIG_HOME: %v", err)
	}

	// A file in the working directory takes precedence, so its error shows
	// that it was picked over the valid one in $XDG_CONFIG_HOME.
	write(t, filepath.Join(cwd, "arbot.config.json"), `invalid`)
	if _, err := Load(""); err == nil {
		t.Fatal("Load: expected the working directory file to be used")
	}
}

func TestLoadXDGDefaultsToHomeConfig(t *testing.T) {
	_, home, _ := setup(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	write(t, filepath.Join(home, ".config", "arbot", "config.json"), `invalid`)
	if _, err := Load(""); err == nil {
		t.Fatal("Load: expected ~/.config/arbot/config.json to be used")
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	for name, contents := range map[string]string{
		"syntax":        `{`,
		"unknown field": `{"unknown": true}`,
		"trailing data": `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			cwd, _, _ := setup(t)
			path := filepath.Join(cwd, "arbot.config.json")
			write(t, path, contents)
			if _, err := Load(path); err == nil {
				t.Fatal("Load: expected an error")
			}
		})
	}
}
