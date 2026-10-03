package config

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/core"
	"github.com/spf13/viper"
)

type Config struct {
	Server struct {
		Address         string
		ReadTimeout     time.Duration `mapstructure:"read_timeout"`
		WriteTimeout    time.Duration `mapstructure:"write_timeout"`
		ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	} `mapstructure:"server"`
	Upload struct {
		MaxBytes      int64 `mapstructure:"max_bytes"`
		MaxLongSide   int   `mapstructure:"max_long_side"`
		MaxShortSide  int   `mapstructure:"max_short_side"`
		MaxConcurrent int   `mapstructure:"max_concurrent"`
	} `mapstructure:"upload"`
	Storage  struct{ Directory string } `mapstructure:"storage"`
	Database struct {
		DSN     string
		MaxOpen int `mapstructure:"max_open"`
		MaxIdle int `mapstructure:"max_idle"`
	} `mapstructure:"database"`
	// Model settings are fixed for the llama.cpp API and excluded from YAML.
	Model struct {
		BaseURL, Name               string
		Timeout                     time.Duration
		MaxTokens, MaxResponseBytes int64
		Temperature, TopP           float64
	} `mapstructure:"-"`
	Worker struct {
		Concurrency   int
		PollInterval  time.Duration `mapstructure:"poll_interval"`
		LeaseDuration time.Duration `mapstructure:"lease_duration"`
		MaxAttempts   int           `mapstructure:"max_attempts"`
		RetryDelay    time.Duration `mapstructure:"retry_delay"`
	} `mapstructure:"worker"`
	Policy struct{ Path string }  `mapstructure:"policy"`
	Log    struct{ Level string } `mapstructure:"log"`
}

func Load(path string) (Config, core.Policy, error) {
	v := viper.New()
	v.SetEnvPrefix("GUARD")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	defaults := map[string]any{
		"server.address": ":8080", "server.read_timeout": "15s", "server.write_timeout": "30s", "server.shutdown_timeout": "15s",
		"upload.max_bytes": 5 << 20, "upload.max_long_side": 1920, "upload.max_short_side": 1080, "upload.max_concurrent": 4,
		"storage.directory": "data/images", "database.dsn": "", "database.max_open": 10, "database.max_idle": 5,
		"worker.concurrency": 1, "worker.poll_interval": "1s", "worker.lease_duration": "150s",
		"worker.max_attempts": 3, "worker.retry_delay": "5s", "policy.path": "config/policy.txt", "log.level": "info",
	}
	for key, value := range defaults {
		v.SetDefault(key, value)
		if err := v.BindEnv(key); err != nil {
			return Config{}, core.Policy{}, err
		}
	}
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return Config{}, core.Policy{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := v.UnmarshalExact(&cfg); err != nil {
		return cfg, core.Policy{}, fmt.Errorf("decode config: %w", err)
	}
	cfg.Model.BaseURL = "http://127.0.0.1:8000/v1"
	if endpoint, ok := os.LookupEnv("GUARD_MODEL_BASE_URL"); ok {
		cfg.Model.BaseURL = endpoint
	}
	cfg.Model.Name = "Qwen/Qwen3-VL-2B-Thinking"
	cfg.Model.Timeout = 120 * time.Second
	cfg.Model.MaxTokens = 8192
	cfg.Model.MaxResponseBytes = 1 << 20
	cfg.Model.Temperature = 0.6
	cfg.Model.TopP = 0.95
	if err := cfg.Validate(); err != nil {
		return cfg, core.Policy{}, err
	}
	f, err := os.Open(cfg.Policy.Path)
	if err != nil {
		return cfg, core.Policy{}, fmt.Errorf("read policy: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, core.MaxPolicyBytes+1))
	if err != nil {
		return cfg, core.Policy{}, err
	}
	policy, err := core.NewPolicy(string(data))
	return cfg, policy, err
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Database.DSN) == "" {
		return fmt.Errorf("database.dsn or GUARD_DATABASE_DSN is required")
	}
	if c.Server.Address == "" || c.Storage.Directory == "" || c.Policy.Path == "" {
		return fmt.Errorf("server address, storage directory and policy path are required")
	}
	if c.Upload.MaxBytes <= 0 || c.Upload.MaxBytes > 64<<20 || c.Upload.MaxLongSide <= 0 || c.Upload.MaxLongSide > 1920 || c.Upload.MaxShortSide <= 0 || c.Upload.MaxShortSide > 1080 || c.Upload.MaxShortSide > c.Upload.MaxLongSide {
		return fmt.Errorf("upload limits must be positive, at most 64 MiB and FHD")
	}
	if c.Upload.MaxConcurrent < 1 || c.Upload.MaxConcurrent > 64 || c.Worker.Concurrency < 1 || c.Worker.Concurrency > 16 {
		return fmt.Errorf("upload concurrency must be 1–64 and worker concurrency 1–16")
	}
	if c.Database.MaxOpen < 1 || c.Database.MaxIdle < 0 || c.Database.MaxIdle > c.Database.MaxOpen {
		return fmt.Errorf("invalid database pool limits")
	}
	if c.Server.ReadTimeout <= 0 || c.Server.WriteTimeout <= 0 || c.Server.ShutdownTimeout <= 0 || c.Worker.PollInterval < 100*time.Millisecond || c.Worker.RetryDelay <= 0 || c.Model.Timeout <= 0 || c.Worker.LeaseDuration < c.Model.Timeout+15*time.Second {
		return fmt.Errorf("timeouts must be positive and processing lease must exceed model timeout by at least 15s")
	}
	if c.Worker.MaxAttempts < 1 || c.Worker.MaxAttempts > 10 || c.Model.MaxTokens < 1 || c.Model.MaxTokens > 40960 || c.Model.MaxResponseBytes < 1024 || c.Model.MaxResponseBytes > 16<<20 {
		return fmt.Errorf("invalid worker or model limits")
	}
	u, err := url.Parse(c.Model.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("GUARD_MODEL_BASE_URL must be an HTTP(S) endpoint without credentials, query or fragment")
	}
	return nil
}
