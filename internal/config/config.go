package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	Nomad     NomadConfig     `json:"nomad"`
	HAProxy   HAProxyConfig   `json:"haproxy"`
	Log       LogConfig       `json:"log"`
	DomainMap DomainMapConfig `json:"domain_map"`
}

type NomadConfig struct {
	Address string `json:"address"`
	Token   string `json:"token"`
	Region  string `json:"region"`
}

type HAProxyConfig struct {
	Address         string `json:"address"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	BackendStrategy string `json:"backend_strategy"`
}

type LogConfig struct {
	Level string `json:"level"`
}

type DomainMapConfig struct {
	Enabled  bool   `json:"enabled"`
	FilePath string `json:"file_path"`
}

// Load configuration from file or environment variables
func Load(configFile string) (*Config, error) {
	cfg := &Config{
		// Default values
		Nomad: NomadConfig{
			Address: getEnv("NOMAD_ADDR", "http://localhost:4646"),
			Token:   getEnv("NOMAD_TOKEN", ""),
			Region:  getEnv("NOMAD_REGION", "global"),
		},
		HAProxy: HAProxyConfig{
			Address:         getEnv("HAPROXY_DATAPLANE_URL", "http://localhost:5555"),
			Username:        getEnv("HAPROXY_USERNAME", "admin"),
			Password:        getEnv("HAPROXY_PASSWORD", "adminpwd"),
			BackendStrategy: getEnv("HAPROXY_BACKEND_STRATEGY", "use_existing"),
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
		},
		DomainMap: DomainMapConfig{
			Enabled:  getEnv("DOMAIN_MAP_ENABLED", "false") == "true",
			FilePath: getEnv("DOMAIN_MAP_FILE_PATH", "/etc/haproxy2/domain-backend.map"),
		},
	}

	// Load from file if provided
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
