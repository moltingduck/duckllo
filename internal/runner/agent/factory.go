package agent

import "fmt"

// Config is the runner-side configuration that picks which Provider
// implementation to use. The provider name is normalised to lower-case
// and matched against a small switch — adding a new provider is a
// matter of writing the adapter (see openai.go / ollama.go) and adding
// the case here.
type Config struct {
	Provider     string // anthropic | openai | ollama
	AnthropicKey string
	OpenAIKey    string
	OllamaURL    string
	Model        string
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
	}
	return nil, fmt.Errorf("unknown provider %q (want anthropic|openai|ollama)", cfg.Provider)
}
