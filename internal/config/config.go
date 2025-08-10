package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	Nomad   NomadConfig   `json:"nomad"`
	HAProxy HAProxyConfig `json:"haproxy"`
	Log     LogConfig     `json:"log"`
}

type NomadConfig struct {
	Address string `json:"address"`
	Token   string `json:"token"`
	Region  string `json:"region"`
}

type HAProxyConfig struct {
	Address  string `json:"address"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type LogConfig struct {
	Level string `json:"level"`
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
			Address:  getEnv("HAPROXY_DATAPLANE_URL", "http://localhost:5555"),
			Username: getEnv("HAPROXY_USERNAME", "admin"),
			Password: getEnv("HAPROXY_PASSWORD", "adminpwd"),
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
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