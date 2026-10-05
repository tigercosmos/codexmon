package agent

// Model describes a documented model choice, not account-specific availability.
type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Default     bool   `json:"default"`
}

// ModelProvider exposes a curated model list without probing the agent or account.
// Native model arguments remain unrestricted, including models outside this list.
type ModelProvider interface {
	Models() []Model
}
