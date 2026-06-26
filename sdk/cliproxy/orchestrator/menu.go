package orchestrator

import (
	"sort"
	"strings"
)

// defaultRouterSystemPrompt is what the master model sees when no override
// is configured. Kept short on purpose — the master is meant to be a cheap
// classifier, not a thinker.
const defaultRouterSystemPrompt = `You are a routing classifier. Read the user's request and pick the ONE model that will produce the best answer for the smallest cost. Be conservative — prefer cheap fast models unless the task is clearly heavy (long-context, agentic coding, hard reasoning, vision, image generation).

Reply with ONLY a JSON object: {"model": "<id>", "reason": "<short>"}. No prose, no markdown fences.`

// defaultModelCatalog is the curated menu of upstream model ids the master
// may choose from when AllowedModels is empty. Each entry pairs an id with a
// one-line strength description; the menu is what the master sees in its
// system prompt.
//
// Source of truth: the 29 models currently exposed by /v1/models on
// llm.nexalance.cloud (see .continuum/STATE.md). Keep this list in sync if
// the upstream provider OAuth pools change.
var defaultModelCatalog = []catalogEntry{
	// --- Anthropic / Claude (12 ids) ---
	{"claude-opus-4-8", "heavy reasoning + agentic coding, top SWE-bench score (slow, expensive)"},
	{"claude-opus-4-7", "long agentic coding sessions, top MCP-Atlas (slightly cheaper than 4-8)"},
	{"claude-opus-4-5-20251101", "code review and critic, strong general critic (cheaper than 4-7)"},
	{"claude-opus-4-1-20250805", "older Opus 4.1 — only when others unavailable"},
	{"claude-opus-4-20250514", "older Opus 4 — only when others unavailable"},
	{"claude-sonnet-4-5-20250929", "balanced Claude, OSWorld leader, best browser-agent under Claude"},
	{"claude-sonnet-4-20250514", "older Sonnet 4 — fallback only"},
	{"claude-3-7-sonnet-20250219", "legacy Sonnet — fallback only"},
	{"claude-haiku-4-5-20251001", "cheap + fast Anthropic, strong instruction following, good JSON"},
	{"claude-3-5-haiku-20241022", "cheapest legacy Haiku — bulk classification only"},
	{"claude-fable-5", "narrative/creative Anthropic flagship (may be account-gated)"},
	{"claude-sonnet-4-6", "antigravity-rebadged Sonnet 4.6 — balanced via Antigravity OAuth"},
	{"claude-opus-4-6-thinking", "explicit step-by-step reasoning, SOTA ARC-AGI-2 (via Antigravity OAuth)"},
	{"claude-opus-4-6", "Opus 4.6 base — strong reasoning"},

	// --- OpenAI / Codex (6 ids) ---
	{"gpt-5.5", "OpenAI flagship, general-purpose"},
	{"gpt-5.4", "OpenAI prior flagship"},
	{"gpt-5.4-mini", "cheap OpenAI mini, fast, reliable JSON / function-calling"},
	{"gpt-5.3-codex-spark", "OpenAI's real-time coding tier — quick file edits, fix-the-bug"},
	{"codex-auto-review", "purpose-built code reviewer model"},
	{"gpt-image-2", "best text-to-image generator (4K, ~99% text rendering)"},

	// --- Google / Antigravity Gemini (8 ids) ---
	{"gemini-3-flash", "best Gemini multimodal for vision / charts / UI"},
	{"gemini-3-flash-agent", "agentic harness build, best for browser-use loops"},
	{"gemini-3.1-pro-low", "1M-context Gemini Pro, multilingual leader, cheap thinking tier"},
	{"gemini-3.1-flash-lite", "cheap Gemini Flash"},
	{"gemini-3.1-flash-image", "image generator, ~50% the cost of gpt-image-2"},
	{"gemini-3.5-flash-low", "Gemini 3.5 Flash, low-thinking tier"},
	{"gemini-3.5-flash-extra-low", "cheapest + fastest in the entire menu — high QPS / classification / fallback"},
	{"gemini-pro-agent", "Gemini Pro agentic harness"},

	// --- Other (1 id) ---
	{"gpt-oss-120b-medium", "open-weight 120B — only when open-weight semantics matter"},
}

type catalogEntry struct {
	ID   string
	Desc string
}

// buildModelMenu renders the catalog (or the operator's whitelist) as a
// markdown bullet list the master sees in its system prompt.
//
// If allowed is non-empty, only those ids are shown. Unknown ids in `allowed`
// are still listed (with no description) so the master is never told about
// a model the operator hasn't authorized AND never hides one they have.
func buildModelMenu(allowed []string) string {
	if len(allowed) == 0 {
		var b strings.Builder
		for _, e := range defaultModelCatalog {
			b.WriteString("- ")
			b.WriteString(e.ID)
			b.WriteString(" — ")
			b.WriteString(e.Desc)
			b.WriteByte('\n')
		}
		return b.String()
	}

	descByID := make(map[string]string, len(defaultModelCatalog))
	for _, e := range defaultModelCatalog {
		descByID[strings.ToLower(e.ID)] = e.Desc
	}

	uniq := make([]string, 0, len(allowed))
	seen := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if seen[key] {
			continue
		}
		seen[key] = true
		uniq = append(uniq, id)
	}
	sort.Strings(uniq)

	var b strings.Builder
	for _, id := range uniq {
		b.WriteString("- ")
		b.WriteString(id)
		if d, ok := descByID[strings.ToLower(id)]; ok && d != "" {
			b.WriteString(" — ")
			b.WriteString(d)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
