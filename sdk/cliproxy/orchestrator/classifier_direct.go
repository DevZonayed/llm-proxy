package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// directModelClassifier picks a catalog entry id directly from the
// catalog's paragraph-level Instructions field. It is the "operator
// writes one paragraph per model and the LLM picks" mode requested in
// the v2.1 design — no categories required.
//
// Failure modes (timeout, unknown reply, missing provider) return ""
// so the orchestrator falls back to the configured fallback path.
type directModelClassifier struct {
	cfg     ClassifierLLMConfig
	catalog []CatalogEntry
	idx     *catalogIndex
	mgr     AuthManager
	tmpl    string
	cache   *llmCache // nil when CacheTTL == 0
	mu      sync.Mutex
	idLower map[string]string // lowercase(id) -> canonical id
}

// newDirectModelClassifier constructs a direct-model classifier from
// the supplied LLM config + catalog. The cache and template defaults
// mirror the category classifier.
func newDirectModelClassifier(cfg ClassifierLLMConfig, catalog []CatalogEntry, mgr AuthManager) *directModelClassifier {
	tmpl := strings.TrimSpace(cfg.PromptTemplate)
	if tmpl == "" {
		tmpl = defaultDirectModelPrompt
	}
	c := &directModelClassifier{
		cfg:     cfg,
		catalog: append([]CatalogEntry(nil), catalog...),
		idx:     newCatalogIndex(catalog),
		mgr:     mgr,
		tmpl:    tmpl,
		idLower: make(map[string]string, len(catalog)),
	}
	if cfg.CacheTTL > 0 {
		c.cache = newLLMCache(cfg.CacheTTL)
	}
	for _, e := range catalog {
		id := e.EffectiveID()
		if id == "" {
			continue
		}
		c.idLower[strings.ToLower(strings.TrimSpace(id))] = id
	}
	return c
}

// classify asks the upstream model to pick a catalog id. Returns the
// canonical id on success, or "" on any error path so the caller can
// fall back.
func (c *directModelClassifier) classify(ctx context.Context, in CategoryInput, req DecideRequest) string {
	if c == nil || !c.cfg.Enabled || len(c.catalog) == 0 {
		return ""
	}
	if strings.TrimSpace(c.cfg.Provider) == "" || strings.TrimSpace(c.cfg.Model) == "" {
		return ""
	}
	// The classifier provider must be in the candidate set — otherwise
	// we'd ship the routing call somewhere it doesn't belong.
	if !providerInSet(c.cfg.Provider, req.Providers) {
		return ""
	}

	text := in.UserText
	maxChars := c.cfg.MaxInputChars
	if maxChars <= 0 {
		maxChars = 4000
	}
	if len(text) > maxChars {
		text = text[:maxChars]
	}

	cacheKey := c.cacheKey(text)
	if c.cache != nil {
		if hit, ok := c.cache.get(cacheKey); ok {
			return hit
		}
	}

	// Filter the catalog to entries whose provider is in the
	// candidate set — there is no point asking the classifier to pick
	// a model the orchestrator can't dispatch to.
	available := c.eligibleEntries(req.Providers)
	if len(available) == 0 {
		return ""
	}

	prompt := c.renderPrompt(text, available)
	payload, err := c.buildOpenAIPayload(prompt)
	if err != nil {
		return ""
	}

	timeout := c.cfg.Timeout
	if timeout <= 0 {
		timeout = 800 * time.Millisecond
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := c.mgr.Execute(callCtx, []string{c.cfg.Provider}, coreexecutor.Request{
		Model:   c.cfg.Model,
		Payload: payload,
	}, coreexecutor.Options{
		Stream:       false,
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		return ""
	}

	raw := strings.TrimSpace(extractAssistantText(resp.Payload))
	if raw == "" {
		return ""
	}
	picked := c.normalizeChoice(raw, available)
	if picked == "" {
		return ""
	}
	if c.cache != nil {
		c.cache.put(cacheKey, picked)
	}
	return picked
}

// eligibleEntries filters the catalog to entries servable by the
// supplied candidate provider set. Preserves catalog order so the
// prompt-rendered list matches what operators wrote.
func (c *directModelClassifier) eligibleEntries(providers []string) []CatalogEntry {
	if len(c.catalog) == 0 || len(providers) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(providers))
	for _, p := range providers {
		set[p] = struct{}{}
	}
	out := make([]CatalogEntry, 0, len(c.catalog))
	for _, e := range c.catalog {
		if _, ok := set[e.Provider]; ok {
			out = append(out, e)
		}
	}
	return out
}

// renderPrompt builds the user-message body. Placeholders supported in
// the template:
//   - {{models}}:  the catalog block, "[id] instructions\n\n" per entry
//   - {{request}}: the lowercased user-text excerpt
func (c *directModelClassifier) renderPrompt(userText string, entries []CatalogEntry) string {
	out := c.tmpl
	if strings.Contains(out, "{{models}}") {
		out = strings.ReplaceAll(out, "{{models}}", renderCatalogBlock(entries))
	}
	out = strings.ReplaceAll(out, "{{request}}", userText)
	return out
}

// renderCatalogBlock formats the catalog into the routing prompt. Each
// entry is rendered as "[id] instructions" followed by a blank line so
// the model sees a clear separation between candidates.
func renderCatalogBlock(entries []CatalogEntry) string {
	var b strings.Builder
	for _, e := range entries {
		id := e.EffectiveID()
		if id == "" {
			continue
		}
		instr := e.EffectiveInstructions()
		if instr == "" {
			// Fall back to a minimal "model: provider/model" line so
			// the classifier still has SOMETHING to compare.
			instr = "Provider " + e.Provider + ", model " + e.Model + "."
		}
		b.WriteString("[")
		b.WriteString(id)
		b.WriteString("]\n")
		b.WriteString(instr)
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildOpenAIPayload constructs a minimal OpenAI chat completion body.
func (c *directModelClassifier) buildOpenAIPayload(prompt string) ([]byte, error) {
	body := map[string]any{
		"model": c.cfg.Model,
		"messages": []map[string]any{
			{"role": "system", "content": directModelSystemPrompt},
			{"role": "user", "content": prompt},
		},
		"stream":      false,
		"temperature": 0,
	}
	return json.Marshal(body)
}

// normalizeChoice resolves the LLM's free-form reply to a canonical
// catalog id. We accept several common verbosity wrappers (brackets,
// "model: id", quotes, leading explanation) and finally fall back to
// a substring search.
func (c *directModelClassifier) normalizeChoice(raw string, available []CatalogEntry) string {
	if c == nil || len(c.idLower) == 0 {
		return ""
	}
	// Build a quick available-set so we don't return an entry the
	// orchestrator can't dispatch to.
	allowed := make(map[string]struct{}, len(available))
	for _, e := range available {
		allowed[strings.ToLower(strings.TrimSpace(e.EffectiveID()))] = struct{}{}
	}

	candidates := []string{raw}
	if idx := strings.LastIndex(raw, ":"); idx >= 0 && idx < len(raw)-1 {
		candidates = append(candidates, strings.TrimSpace(raw[idx+1:]))
	}
	if first := strings.SplitN(raw, "\n", 2)[0]; first != raw {
		candidates = append(candidates, strings.TrimSpace(first))
	}
	for _, cand := range candidates {
		key := strings.ToLower(strings.TrimSpace(cand))
		key = strings.Trim(key, `."'` + "`" + `,;:()[]{}`)
		if key == "" {
			continue
		}
		if _, ok := allowed[key]; !ok {
			continue
		}
		if name, found := c.idLower[key]; found {
			return name
		}
	}
	// Substring fallback. Prefer the longest match so "claude-opus-4.8"
	// wins over "opus" if both happen to appear.
	rawLower := strings.ToLower(raw)
	best := ""
	for key, name := range c.idLower {
		if _, ok := allowed[key]; !ok {
			continue
		}
		if strings.Contains(rawLower, key) && len(key) > len(best) {
			best = name
		}
	}
	return best
}

// cacheKey returns a short hex hash for the supplied input text. We
// salt with the classifier model so a config change invalidates the
// cache.
func (c *directModelClassifier) cacheKey(text string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("direct\x00"))
	_, _ = h.Write([]byte(c.cfg.Model))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// directModelSystemPrompt asks the classifier to reply with one id.
const directModelSystemPrompt = "You are a routing classifier for an LLM proxy. " +
	"Given a user request and a catalog of available models (each with " +
	"a paragraph describing what it is best at), pick the single best " +
	"model id. Reply with ONLY the chosen id wrapped in nothing — no " +
	"brackets, no quotes, no explanation."

// defaultDirectModelPrompt is the user-message template the direct-
// model classifier uses when the operator hasn't supplied one. Two
// placeholders: {{models}} and {{request}}.
const defaultDirectModelPrompt = "Available models:\n\n{{models}}\n\n" +
	"User request (lowercased excerpt):\n{{request}}\n\n" +
	"Reply with exactly one model id from the [bracketed] headers above."
