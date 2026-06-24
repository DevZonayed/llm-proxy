package orchestrator

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Classifier inspects the inbound request payload and assigns a difficulty
// bucket. It is intentionally cheap — a handful of gjson lookups and
// string scans — so it can run on every request without measurable cost.
type Classifier struct {
	cfg DifficultyConfig
}

// NewClassifier constructs a classifier from configuration.
func NewClassifier(cfg DifficultyConfig) *Classifier {
	return &Classifier{cfg: cfg}
}

// ClassifyInput carries the information the classifier needs. The payload
// is expected to be the raw JSON bytes the client sent (post-translation
// is fine, but the OpenAI shape is matched first).
type ClassifyInput struct {
	// Payload is the raw inbound JSON request body.
	Payload []byte
	// SourceFormat is the inbound schema identifier (e.g. "openai",
	// "claude", "gemini"). Used to pick the right JSON path for
	// message extraction.
	SourceFormat string
}

// Classify returns the difficulty bucket for the input. When the
// classifier is disabled, every request is bucketed as Medium.
func (c *Classifier) Classify(in ClassifyInput) Bucket {
	if c == nil || !c.cfg.Enabled {
		return BucketMedium
	}

	tokens := estimateTokens(in.Payload, in.SourceFormat)
	bucket := BucketEasy
	if tokens >= c.cfg.HardTokenThreshold && c.cfg.HardTokenThreshold > 0 {
		bucket = BucketHard
	} else if tokens >= c.cfg.MediumTokenThreshold && c.cfg.MediumTokenThreshold > 0 {
		bucket = BucketMedium
	}

	// Optionally promote when tools are present.
	if c.cfg.PromoteOnTools && hasTools(in.Payload, in.SourceFormat) {
		switch bucket {
		case BucketEasy:
			bucket = BucketMedium
		case BucketMedium:
			bucket = BucketHard
		}
	}
	return bucket
}

// estimateTokens returns a rough token count derived from the user
// messages in the payload. Approximation: 4 chars per token. Always
// returns 0 for an empty payload.
func estimateTokens(payload []byte, sourceFormat string) int {
	if len(payload) == 0 {
		return 0
	}
	var combined strings.Builder
	collect := func(path string) {
		gjson.GetBytes(payload, path).ForEach(func(_, v gjson.Result) bool {
			combined.WriteString(v.String())
			combined.WriteByte(' ')
			return true
		})
	}
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "claude":
		collect(`messages.#.content`)
		collect(`messages.#.content.#.text`)
		combined.WriteString(gjson.GetBytes(payload, "system").String())
	case "gemini":
		collect(`contents.#.parts.#.text`)
		combined.WriteString(gjson.GetBytes(payload, "systemInstruction.parts.0.text").String())
	default:
		// openai / codex / unknown — fall through to OpenAI shape.
		collect(`messages.#.content`)
		collect(`messages.#.content.#.text`)
	}
	text := combined.String()
	if text == "" {
		// Fall back to whole-payload length so we at least register
		// large requests as such.
		return len(payload) / 4
	}
	return len(text) / 4
}

// hasTools returns true if the payload has a non-empty tools array.
func hasTools(payload []byte, sourceFormat string) bool {
	if len(payload) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "gemini":
		return gjson.GetBytes(payload, "tools.#").Int() > 0
	default:
		return gjson.GetBytes(payload, "tools.#").Int() > 0 || gjson.GetBytes(payload, "functions.#").Int() > 0
	}
}

// estimateMessageCount counts user-visible message turns. Used by the
// classifier as a secondary signal in tests.
func estimateMessageCount(payload []byte, sourceFormat string) int {
	if len(payload) == 0 {
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "claude":
		return int(gjson.GetBytes(payload, "messages.#").Int())
	case "gemini":
		return int(gjson.GetBytes(payload, "contents.#").Int())
	default:
		return int(gjson.GetBytes(payload, "messages.#").Int())
	}
}
