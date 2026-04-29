package agent

import "fmt"

// Config is the client-side configuration that picks which Provider
// implementation to use. The provider name is normalised to lower-case
// and matched against a small switch — adding a new provider is a
// matter of writing the adapter (see openai.go / ollama.go /
// claudecode.go) and adding the case here.
type Config struct {
	Provider         string // anthropic | openai | ollama | claude-code
	AnthropicKey     string
	OpenAIKey        string
	OllamaURL        string
	Model            string
	ClaudeBinary     string // path to the `claude` CLI; empty = look on PATH
	ClaudeWorkingDir string // cwd the claude CLI runs in
}

// New returns the Provider for the configured name. Defaults to
// Anthropic so existing setups keep working.
func New(cfg Config) (Provider, error) {
	switch cfg.Provider {
	case "", "anthropic":
		return NewAnthropic(cfg.AnthropicKey, cfg.Model), nil
	case "openai":
		return NewOpenAI(cfg.OpenAIKey, cfg.Model), nil
	case "ollama":
		return NewOllama(cfg.OllamaURL, cfg.Model), nil
	case "claude-code", "claude":
		return NewClaudeCode(cfg.ClaudeBinary, cfg.Model, cfg.ClaudeWorkingDir), nil
	}
	return nil, fmt.Errorf("unknown provider %q (want anthropic|openai|ollama|claude-code)", cfg.Provider)
}
