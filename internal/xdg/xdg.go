// Package xdg locates the user directories defined by the XDG Base Directory
// Specification.
package xdg

import (
	"os"
	"path/filepath"
)

// ConfigHome returns the base directory for user configuration files:
// $XDG_CONFIG_HOME, defaulting to ~/.config. It returns an empty string if the
// directory can't be determined.
func ConfigHome() string {
	return dir("XDG_CONFIG_HOME", ".config")
}

// CacheHome returns the base directory for user cache files:
// $XDG_CACHE_HOME, defaulting to ~/.cache. It returns an empty string if the
// directory can't be determined.
func CacheHome() string {
	return dir("XDG_CACHE_HOME", ".cache")
}

// dir returns the directory in the environment variable env, or fallback
// relative to the home directory. The specification requires ignoring the
// variable when it is not an absolute path.
func dir(env, fallback string) string {
	if dir := os.Getenv(env); filepath.IsAbs(dir) {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, fallback)
}
