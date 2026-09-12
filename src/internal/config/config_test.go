package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultServerPortIsMOSReceivePort(t *testing.T) {
	t.Setenv("SERVER_PORT", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 10541 {
		t.Fatalf("default server port = %d, want 10541", cfg.Server.Port)
	}
}

func TestSourceStateDirectoryConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, env, want string
		envPresent            bool
	}{
		{"unset", "{}\n", "", "", false},
		{"yaml", "source:\n  statedir: checkpoint-yaml\n", "", "checkpoint-yaml", false},
		{"environment", "{}\n", "checkpoint-env", "checkpoint-env", true},
		{"environment overrides yaml", "source:\n  statedir: checkpoint-yaml\n", "checkpoint-env", "checkpoint-env", true},
		{"empty environment clears yaml", "source:\n  statedir: checkpoint-yaml\n", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CONFIG_FILE", path)
			t.Setenv("STATE_DIR", "native-state")
			t.Setenv("SOURCE_STATE_DIR", tc.env)
			if !tc.envPresent {
				if err := os.Unsetenv("SOURCE_STATE_DIR"); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Source.StateDir != tc.want || cfg.State.Dir != "native-state" {
				t.Fatalf("source directory=%q, native directory=%q; want %q and native-state", cfg.Source.StateDir, cfg.State.Dir, tc.want)
			}
		})
	}
}
