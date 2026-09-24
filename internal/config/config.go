// Package config loads the optional Arbot configuration file.
//
// Every setting has a sane default, so a missing configuration file is not an
// error: it is equivalent to an empty one.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Config is the Arbot configuration.
type Config struct{}

// Load reads the configuration file at path. If path is empty, it uses the
// first file that exists out of:
//
//  1. ./arbot.config.json
//  2. $XDG_CONFIG_HOME/arbot/config.json (defaulting to ~/.config)
//
// If no file exists, it returns the default configuration.
func Load(path string) (Config, error) {
	if path != "" {
		return load(path)
	}

	for _, path := range searchPaths() {
		config, err := load(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		return config, err
	}
	return Config{}, nil
}

// searchPaths returns the paths that Load checks when no path is given, in
// order of precedence.
func searchPaths() []string {
	paths := []string{"arbot.config.json"}
	if dir := xdgConfigHome(); dir != "" {
		paths = append(paths, filepath.Join(dir, "arbot", "config.json"))
	}
	return paths
}

// xdgConfigHome returns the XDG base directory for user configuration files,
// or an empty string if it can't be determined. The XDG Base Directory
// Specification requires ignoring $XDG_CONFIG_HOME when it is not absolute.
func xdgConfigHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}

func load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("invalid configuration file %s: %w", path, err)
	}
	if decoder.More() {
		return Config{}, fmt.Errorf("invalid configuration file %s: unexpected data after configuration object", path)
	}
	return config, nil
}
