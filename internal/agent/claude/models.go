package claude

import "github.com/tigercosmos/codexmon/internal/agent"

// Models lists current Claude choices, with the highest-capability model first.
func (Provider) Models() []agent.Model {
	return []agent.Model{
		{ID: defaultModel, Name: "Claude Fable 5.1", Description: "Demanding reasoning and long-horizon agentic work", Default: true},
		{ID: "claude-opus-5-5", Name: "Claude Opus 5.5", Description: "Long-running agentic coding and knowledge work"},
		{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5", Description: "Balanced speed and intelligence"},
		{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5", Description: "Fast, efficient model for focused tasks"},
	}
}
