package codex

import "github.com/tigercosmos/codexmon/internal/agent"

// Models lists current Codex choices, with the highest-capability model first.
func (Provider) Models() []agent.Model {
	return []agent.Model{
		{ID: defaultModel, Name: "GPT-6 Astra", Description: "Most capable for demanding reasoning and coding", Default: true},
		{ID: "gpt-6.1-sol", Name: "GPT-6.1 Sol", Description: "Near-Astra performance at a lower cost"},
		{ID: "gpt-6-sol", Name: "GPT-6 Sol", Description: "Previous-generation workhorse"},
		{ID: "gpt-6-luna", Name: "GPT-6 Luna", Description: "Fast, efficient model for focused tasks"},
	}
}
