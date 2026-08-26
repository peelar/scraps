package main

import (
	"encoding/json"
	"errors"
	"os"
)

const defaultDaemonURL = "http://127.0.0.1:8484"

type clientConfig struct {
	DaemonURL string   `json:"daemon_url,omitempty"`
	Token     string   `json:"token,omitempty"`
	EnvAllow  []string `json:"env_allow,omitempty"`
}

func readClientProfile(path string) clientConfig {
	configured, _ := loadClientProfile(path)
	return configured
}

func loadClientProfile(path string) (clientConfig, error) {
	configured := clientConfig{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return configured, nil
	}
	if err != nil {
		return configured, err
	}
	if err := json.Unmarshal(data, &configured); err != nil {
		return clientConfig{}, err
	}
	return configured, nil
}

func resolvedClientConfig() clientConfig {
	configured := readClientProfile(clientProfilePath())
	if value, ok := os.LookupEnv("SCRAP_DAEMON_URL"); ok && value != "" {
		configured.DaemonURL = value
	}
	if value, ok := os.LookupEnv("SCRAP_TOKEN"); ok {
		configured.Token = value
	}
	if configured.DaemonURL == "" {
		configured.DaemonURL = defaultDaemonURL
	}
	return configured
}
