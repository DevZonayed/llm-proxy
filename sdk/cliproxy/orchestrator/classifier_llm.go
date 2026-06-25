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

// llmCategoryClassifier asks a cheap upstream model to pick one of the
// configured categories. It is the "intelligent" classifier referenced
// in the design doc — the one that can handle 50 models and dozens of
// categories without a hand-written heuristic for each.
//
// Failure modes (timeout, unknown reply, missing provider) all return
// "" so the orchestrator falls back to the heuristic matcher or the
// configured DefaultCategory.
type llmCategoryClassifier struct {
	cfg     ClassifierLLMConfig
	cat     *categoryClassifier
	mgr     AuthManager
	tmpl    string
	cache   *llmCache // nil when CacheTTL == 0
	mu      sync.Mutex
	clNames map[string]string // lower(name) -> canonical name
}

// newLLMCategoryClassifier constructs the LLM classifier.
func newLLMCategoryClassifier(cfg ClassifierLLMConfig, cat *categoryClassifier, mgr AuthManager) *llmCategoryClassifier {
	tmpl := strings.TrimSpace(cfg.PromptTemplate)
	if tmpl == "" {
		tmpl = defaultClassifierPrompt
	}
	c := &llmCategoryClassifier{
		cfg:  cfg,
		cat:  cat,
		mgr:  mgr,
		tmpl: tmpl,
	}
	if cfg.CacheTTL > 0 {
		c.cache = newLLMCache(cfg.CacheTTL)
	}
	if cat != nil {
		c.clNames = make(map[string]string, len(cat.categories))
		for _, k := range cat.categories {
			c.clNames[strings.ToLower(strings.TrimSpace(k.Name))] = k.Name
		}
	}
	return c
}

// classify asks the configured upstream model to pick a category. It
// returns the canonical category name on success, or "" on any error
// path so the caller can fall back.
func (c *llmCategoryClassifier) classify(ctx context.Context, in CategoryInput, req DecideRequest) string {
	if c == nil || !c.cfg.Enabled || c.cat == nil {
		return ""
	}
	if strings.TrimSpace(c.cfg.Provider) == "" || strings.TrimSpace(c.cfg.Model) == "" {
		return ""
	}
	// Provider must be in the candidate set; otherwise we'd ship the
	// classifier call somewhere it doesn't belong.
	available := req.Providers
	if !providerInSet(c.cfg.Provider, available) {
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

	prompt := c.renderPrompt(text)
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
	picked := c.normalizeChoice(raw)
	if picked == "" {
		return ""
	}
	if c.cache != nil {
		c.cache.put(cacheKey, picked)
	}
	return picked
}

// renderPrompt substitutes the placeholders in the configured (or
// default) prompt template. Supported placeholders:
//   - {{categories}}: bullet-list of "name: instructions"
//   - {{request}}:    excerpt of the user-visible text
func (c *llmCategoryClassifier) renderPrompt(userText string) string {
	out := c.tmpl
	if c.cat != nil {
		out = strings.ReplaceAll(out, "{{categories}}", c.cat.categoriesSummary())
	}
	out = strings.ReplaceAll(out, "{{request}}", userText)
	return out
}

// buildOpenAIPayload constructs a minimal OpenAI-shaped chat completion
// body. We deliberately use the OpenAI shape regardless of the inbound
// request format — the LLM classifier picks its own provider, and
// every supported provider can be reached via the OpenAI-compat path
// already exercised by the orchestrator's Worker/Verifier turns.
func (c *llmCategoryClassifier) buildOpenAIPayload(prompt string) ([]byte, error) {
	body := map[string]any{
		"model": c.cfg.Model,
		"messages": []map[string]any{
			{"role": "system", "content": classifierSystemPrompt},
			{"role": "user", "content": prompt},
		},
		"stream":      false,
		"temperature": 0,
	}
	return json.Marshal(body)
}

// normalizeChoice resolves the LLM's free-form reply to a canonical
// category name. Empty reply, multi-word reply we can't parse, or a
// name that isn't in the configured table all yield "".
func (c *llmCategoryClassifier) normalizeChoice(raw string) string {
	if c == nil || len(c.clNames) == 0 {
		return ""
	}
	candidates := []string{raw}
	// Trim common verbosity wrappers: "category: foo", "the answer is X".
	if idx := strings.LastIndex(raw, ":"); idx >= 0 && idx < len(raw)-1 {
		candidates = append(candidates, strings.TrimSpace(raw[idx+1:]))
	}
	if first := strings.SplitN(raw, "\n", 2)[0]; first != raw {
		candidates = append(candidates, strings.TrimSpace(first))
	}
	// Try each candidate against the configured names.
	for _, cand := range candidates {
		key := strings.ToLower(strings.TrimSpace(cand))
		// Strip surrounding quotes/punctuation.
		key = strings.Trim(key, `."'` + "`" + `,;:()[]{}`)
		if key == "" {
			continue
		}
		if name, ok := c.clNames[key]; ok {
			return name
		}
	}
	// Fall back to a forgiving substring search: pick the name whose
	// lowercase form appears in the raw reply.
	rawLower := strings.ToLower(raw)
	for key, name := range c.clNames {
		if strings.Contains(rawLower, key) {
			return name
		}
	}
	return ""
}

// cacheKey returns a short hex hash for the supplied input text. We
// salt with the classifier model so a config change invalidates the
// cache.
func (c *llmCategoryClassifier) cacheKey(text string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(c.cfg.Model))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// providerInSet returns true when the supplied provider appears in the
// candidate set, comparing case-sensitively (provider keys are stable
// identifiers in this proxy).
func providerInSet(provider string, set []string) bool {
	for _, s := range set {
		if s == provider {
			return true
		}
	}
	return false
}

// classifierSystemPrompt is the constant role prompt for the LLM
// classifier. It is kept short to minimize cost.
const classifierSystemPrompt = "You are a routing classifier for an LLM proxy. " +
	"Given a user request and a list of category names with descriptions, " +
	"pick the single best-fitting category. Reply with ONLY the chosen " +
	"category name, no punctuation, no extra words."

// defaultClassifierPrompt is the user-visible template the classifier
// uses when the operator hasn't supplied their own. Two placeholders:
// {{categories}} and {{request}}.
const defaultClassifierPrompt = "Categories:\n{{categories}}\n" +
	"User request (lowercased excerpt):\n{{request}}\n\n" +
	"Pick exactly one category name from the list above."

// llmCache is a tiny TTL'd map used by the LLM classifier. A
// production deployment can swap this for any redis/memcached shim
// behind the same get/put contract.
type llmCache struct {
	ttl   time.Duration
	mu    sync.Mutex
	items map[string]llmCacheEntry
}

type llmCacheEntry struct {
	value     string
	expiresAt time.Time
}

func newLLMCache(ttl time.Duration) *llmCache {
	return &llmCache{
		ttl:   ttl,
		items: map[string]llmCacheEntry{},
	}
}

func (c *llmCache) get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiresAt) {
		delete(c.items, key)
		return "", false
	}
	return e.value, true
}

func (c *llmCache) put(key, value string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = llmCacheEntry{value: value, expiresAt: time.Now().Add(c.ttl)}
	// Cheap GC: when the map grows past ~1k entries, drop expired ones.
	if len(c.items) > 1024 {
		now := time.Now()
		for k, v := range c.items {
			if now.After(v.expiresAt) {
				delete(c.items, k)
			}
		}
	}
}

