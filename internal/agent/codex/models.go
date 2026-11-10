package codex

import "github.com/tigercosmos/codexmon/internal/agent"

// Models lists current Codex choices, with the default model first.
func (Provider) Models() []agent.Model {
	return []agent.Model{
		{ID: defaultModel, Name: "GPT-6.1 Sol", Description: "Near-Astra performance at a lower cost", Default: true},
		{ID: "gpt-6-astra", Name: "GPT-6 Astra", Description: "Most capable for demanding reasoning and coding"},
		{ID: "gpt-6.1-terra", Name: "GPT-6.1 Terra", Description: "Fast, efficient model for writing, visual work, and computer use"},
		{ID: "gpt-6-sol", Name: "GPT-6 Sol", Description: "Previous-generation workhorse"},
		{ID: "gpt-6-luna", Name: "GPT-6 Luna", Description: "Fast, efficient model for focused tasks"},
	}
}
