package xdg

import (
	"path/filepath"
	"testing"
)

func TestDirs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      string
		fallback string
		get      func() string
	}{
		{"ConfigHome", "XDG_CONFIG_HOME", ".config", ConfigHome},
		{"CacheHome", "XDG_CACHE_HOME", ".cache", CacheHome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			fallback := filepath.Join(home, tc.fallback)

			for value, want := range map[string]string{
				"/custom/dir":  "/custom/dir",
				"":             fallback,
				"relative/dir": fallback,
			} {
				t.Setenv(tc.env, value)
				if got := tc.get(); got != want {
					t.Errorf("with $%s=%q: got %q, want %q", tc.env, value, got, want)
				}
			}
		})
	}
}
