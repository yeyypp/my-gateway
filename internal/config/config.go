package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfig = "configs/default_config.yaml"
)

type Config struct {
	Models    []Model    `yaml:"models"`
	Upstreams []Upstream `yaml:"upstreams"`
}

type Model struct {
	Name     string `yaml:"name"`
	Upstream string `yaml:"upstream"`
}

type Upstream struct {
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
}

func Load(configFile string) (Config, error) {
	content, err := os.ReadFile(configFile)
	if err != nil {
		return Config{}, err
	}

	var config Config
	if err := yaml.NewDecoder(bytes.NewReader(content)).Decode(&config); err != nil {
		return Config{}, err
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}

	return config, nil
}

func validateConfig(config Config) error {
	// validate upstream
	if len(config.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream is required")
	}
	exists := make(map[string]struct{})
	for index, upstream := range config.Upstreams {
		if len(upstream.Name) == 0 {
			return fmt.Errorf("upstream at index %d has empty name", index)
		}
		if _, ok := exists[upstream.Name]; ok {
			return fmt.Errorf("duplicate upstream name %q", upstream.Name)
		}
		if len(upstream.BaseURL) == 0 {
			return fmt.Errorf("upstream %q has empty base URL", upstream.Name)
		}
		if err := validateBaseURL(upstream.BaseURL); err != nil {
			return fmt.Errorf("upstream %q: %w", upstream.Name, err)
		}

		exists[upstream.Name] = struct{}{}
	}

	// validate models
	if len(config.Models) == 0 {
		return fmt.Errorf("at least one model is required")
	}
	existsM := make(map[string]struct{})
	for index, model := range config.Models {
		if len(model.Name) == 0 {
			return fmt.Errorf("model at index %d has empty name", index)
		}
		if _, ok := existsM[model.Name]; ok {
			return fmt.Errorf("duplicate model name %q", model.Name)
		}
		if len(model.Upstream) == 0 {
			return fmt.Errorf("model %q has empty upstream", model.Name)
		}
		if _, ok := exists[model.Upstream]; !ok {
			return fmt.Errorf("model %q references unknown upstream %q", model.Name, model.Upstream)
		}
		existsM[model.Name] = struct{}{}
	}
	return nil
}

func validateBaseURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse upstream base URL %q: %w", rawURL, err)
	}

	if len(u.Scheme) == 0 {
		return fmt.Errorf("upstream base URL %q is missing scheme", rawURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("upstream base URL %q uses unsupported scheme %q", rawURL, u.Scheme)
	}

	if len(u.Host) == 0 {
		return fmt.Errorf("upstream base URL %q is missing host", rawURL)
	}
	return nil
}
