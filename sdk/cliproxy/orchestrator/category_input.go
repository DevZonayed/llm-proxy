package orchestrator

import (
	"strings"

	"github.com/tidwall/gjson"
)

// buildCategoryInput extracts the user-visible text, tool presence, and
// rough token count from a raw request payload. The output drives the
// heuristic category matcher and the LLM-based classifier's prompt.
//
// The function tolerates Claude / OpenAI / Gemini shaped payloads — it
// tries each known path in turn and concatenates whatever it finds. The
// returned text is lowercased so the matcher's substring checks don't
// re-walk the string.
func buildCategoryInput(payload []byte, sourceFormat string) CategoryInput {
	if len(payload) == 0 {
		return CategoryInput{}
	}
	text := extractUserText(payload, sourceFormat)
	tokens := len(text) / 4
	return CategoryInput{
		UserText:     strings.ToLower(text),
		HasTools:     hasTools(payload, sourceFormat),
		ApproxTokens: tokens,
	}
}

// extractUserText concatenates the textual content of every user-role
// message in the payload. System messages are intentionally excluded —
// they bias the classifier toward the proxy's prompts rather than the
// caller's task.
func extractUserText(payload []byte, sourceFormat string) string {
	if len(payload) == 0 {
		return ""
	}
	var b strings.Builder
	add := func(s string) {
		if s == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s)
	}
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "claude":
		// Anthropic messages may have content as string or as array of
		// {type, text} parts.
		gjson.GetBytes(payload, "messages").ForEach(func(_, m gjson.Result) bool {
			if m.Get("role").String() != "user" {
				return true
			}
			content := m.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					if t := part.Get("text"); t.Exists() {
						add(t.String())
					}
					return true
				})
			} else {
				add(content.String())
			}
			return true
		})
	case "gemini":
		gjson.GetBytes(payload, "contents").ForEach(func(_, c gjson.Result) bool {
			role := c.Get("role").String()
			if role != "" && role != "user" {
				return true
			}
			c.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if t := part.Get("text"); t.Exists() {
					add(t.String())
				}
				return true
			})
			return true
		})
	default:
		// openai / codex / unknown — OpenAI shape. Content may be a
		// string or an array of {type, text} parts. Non-text parts
		// (image_url etc.) are silently skipped.
		gjson.GetBytes(payload, "messages").ForEach(func(_, m gjson.Result) bool {
			if m.Get("role").String() != "user" {
				return true
			}
			content := m.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					if t := part.Get("text"); t.Exists() {
						add(t.String())
					}
					return true
				})
			} else {
				add(content.String())
			}
			return true
		})
	}
	return b.String()
}
