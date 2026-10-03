package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestYAMLConfigurationAndEnvironmentOverrides(t *testing.T) {
	t.Setenv("GUARD_DATABASE_DSN", "postgres://guard:password@localhost/guard")
	t.Setenv("GUARD_POLICY_PATH", "../../config/policy.txt")
	t.Setenv("GUARD_MODEL_BASE_URL", "http://localhost:9000/v1")
	t.Setenv("GUARD_UPLOAD_MAX_BYTES", "1024")
	cfg, policy, err := Load("../../config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.BaseURL != "http://localhost:9000/v1" || cfg.Upload.MaxBytes != 1024 || policy.Hash() == "" {
		t.Fatal("incorrect defaults or connection settings")
	}
	cfg.Worker.LeaseDuration = cfg.Model.Timeout
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted short lease")
	}
	cfg.Upload.MaxLongSide = 1921
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted greater than FHD")
	}
}

func TestLoadRejectsInvalidConnections(t *testing.T) {
	for _, tc := range []struct{ name, dsn, endpoint string }{
		{"missing-database", "", "http://localhost:8000/v1"},
		{"credentials", "postgres://guard:password@localhost/guard", "http://user:password@localhost:8000/v1"},
		{"query", "postgres://guard:password@localhost/guard", "http://localhost:8000/v1?key=secret"},
		{"invalid-scheme", "postgres://guard:password@localhost/guard", "file:///tmp/model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GUARD_DATABASE_DSN", tc.dsn)
			t.Setenv("GUARD_MODEL_BASE_URL", tc.endpoint)
			if _, _, err := Load("../../config/config.yaml"); err == nil {
				t.Fatal("accepted invalid connection settings")
			}
		})
	}
}

func TestYAMLSettingsAndStrictKeys(t *testing.T) {
	t.Setenv("GUARD_DATABASE_DSN", "postgres://guard:password@localhost/guard")
	t.Setenv("GUARD_POLICY_PATH", "../../config/policy.txt")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  address: ':9090'\nupload:\n  max_bytes: 2048\nlog:\n  level: debug\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != ":9090" || cfg.Upload.MaxBytes != 2048 || cfg.Log.Level != "debug" {
		t.Fatal("YAML settings not applied")
	}
	for _, content := range []string{"unknown_setting: true\n", "model:\n  temperature: 0.1\n", "model:\n  api_key: secret\n"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "decode config") {
			t.Fatalf("expected unknown YAML keys to be rejected, got %v", err)
		}
	}
}
