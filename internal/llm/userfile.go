package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// UserFile is persisted LLM settings for terminal/CLI use (~/.codehelper/llm.json).
// Environment variables override file values. API keys are env-only for safety.
type UserFile struct {
	BaseURL        string   `json:"base_url,omitempty"`
	ChatURL        string   `json:"chat_url,omitempty"`
	CompletionPath string   `json:"completion_path,omitempty"`
	Model          string   `json:"model,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
}

// UserFilePath returns ~/.codehelper/llm.json.
func UserFilePath() (string, error) {
	dir, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "llm.json"), nil
}

// LoadUserFile reads terminal LLM settings; missing file is not an error.
func LoadUserFile() (UserFile, error) {
	p, err := UserFilePath()
	if err != nil {
		return UserFile{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return UserFile{}, nil
		}
		return UserFile{}, err
	}
	var uf UserFile
	if err := json.Unmarshal(data, &uf); err != nil {
		return UserFile{}, err
	}
	return uf, nil
}

// SaveUserFile writes terminal LLM settings (API key is never stored here).
func SaveUserFile(uf UserFile) error {
	p, err := UserFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(uf, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func mergeUserFile(base Config, uf UserFile) Config {
	if s := strings.TrimSpace(uf.BaseURL); s != "" && strings.TrimSpace(base.BaseURL) == "" {
		base.BaseURL = s
	}
	if s := strings.TrimSpace(uf.ChatURL); s != "" && strings.TrimSpace(base.ChatURL) == "" {
		base.ChatURL = s
	}
	if s := strings.TrimSpace(uf.CompletionPath); s != "" && strings.TrimSpace(base.CompletionPath) == "" {
		base.CompletionPath = s
	}
	if s := strings.TrimSpace(uf.Model); s != "" && strings.TrimSpace(base.Model) == "" {
		base.Model = s
	}
	if uf.Temperature != nil && base.Temperature == nil {
		base.Temperature = uf.Temperature
	}
	return base
}

func configFromEnvRaw() Config {
	cfg := Config{
		BaseURL:        firstNonEmpty(os.Getenv("CODEHELPER_LLM_BASE_URL"), os.Getenv("OPENAI_BASE_URL")),
		ChatURL:        strings.TrimSpace(os.Getenv("CODEHELPER_LLM_CHAT_URL")),
		CompletionPath: strings.TrimSpace(os.Getenv("CODEHELPER_LLM_COMPLETION_PATH")),
		Model:          strings.TrimSpace(os.Getenv("CODEHELPER_LLM_MODEL")),
		APIKey:         firstNonEmpty(os.Getenv("CODEHELPER_LLM_API_KEY"), os.Getenv("OPENAI_API_KEY")),
	}
	if raw := strings.TrimSpace(os.Getenv("CODEHELPER_LLM_TEMPERATURE")); raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			cfg.Temperature = &f
		}
	}
	return cfg
}
