package agent

import "encoding/json"

// ContentBlock is one block of an Anthropic-style message `content` array. Both
// Claude Code and the Cursor agent stream messages in this shape, so the two
// providers share one decoder; each reads only the fields its own stream uses.
type ContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`          // tool_use id
	Name      string `json:"name"`        // tool name (for tool_use)
	ToolUseID string `json:"tool_use_id"` // matching id on a tool_result
}

// DecodeBlocks pulls the content blocks out of a streamed message. `content` is
// normally an array of blocks, but a plain message may carry a bare string
// instead; both are tolerated, and anything else yields no blocks.
func DecodeBlocks(raw json.RawMessage) []ContentBlock {
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || len(msg.Content) == 0 {
		return nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return []ContentBlock{{Type: "text", Text: s}}
	}
	return nil
}
