package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvironmentOverridesAndValidation(t *testing.T) {
	t.Setenv("GUARD_DATABASE_DSN", "postgres://guard:password@localhost/guard")
	t.Setenv("GUARD_MODEL_NAME", "local-thinking")
	t.Setenv("GUARD_UPLOAD_MAX_BYTES", "1024")
	t.Setenv("GUARD_POLICY_PATH", "does-not-exist.txt")
	config, policy, err := Load("../../config/config.yaml")
	// Policy paths are relative to the working directory, so override it explicitly.
	if err == nil {
		t.Fatal("unexpected policy path resolution")
	}
	t.Setenv("GUARD_POLICY_PATH", "../../config/policy.txt")
	config, policy, err = Load("../../config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if config.Model.Name != "local-thinking" || config.Upload.MaxBytes != 1024 || policy.Hash() == "" {
		t.Fatal("environment override not applied")
	}
	config.Worker.LeaseDuration = config.Model.Timeout
	if err := config.Validate(); err == nil {
		t.Fatal("accepted short lease")
	}
	config.Upload.MaxLongSide = 1921
	if err := config.Validate(); err == nil {
		t.Fatal("accepted greater than FHD")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("unknown_setting: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("accepted unknown configuration key")
	}
}
