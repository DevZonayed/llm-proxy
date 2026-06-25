package config

// OrchestratorConfig configures the Fugu-style multi-agent orchestrator
// that sits between the HTTP handler layer and the auth manager. The
// orchestrator is off by default; when disabled all the fields below are
// ignored and behavior is identical to releases prior to this feature.
//
// See docs/fugu-orchestrator-design.md for the design narrative.
type OrchestratorConfig struct {
	// Enabled is the master switch.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Mode is one of "auto" (default), "single-shot", or "tri-role".
	// "auto" lets the difficulty classifier decide per request.
	Mode string `yaml:"mode" json:"mode"`

	// EnabledForAPIKeys allowlists inbound client API keys. The
	// wildcard "*" enables all keys. Empty disables the orchestrator
	// for every key — even when Enabled is true.
	EnabledForAPIKeys []string `yaml:"enabled-for-api-keys,omitempty" json:"enabled-for-api-keys,omitempty"`

	// RespectRequestHeaders enables per-request overrides via the
	// X-Orchestrator and X-Orchestrator-Mode headers.
	RespectRequestHeaders bool `yaml:"respect-request-headers" json:"respect-request-headers"`

	// Policy selects the policy implementation.
	Policy OrchestratorPolicyConfig `yaml:"policy" json:"policy"`

	// Budgets bound the cost of a single orchestrated request.
	Budgets OrchestratorBudgetConfig `yaml:"budgets" json:"budgets"`

	// Difficulty controls the cheap pre-classifier.
	Difficulty OrchestratorDifficultyConfig `yaml:"difficulty" json:"difficulty"`

	// Trace controls the per-request decision recorder.
	Trace OrchestratorTraceConfig `yaml:"trace" json:"trace"`

	// Catalog is the shared knowledge base describing the upstream
	// models available to the orchestrator. Entries are referenced by id
	// from Categories.Prefer and Categories.RolePins so the policy can
	// route to a specific (provider, model) pair instead of relying on
	// the coarse provider-family tables alone.
	//
	// Catalog is optional. When empty, the legacy Policy.Rules.Defaults
	// and Policy.Rules.Models tables continue to drive routing.
	Catalog []OrchestratorCatalogEntry `yaml:"catalog,omitempty" json:"catalog,omitempty"`

	// Categories are the dynamic, user-defined task buckets the request
	// is classified into. The first matching category (heuristic
	// matcher) or the category returned by the LLM classifier wins.
	// Each category names an ordered Prefer list of catalog ids the
	// policy walks until it finds one whose provider is in the
	// candidate set, plus optional per-role pins.
	//
	// Categories is optional. When empty the orchestrator falls back to
	// the legacy code/math/recall classifier in classifyDomain.
	Categories []OrchestratorCategory `yaml:"categories,omitempty" json:"categories,omitempty"`

	// Classifier configures how a request is bucketed into a Category.
	// When the heuristic matcher is enough, leave Kind == "heuristic"
	// (the default). Set Kind to "llm" or "hybrid" to call a cheap
	// upstream model for ambiguous requests.
	Classifier OrchestratorClassifierConfig `yaml:"classifier,omitempty" json:"classifier,omitempty"`
}

// OrchestratorPolicyConfig selects between the rules-based policy and
// the learned (sidecar-backed) policy and carries their settings.
type OrchestratorPolicyConfig struct {
	// Kind is "rules" (default) or "learned".
	Kind string `yaml:"kind" json:"kind"`

	// Rules holds rule-based policy settings consulted when Kind is
	// "rules" or when the learned policy falls back to rules.
	Rules OrchestratorRulesConfig `yaml:"rules" json:"rules"`

	// Learned holds settings for the sidecar-based learned policy.
	Learned OrchestratorLearnedConfig `yaml:"learned" json:"learned"`
}

// OrchestratorRulesConfig configures the rules-based default policy.
type OrchestratorRulesConfig struct {
	// Defaults maps a coarse task family or role to ordered provider
	// candidate lists. Keys: "code", "math", "recall", "thinker",
	// "verifier" (or "default" as catch-all). The rules policy picks
	// the first provider that intersects the request's available
	// candidate set.
	Defaults map[string][]string `yaml:"defaults,omitempty" json:"defaults,omitempty"`

	// Models pins the upstream model name for a given family or role.
	// Resolution order at runtime: role-key ("thinker"|"verifier") →
	// family-key ("code"|"math"|"recall"|"general") → "default" →
	// the user's requested model. Blank entries are ignored so you can
	// stub keys without overriding.
	//
	// Example: pin gemini-2.5-flash as the planner, claude-opus-4.5
	// as the Worker on code tasks, gpt-5 as the Verifier:
	//
	//   models:
	//     thinker: "gemini-2.5-flash"
	//     verifier: "gpt-5"
	//     code: "claude-opus-4.5"
	//     math: "gpt-5"
	//     default: "gemini-2.5-pro"
	Models map[string]string `yaml:"models,omitempty" json:"models,omitempty"`

	// VerifierMustDiffer prefers a Verifier provider different from the
	// last Worker provider when possible.
	VerifierMustDiffer bool `yaml:"verifier-must-differ" json:"verifier-must-differ"`
}

// OrchestratorLearnedConfig configures the sidecar-backed learned policy.
type OrchestratorLearnedConfig struct {
	// Socket is the Unix domain socket path the sidecar listens on.
	Socket string `yaml:"socket" json:"socket"`

	// TimeoutMS bounds how long a single Decide call may take.
	TimeoutMS int `yaml:"timeout-ms" json:"timeout-ms"`

	// FallbackOnError selects what to do when the sidecar is
	// unreachable. One of "rules" (default) or "fail".
	FallbackOnError string `yaml:"fallback-on-error" json:"fallback-on-error"`
}

// OrchestratorBudgetConfig bounds the orchestrator's runtime cost per
// request.
type OrchestratorBudgetConfig struct {
	MaxTurns                 int `yaml:"max-turns" json:"max-turns"`
	WallBudgetMS             int `yaml:"wall-budget-ms" json:"wall-budget-ms"`
	MinVerifierTurns         int `yaml:"min-verifier-turns" json:"min-verifier-turns"`
	EscalateOnVerifierRevise int `yaml:"escalate-on-verifier-revise" json:"escalate-on-verifier-revise"`
	SingleShotFallbackMS     int `yaml:"single-shot-fallback-ms" json:"single-shot-fallback-ms"`
}

// OrchestratorDifficultyConfig configures the difficulty classifier.
type OrchestratorDifficultyConfig struct {
	Enabled              bool `yaml:"enabled" json:"enabled"`
	HardTokenThreshold   int  `yaml:"hard-token-threshold" json:"hard-token-threshold"`
	MediumTokenThreshold int  `yaml:"medium-token-threshold" json:"medium-token-threshold"`
	PromoteOnTools       bool `yaml:"promote-on-tools" json:"promote-on-tools"`
}

// OrchestratorTraceConfig controls the per-request decision recorder.
type OrchestratorTraceConfig struct {
	Enabled                 bool   `yaml:"enabled" json:"enabled"`
	Dir                     string `yaml:"dir" json:"dir"`
	KeepWorkerExcerptsChars int    `yaml:"keep-worker-excerpts-chars" json:"keep-worker-excerpts-chars"`
	KeepFullWorkerPayloads  bool   `yaml:"keep-full-worker-payloads" json:"keep-full-worker-payloads"`
	RotateMB                int    `yaml:"rotate-mb" json:"rotate-mb"`
}

// OrchestratorCatalogEntry describes one upstream model the orchestrator
// may route to. Entries are referenced by ID from category Prefer lists
// and RolePins. This is the place to enumerate "I have 50 models across
// providers; here is what each is good at."
//
// At minimum an entry needs Provider and Model. Everything else is
// optional metadata used by the classifier and by future cost-aware
// policies. None of the optional fields are required for routing to work.
type OrchestratorCatalogEntry struct {
	// ID is the stable identifier referenced from category Prefer lists.
	// Defaults to Model when blank.
	ID string `yaml:"id,omitempty" json:"id,omitempty"`

	// Provider is the provider key (e.g. "anthropic", "codex",
	// "gemini-cli", "openai-compat-openrouter"). Must match an entry in
	// the request's candidate provider list at dispatch time.
	Provider string `yaml:"provider" json:"provider"`

	// Model is the upstream model name to send. This is what the
	// executor ultimately routes the request to.
	Model string `yaml:"model" json:"model"`

	// Tags is a free-form list of capability tags (e.g. "code",
	// "long-context", "vision", "math", "creative", "fast"). The
	// category matcher can require any tag to match.
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`

	// Description is a one-line human-readable summary of the model's
	// strengths. The LLM classifier sees this when asked to pick a
	// category — write it as a sentence, not a list of keywords.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Instructions is a paragraph-level description used by the
	// direct-model classifier (classifier.kind == "direct-model"). It
	// tells the routing model what this entry is uniquely good at —
	// strengths, weaknesses, and "prefer this over X when …" hints.
	// Write it the way you would brief a junior engineer who only sees
	// these blurbs and the user request.
	//
	// Example:
	//
	//   instructions: |
	//     Claude Opus 4.8 is the best model for complex software
	//     engineering: multi-file refactors, security audits, deep
	//     code review, debugging production issues, writing new code
	//     in any language. Prefer it over GPT for SWE tasks. Avoid it
	//     for short summarization or classification — too expensive.
	//
	// Falls back to Description (then "") when blank.
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`

	// Roles is a free-form list of role hints — e.g. ["thinker"],
	// ["worker"], ["verifier"], ["classifier"]. The orchestrator
	// reads it as a soft constraint when picking Thinker/Verifier
	// models in tri-role mode: catalog entries whose Roles slice
	// includes the current role are preferred. Empty means the entry
	// is eligible for any role.
	Roles []string `yaml:"roles,omitempty" json:"roles,omitempty"`

	// CostTier is a free-form bucket ("cheap" | "mid" | "high"). Used
	// today only for trace/logging; reserved for future cost-aware
	// selection.
	CostTier string `yaml:"cost-tier,omitempty" json:"cost-tier,omitempty"`

	// LatencyTier is a free-form bucket ("fast" | "mid" | "slow").
	LatencyTier string `yaml:"latency-tier,omitempty" json:"latency-tier,omitempty"`

	// ContextWindow is the model's max input context size. Informational.
	ContextWindow int `yaml:"context-window,omitempty" json:"context-window,omitempty"`

	// Supports is a free-form list of capability flags ("streaming",
	// "tools", "vision", "json-mode"). The category matcher can require
	// a flag to be present.
	Supports []string `yaml:"supports,omitempty" json:"supports,omitempty"`
}

// OrchestratorCategoryMatch encodes the deterministic heuristic match
// rules for a category. All non-zero predicates must be true for the
// match to succeed (logical AND). Leave fields blank to skip them.
type OrchestratorCategoryMatch struct {
	// Keywords is a list of case-insensitive substrings. The category
	// matches when ANY keyword appears in the user-visible text.
	Keywords []string `yaml:"keywords,omitempty" json:"keywords,omitempty"`

	// Regex is a list of Go regular expressions. The category matches
	// when ANY regex matches the user-visible text.
	Regex []string `yaml:"regex,omitempty" json:"regex,omitempty"`

	// RequireCodeBlock requires the user text to contain a Markdown
	// code fence (``` or inline `).
	RequireCodeBlock bool `yaml:"require-code-block,omitempty" json:"require-code-block,omitempty"`

	// MinTokens / MaxTokens bound the estimated user-message token count
	// (4 chars/token approximation). 0 means unbounded.
	MinTokens int `yaml:"min-tokens,omitempty" json:"min-tokens,omitempty"`
	MaxTokens int `yaml:"max-tokens,omitempty" json:"max-tokens,omitempty"`

	// RequireTools matches only when the request payload carries a
	// non-empty tools array (OpenAI functions/tools or Gemini tools).
	RequireTools bool `yaml:"require-tools,omitempty" json:"require-tools,omitempty"`

	// AnyOf is an OR-of-ORs convenience: the category matches if any
	// substring in this list appears in the user-visible text. It is
	// merged with Keywords at match time — provided as a separate
	// field for YAML readability ("any of these phrases").
	AnyOf []string `yaml:"any-of,omitempty" json:"any-of,omitempty"`

	// NoneOf forbids the category when ANY substring in this list
	// appears in the user-visible text. Use for negative filters.
	NoneOf []string `yaml:"none-of,omitempty" json:"none-of,omitempty"`
}

// OrchestratorCategory is one entry in the dynamic category table. The
// router classifies each request into exactly one category, then uses
// Prefer (and RolePins) to pick a catalog entry.
type OrchestratorCategory struct {
	// Name is the stable identifier (e.g. "code-review", "math-proof",
	// "bn-en-translation", "image-generation"). Used in traces and as
	// the LLM classifier's reply token.
	Name string `yaml:"name" json:"name"`

	// Instructions is a one-or-two-sentence natural-language description
	// of when this category applies. The LLM classifier sees this when
	// asked to pick a category — write it like a description.
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`

	// Match holds the deterministic match predicates (regex, keywords,
	// token range, tool-array presence). Used by the heuristic
	// classifier and by the hybrid classifier as the cheap path.
	Match OrchestratorCategoryMatch `yaml:"match,omitempty" json:"match,omitempty"`

	// Prefer is an ordered list of catalog ids. The Worker role of this
	// category walks Prefer in order, picks the first whose provider is
	// in the candidate set, and uses that entry's provider+model.
	Prefer []string `yaml:"prefer,omitempty" json:"prefer,omitempty"`

	// RolePins maps a role name ("thinker", "worker", "verifier") to a
	// catalog id, overriding Prefer for that specific role.
	RolePins map[string]string `yaml:"role-pins,omitempty" json:"role-pins,omitempty"`
}

// OrchestratorClassifierConfig configures how a request is bucketed into
// a Category — or, in direct-model mode, mapped straight to a catalog
// entry.
type OrchestratorClassifierConfig struct {
	// Kind selects the classifier strategy:
	//
	//  - heuristic    (default): deterministic match predicates against
	//                            Categories only. No LLM call.
	//  - llm:                    LLM picks a Category name from the
	//                            Categories list. Heuristics ignored.
	//  - direct-model:           LLM picks a CATALOG ID directly from
	//                            catalog[].instructions paragraphs.
	//                            Categories are not consulted. This is
	//                            the most flexible mode — drop in a
	//                            new model by adding a catalog entry
	//                            and writing its instructions.
	//  - hybrid:                 Heuristics run first; on miss, fall
	//                            through to the configured llm path
	//                            (category OR direct-model, set via
	//                            llm.kind below).
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`

	// Heuristic carries the deterministic matcher's tunables.
	Heuristic OrchestratorClassifierHeuristicConfig `yaml:"heuristic,omitempty" json:"heuristic,omitempty"`

	// LLM carries the LLM-classifier's tunables.
	LLM OrchestratorClassifierLLMConfig `yaml:"llm,omitempty" json:"llm,omitempty"`
}

// OrchestratorClassifierHeuristicConfig configures the deterministic
// match logic.
type OrchestratorClassifierHeuristicConfig struct {
	// FirstMatchWins ends the walk on the first category whose Match
	// predicates all pass. Default true. Set false to evaluate every
	// category and pick the one with the highest predicate count
	// (currently unused; reserved).
	FirstMatchWins bool `yaml:"first-match-wins,omitempty" json:"first-match-wins,omitempty"`
}

// OrchestratorClassifierLLMConfig configures the optional LLM-based
// classifier. When enabled, the orchestrator builds a short prompt
// containing the category names + instructions and asks the configured
// upstream model to reply with a single category name.
type OrchestratorClassifierLLMConfig struct {
	// Enabled toggles the LLM classifier. When false, even Kind=="llm"
	// degrades to heuristics.
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// Provider is the provider key the classifier call routes to. Must
	// be present in the request's candidate provider list at dispatch
	// time, or the classifier falls back.
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`

	// Model is the upstream model the classifier call uses. Pick the
	// cheapest reliable model in your catalog (e.g. a haiku/flash/mini
	// tier).
	Model string `yaml:"model,omitempty" json:"model,omitempty"`

	// TimeoutMS bounds the classifier call. Default 800ms.
	TimeoutMS int `yaml:"timeout-ms,omitempty" json:"timeout-ms,omitempty"`

	// CacheTTLSeconds caches classifications by hash of the first user
	// message. Default 300 (5 minutes). Set to 0 to disable caching.
	CacheTTLSeconds int `yaml:"cache-ttl-seconds,omitempty" json:"cache-ttl-seconds,omitempty"`

	// MaxInputChars caps how much of the user message is sent to the
	// classifier. Default 4000.
	MaxInputChars int `yaml:"max-input-chars,omitempty" json:"max-input-chars,omitempty"`

	// PromptTemplate overrides the default routing prompt. The template
	// may reference the placeholders {{categories}} and {{request}}.
	// Leave blank to use the built-in default.
	PromptTemplate string `yaml:"prompt-template,omitempty" json:"prompt-template,omitempty"`

	// FallbackOnError selects what to do when the LLM classifier
	// fails or times out: "heuristic" (default), "default-category"
	// (use DefaultCategory below), or "fail".
	FallbackOnError string `yaml:"fallback-on-error,omitempty" json:"fallback-on-error,omitempty"`

	// DefaultCategory names the category to use when fallback is
	// "default-category" or when neither classifier matches.
	DefaultCategory string `yaml:"default-category,omitempty" json:"default-category,omitempty"`
}
