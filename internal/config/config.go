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
		return fmt.Errorf("empty upstreams")
	}
	exists := make(map[string]struct{})
	for _, upstream := range config.Upstreams {
		if _, ok := exists[upstream.Name]; ok {
			return fmt.Errorf("replicate upstream name")
		}
		if len(upstream.BaseURL) == 0 {
			return fmt.Errorf("empty upstream base url")
		}
		if err := validateBaseURL(upstream.BaseURL); err != nil {
			return err
		}
		if len(upstream.Name) == 0 {
			return fmt.Errorf("empty upstream name")
		}

		exists[upstream.Name] = struct{}{}
	}

	// validate models
	if len(config.Models) == 0 {
		return fmt.Errorf("empty models")
	}
	existsM := make(map[string]struct{})
	for _, model := range config.Models {
		if len(model.Name) == 0 {
			return fmt.Errorf("empty model name")
		}
		if _, ok := existsM[model.Name]; ok {
			return fmt.Errorf("replicate model name")
		}
		if _, ok := exists[model.Upstream]; !ok {
			return fmt.Errorf("upstream doesn't exist")
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
