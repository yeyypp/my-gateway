package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	validUpstream := Upstream{
		Name:         "mock-a",
		BaseURL:      "http://localhost:8091",
		DefaultModel: "real-model-a",
	}
	validModel := Model{
		LogicModel: "model-a",
		Upstreams:  []string{"mock-a"},
	}

	tests := []struct {
		name    string
		wantErr string
		config  Config
	}{
		{
			name:    "empty upstreams",
			wantErr: "at least one upstream is required",
			config:  Config{Models: []Model{validModel}},
		},
		{
			name:    "empty upstream name",
			wantErr: "upstream at index 0 has empty name",
			config: Config{
				Upstreams: []Upstream{{BaseURL: validUpstream.BaseURL}},
				Models:    []Model{validModel},
			},
		},
		{
			name:    "empty upstream base URL",
			wantErr: `upstream "mock-a" has empty base URL`,
			config: Config{
				Upstreams: []Upstream{{Name: validUpstream.Name}},
				Models:    []Model{validModel},
			},
		},
		{
			name:    "duplicate upstream name",
			wantErr: `duplicate upstream name "mock-a"`,
			config: Config{
				Upstreams: []Upstream{
					validUpstream,
					{Name: validUpstream.Name, BaseURL: "http://localhost:8092"},
				},
				Models: []Model{validModel},
			},
		},
		{
			name:    "invalid upstream base URL",
			wantErr: `upstream "mock-a": upstream base URL "localhost:8091" uses unsupported scheme "localhost"`,
			config: Config{
				Upstreams: []Upstream{
					{Name: validUpstream.Name, BaseURL: "localhost:8091"},
				},
				Models: []Model{validModel},
			},
		},
		{
			name:    "invalid upstream health URL",
			wantErr: `upstream "mock-a": health URL "localhost:8091/readyz" uses unsupported scheme "localhost"`,
			config: Config{
				Upstreams: []Upstream{{
					Name:      validUpstream.Name,
					BaseURL:   validUpstream.BaseURL,
					HealthURL: "localhost:8091/readyz",
				}},
				Models: []Model{validModel},
			},
		},
		{
			name:    "empty models",
			wantErr: "at least one model is required",
			config: Config{
				Upstreams: []Upstream{validUpstream},
			},
		},
		{
			name:    "empty model name",
			wantErr: "logic model at index 0 has empty name",
			config: Config{
				Upstreams: []Upstream{validUpstream},
				Models:    []Model{{Upstreams: []string{validUpstream.Name}}},
			},
		},
		{
			name:    "duplicate model name",
			wantErr: `duplicate model name "model-a"`,
			config: Config{
				Upstreams: []Upstream{validUpstream},
				Models: []Model{
					validModel,
					{LogicModel: validModel.LogicModel, Upstreams: []string{validUpstream.Name}},
				},
			},
		},
		{
			name:    "empty model upstream",
			wantErr: `model "model-a" has empty upstream`,
			config: Config{
				Upstreams: []Upstream{validUpstream},
				Models:    []Model{{LogicModel: validModel.LogicModel}},
			},
		},
		{
			name:    "unknown model upstream",
			wantErr: `model "model-a" references unknown upstream "mock-unknown"`,
			config: Config{
				Upstreams: []Upstream{validUpstream},
				Models:    []Model{{LogicModel: validModel.LogicModel, Upstreams: []string{"mock-unknown"}}},
			},
		},
		{
			name:    "repeated model upstream",
			wantErr: `model "model-a" repeats upstream "mock-a"`,
			config: Config{
				Upstreams: []Upstream{validUpstream},
				Models:    []Model{{LogicModel: validModel.LogicModel, Upstreams: []string{"mock-a", "mock-a"}}},
			},
		},
		{
			name: "valid ordered upstreams",
			config: Config{
				Upstreams: []Upstream{validUpstream, {Name: "mock-b", BaseURL: "http://localhost:8092", DefaultModel: "real-model-b"}},
				Models:    []Model{{LogicModel: validModel.LogicModel, Upstreams: []string{"mock-a", "mock-b"}}},
			},
		},
		{
			name: "valid config",
			config: Config{
				Upstreams: []Upstream{validUpstream},
				Models:    []Model{validModel},
			},
		},
		{
			name: "valid config with optional health URL",
			config: Config{
				Upstreams: []Upstream{{
					Name:      validUpstream.Name,
					BaseURL:   validUpstream.BaseURL,
					HealthURL: "http://localhost:8091/readyz",
				}},
				Models: []Model{validModel},
			},
		},
		{
			name: "API key resolved from environment",
			config: Config{
				Upstreams: []Upstream{{
					Name:      validUpstream.Name,
					BaseURL:   validUpstream.BaseURL,
					APIKeyEnv: "GATEWAY_TEST_API_KEY",
					APIKey:    "sk-test-secret",
				}},
				Models: []Model{validModel},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.config)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error %q, got nil", tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("expected error %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{
			name:   "HTTP URL",
			rawURL: "http://localhost:8091",
		},
		{
			name:   "HTTPS URL with path",
			rawURL: "https://api.example.com/v1",
		},
		{
			name:    "relative URL",
			rawURL:  "mock-a",
			wantErr: `upstream base URL "mock-a" is missing scheme`,
		},
		{
			name:    "unsupported scheme",
			rawURL:  "ftp://example.com",
			wantErr: `upstream base URL "ftp://example.com" uses unsupported scheme "ftp"`,
		},
		{
			name:    "missing host",
			rawURL:  "http:/v1",
			wantErr: `upstream base URL "http:/v1" is missing host`,
		},
		{
			name:    "malformed URL",
			rawURL:  "%",
			wantErr: `parse upstream base URL "%": parse "%": invalid URL escape "%"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBaseURL(tc.rawURL)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error %q, got nil", tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("expected error %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestValidateHealthURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{
			name:   "HTTP URL with path",
			rawURL: "http://localhost:8091/readyz",
		},
		{
			name:    "relative URL",
			rawURL:  "/readyz",
			wantErr: `health URL "/readyz" is missing scheme`,
		},
		{
			name:    "unsupported scheme",
			rawURL:  "ftp://example.com/readyz",
			wantErr: `health URL "ftp://example.com/readyz" uses unsupported scheme "ftp"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHealthURL(tc.rawURL)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error %q, got nil", tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("expected error %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Setenv("GATEWAY_TEST_API_KEY", "sk-test-secret")

	tests := []struct {
		name       string
		content    string
		wantConfig Config
		wantErr    string
	}{
		{
			name: "valid YAML",
			content: `upstreams:
  - name: mock-a
    base_url: http://localhost:8091
    health_url: http://localhost:8091/readyz
    default_model: real-model-a
models:
  - logic_model: model-a
    upstreams:
      - mock-a
`,
			wantConfig: Config{
				Upstreams: []Upstream{{
					Name:         "mock-a",
					BaseURL:      "http://localhost:8091",
					HealthURL:    "http://localhost:8091/readyz",
					DefaultModel: "real-model-a",
				}},
				Models: []Model{{LogicModel: "model-a", Upstreams: []string{"mock-a"}}},
			},
		},
		{
			name:    "malformed YAML",
			content: "upstreams: [",
			wantErr: "yaml:",
		},
		{
			name: "invalid configuration",
			content: `upstreams:
  - name: mock-a
    base_url: http://localhost:8091
    default_model: real-model-a
models:
  - logic_model: model-a
    upstreams:
      - mock-unknown
`,
			wantErr: `model "model-a" references unknown upstream "mock-unknown"`,
		},
		{
			name: "API key env resolved into APIKey",
			content: `upstreams:
  - name: mock-a
    base_url: http://localhost:8091
    default_model: real-model-a
    api_key_env: GATEWAY_TEST_API_KEY
models:
  - logic_model: model-a
    upstreams:
      - mock-a
`,
			wantConfig: Config{
				Upstreams: []Upstream{{
					Name:         "mock-a",
					BaseURL:      "http://localhost:8091",
					APIKeyEnv:    "GATEWAY_TEST_API_KEY",
					APIKey:       "sk-test-secret",
					DefaultModel: "real-model-a",
				}},
				Models: []Model{{LogicModel: "model-a", Upstreams: []string{"mock-a"}}},
			},
		},
		{
			// A missing credential must not fail startup; readiness reports it.
			name: "unset API key env leaves APIKey empty without failing",
			content: `upstreams:
  - name: mock-a
    base_url: http://localhost:8091
    default_model: real-model-a
    api_key_env: GATEWAY_TEST_MISSING_KEY
models:
  - logic_model: model-a
    upstreams:
      - mock-a
`,
			wantConfig: Config{
				Upstreams: []Upstream{{
					Name:         "mock-a",
					BaseURL:      "http://localhost:8091",
					APIKeyEnv:    "GATEWAY_TEST_MISSING_KEY",
					DefaultModel: "real-model-a",
				}},
				Models: []Model{{LogicModel: "model-a", Upstreams: []string{"mock-a"}}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(configPath, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write config file: %v", err)
			}

			got, err := Load(configPath)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				if !reflect.DeepEqual(got, tc.wantConfig) {
					t.Fatalf("expected config %+v, got %+v", tc.wantConfig, got)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing.yaml")
	if _, err := Load(configPath); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}
