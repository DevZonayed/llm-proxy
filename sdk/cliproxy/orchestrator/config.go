package orchestrator

import (
	"errors"
	"strings"
	"time"
)

// Config carries the orchestrator's runtime configuration. It is constructed
// from internalconfig.OrchestratorConfig but kept in its own form to keep
// this package free of dependencies on internal/config (avoids an import
// cycle through the SDK's re-exported config types).
type Config struct {
	// Enabled is the master switch. When false, all entry points become
	// no-ops and the orchestrator never alters dispatch.
	Enabled bool

	// Mode selects the default execution mode. One of "single-shot",
	// "tri-role", or "auto". When "auto", the difficulty classifier
	// decides per request.
	Mode string

	// EnabledForAPIKeys is the allowlist of inbound client API keys that
	// may use the orchestrator. The wildcard "*" enables all keys.
	// An empty slice disables the orchestrator for every key.
	EnabledForAPIKeys []string

	// RespectRequestHeaders enables per-request overrides via the
	// X-Orchestrator and X-Orchestrator-Mode headers.
	RespectRequestHeaders bool

	// Policy selects the policy implementation.
	Policy PolicyConfig

	// Budgets bound the cost of a single orchestrated request.
	Budgets BudgetConfig

	// Difficulty controls the cheap pre-classifier.
	Difficulty DifficultyConfig

	// Trace controls the per-request decision recorder.
	Trace TraceConfig

	// Catalog is the shared knowledge base of upstream models. The rules
	// policy consults the catalog when a request matches a category, so
	// the orchestrator can route to a specific (provider, model) pair
	// rather than relying on coarse provider-family tables. Optional.
	Catalog []CatalogEntry

	// Categories describes the dynamic task buckets the request is
	// classified into. The first matching category (heuristic) or the
	// category returned by the LLM classifier wins. Optional.
	Categories []Category

	// Classifier configures how a request is bucketed into a category.
	Classifier ClassifierConfig
}

// CatalogEntry mirrors internalconfig.OrchestratorCatalogEntry in the
// orchestrator package's own form. See the YAML-tagged struct for field
// documentation.
type CatalogEntry struct {
	ID            string
	Provider      string
	Model         string
	Tags          []string
	Description   string
	Instructions  string
	Roles         []string
	CostTier      string
	LatencyTier   string
	ContextWindow int
	Supports      []string
}

// EffectiveInstructions returns the paragraph-level instructions for
// this catalog entry, falling back to Description when Instructions is
// blank. Used by the direct-model classifier when rendering the
// routing prompt.
func (e CatalogEntry) EffectiveInstructions() string {
	if s := strings.TrimSpace(e.Instructions); s != "" {
		return s
	}
	return strings.TrimSpace(e.Description)
}

// HasRole reports whether the supplied role hint appears in the
// entry's Roles slice (case-insensitive). Empty Roles is treated as
// "any role" — the entry is eligible for every role.
func (e CatalogEntry) HasRole(role string) bool {
	if len(e.Roles) == 0 {
		return true
	}
	want := strings.ToLower(strings.TrimSpace(role))
	for _, r := range e.Roles {
		if strings.EqualFold(strings.TrimSpace(r), want) {
			return true
		}
	}
	return false
}

// EffectiveID returns the entry's ID, falling back to Model when ID is
// blank. Catalog IDs are referenced from category Prefer lists.
func (e CatalogEntry) EffectiveID() string {
	if strings.TrimSpace(e.ID) != "" {
		return e.ID
	}
	return e.Model
}

// HasTag reports whether the supplied tag is present in the entry's
// Tags slice. Comparison is case-insensitive.
func (e CatalogEntry) HasTag(tag string) bool {
	want := strings.ToLower(strings.TrimSpace(tag))
	for _, t := range e.Tags {
		if strings.EqualFold(strings.TrimSpace(t), want) {
			return true
		}
	}
	return false
}

// CategoryMatch is the deterministic match predicate for a single
// category. All non-zero predicates must hold for the category to match
// (logical AND across predicates).
type CategoryMatch struct {
	Keywords         []string
	Regex            []string
	RequireCodeBlock bool
	MinTokens        int
	MaxTokens        int
	RequireTools     bool
	AnyOf            []string
	NoneOf           []string
}

// Category mirrors internalconfig.OrchestratorCategory in the
// orchestrator package's own form.
type Category struct {
	Name         string
	Instructions string
	Match        CategoryMatch
	Prefer       []string
	RolePins     map[string]string
}

// PinFor returns the catalog id pinned for the supplied role name, or
// the empty string when no pin is configured. Role lookup is
// case-insensitive.
func (c Category) PinFor(role string) string {
	if len(c.RolePins) == 0 {
		return ""
	}
	want := strings.ToLower(strings.TrimSpace(role))
	for k, v := range c.RolePins {
		if strings.EqualFold(strings.TrimSpace(k), want) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ClassifierConfig configures how a request is classified into one of
// the configured Categories.
type ClassifierConfig struct {
	// Kind is "heuristic" (default), "llm", or "hybrid".
	Kind string

	Heuristic ClassifierHeuristicConfig
	LLM       ClassifierLLMConfig
}

// ClassifierHeuristicConfig tunes the deterministic matcher.
type ClassifierHeuristicConfig struct {
	FirstMatchWins bool
}

// ClassifierLLMConfig tunes the optional LLM-based classifier.
type ClassifierLLMConfig struct {
	Enabled         bool
	Provider        string
	Model           string
	Timeout         time.Duration
	CacheTTL        time.Duration
	MaxInputChars   int
	PromptTemplate  string
	FallbackOnError string
	DefaultCategory string
}

// PolicyConfig selects between the rules-based policy and the learned
// (sidecar-backed) policy and carries their respective settings.
type PolicyConfig struct {
	// Kind is "rules" (default) or "learned".
	Kind string

	// Rules holds rule-based policy settings consulted when Kind == "rules"
	// or when the learned policy falls back to rules.
	Rules RulesConfig

	// Learned holds settings for the sidecar-based learned policy.
	Learned LearnedConfig
}

// RulesConfig configures the rules-based default policy.
type RulesConfig struct {
	// Defaults maps a coarse task family or role to ordered provider
	// candidate lists. Keys may be a family ("code", "math", "recall")
	// or a role ("thinker", "verifier"). The rules policy picks the
	// first provider that intersects the request's available candidate
	// set. Missing keys fall back to the candidate set's first entry.
	Defaults map[string][]string

	// Models pins the upstream model name used for a given family or
	// role. Keys mirror Defaults ("code", "math", "recall", "thinker",
	// "verifier", or a request-domain key). When a matching entry
	// exists, the rules policy emits it as Action.Model; otherwise the
	// orchestrator falls back to the user's requested model name. This
	// is the seam for "use gemini-2.5-flash as the Thinker, claude-opus
	// as the Worker on code tasks, gpt-5 as the Verifier".
	//
	// A pinned model must be servable by at least one provider in the
	// matching Defaults entry; otherwise the dispatch layer will error
	// when it tries to send the request.
	Models map[string]string

	// VerifierMustDiffer asks the policy to choose a Verifier provider
	// different from the last Worker provider when possible.
	VerifierMustDiffer bool
}

// LearnedConfig configures the sidecar-backed learned policy.
type LearnedConfig struct {
	// Socket is the Unix domain socket path the sidecar listens on.
	Socket string

	// Timeout bounds how long a single Decide call may take.
	Timeout time.Duration

	// FallbackOnError selects what to do when the sidecar is unreachable
	// or returns an error. One of "rules" (default) or "fail".
	FallbackOnError string
}

// BudgetConfig bounds the orchestrator's runtime cost per request.
type BudgetConfig struct {
	// MaxTurns is the upper bound on the number of role turns (Thinker,
	// Worker, Verifier) executed in tri-role mode. Includes the final
	// Worker turn.
	MaxTurns int

	// WallBudget is the hard wall-clock kill switch for the entire
	// orchestrated request.
	WallBudget time.Duration

	// MinVerifierTurns requires at least this many Verifier evaluations
	// before halting on Worker output. Default 1.
	MinVerifierTurns int

	// EscalateOnVerifierRevise is the number of consecutive REVISE
	// verdicts that escalates the next Worker to a stronger family
	// (currently a no-op stub for future use).
	EscalateOnVerifierRevise int

	// SingleShotFallback bounds how long the orchestrator may sit
	// computing Decide() or running difficulty classification before it
	// falls back to single-shot dispatch.
	SingleShotFallback time.Duration
}

// DifficultyConfig configures the difficulty classifier.
type DifficultyConfig struct {
	// Enabled toggles the classifier. When false, every request is
	// treated as Medium difficulty.
	Enabled bool

	// HardTokenThreshold is the rough token count above which a request
	// is considered hard.
	HardTokenThreshold int

	// MediumTokenThreshold is the rough token count above which a request
	// is considered medium.
	MediumTokenThreshold int

	// PromoteOnTools promotes a request's difficulty bucket by one when
	// the payload carries a non-empty tools array.
	PromoteOnTools bool
}

// TraceConfig controls the per-request decision recorder.
type TraceConfig struct {
	// Enabled toggles trace persistence. When false, no files are written.
	Enabled bool

	// Dir is the destination directory for rolling trace files. The
	// directory is created on first write.
	Dir string

	// KeepWorkerExcerptsChars caps how much of each Worker turn output is
	// recorded in the trace.
	KeepWorkerExcerptsChars int

	// KeepFullWorkerPayloads bypasses the excerpt cap and stores the
	// entire Worker output verbatim. Use with care.
	KeepFullWorkerPayloads bool

	// RotateMB is the size at which trace files roll over.
	RotateMB int
}

// Default returns a Config populated with the documented v0 defaults. The
// returned Config has Enabled=false so existing behavior is unaffected.
func Default() Config {
	return Config{
		Enabled:               false,
		Mode:                  "auto",
		EnabledForAPIKeys:     nil,
		RespectRequestHeaders: true,
		Policy: PolicyConfig{
			Kind: "rules",
			Rules: RulesConfig{
				Defaults:           nil,
				VerifierMustDiffer: true,
			},
			Learned: LearnedConfig{
				Timeout:         50 * time.Millisecond,
				FallbackOnError: "rules",
			},
		},
		Budgets: BudgetConfig{
			MaxTurns:                 4,
			WallBudget:               60 * time.Second,
			MinVerifierTurns:         1,
			EscalateOnVerifierRevise: 1,
			SingleShotFallback:       10 * time.Second,
		},
		Difficulty: DifficultyConfig{
			Enabled:              true,
			HardTokenThreshold:   2500,
			MediumTokenThreshold: 500,
			PromoteOnTools:       false,
		},
		Trace: TraceConfig{
			Enabled:                 true,
			Dir:                     "",
			KeepWorkerExcerptsChars: 256,
			KeepFullWorkerPayloads:  false,
			RotateMB:                64,
		},
		Catalog:    nil,
		Categories: nil,
		Classifier: ClassifierConfig{
			Kind: "heuristic",
			Heuristic: ClassifierHeuristicConfig{
				FirstMatchWins: true,
			},
			LLM: ClassifierLLMConfig{
				Enabled:         false,
				Timeout:         800 * time.Millisecond,
				CacheTTL:        5 * time.Minute,
				MaxInputChars:   4000,
				FallbackOnError: "heuristic",
			},
		},
	}
}

// Validate returns an error if the configuration is internally inconsistent.
// A disabled orchestrator is always valid.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	switch mode {
	case "", "auto", "single-shot", "tri-role":
		// OK
	default:
		return errors.New("orchestrator: mode must be one of 'auto', 'single-shot', 'tri-role'")
	}
	kind := strings.ToLower(strings.TrimSpace(c.Policy.Kind))
	switch kind {
	case "", "rules", "learned":
		// OK
	default:
		return errors.New("orchestrator: policy.kind must be 'rules' or 'learned'")
	}
	if c.Budgets.MaxTurns < 1 {
		return errors.New("orchestrator: budgets.max-turns must be >= 1")
	}
	if c.Budgets.WallBudget < 0 {
		return errors.New("orchestrator: budgets.wall-budget must be >= 0")
	}
	if c.Difficulty.HardTokenThreshold < c.Difficulty.MediumTokenThreshold {
		return errors.New("orchestrator: difficulty.hard-token-threshold must be >= medium-token-threshold")
	}
	if err := c.validateCatalogCategories(); err != nil {
		return err
	}
	return nil
}

// validateCatalogCategories checks the new catalog/categories tables for
// internal consistency. The orchestrator can still run with neither
// configured (legacy provider-family routing takes over); when either is
// present, both must agree.
func (c Config) validateCatalogCategories() error {
	classifierKindFirst := strings.ToLower(strings.TrimSpace(c.Classifier.Kind))
	// `direct-model` MUST have a non-empty catalog regardless of whether
	// categories are configured — there is nothing for the classifier to
	// pick from otherwise.
	if classifierKindFirst == "direct-model" && len(c.Catalog) == 0 {
		return errors.New("orchestrator: classifier.kind 'direct-model' requires a non-empty catalog")
	}
	if len(c.Catalog) == 0 && len(c.Categories) == 0 {
		return nil
	}
	// Build the catalog index by effective id.
	idx := make(map[string]struct{}, len(c.Catalog))
	for i, e := range c.Catalog {
		id := e.EffectiveID()
		if strings.TrimSpace(id) == "" {
			return errors.New("orchestrator: catalog entry " + itoa(i) + " is missing id and model")
		}
		if strings.TrimSpace(e.Provider) == "" {
			return errors.New("orchestrator: catalog entry " + id + " is missing provider")
		}
		if _, dup := idx[id]; dup {
			return errors.New("orchestrator: catalog entry id is not unique: " + id)
		}
		idx[id] = struct{}{}
	}
	// Validate each category references catalog ids that exist.
	classifierKind := strings.ToLower(strings.TrimSpace(c.Classifier.Kind))
	for _, cat := range c.Categories {
		if strings.TrimSpace(cat.Name) == "" {
			return errors.New("orchestrator: category is missing name")
		}
		for _, id := range cat.Prefer {
			if id == "" {
				continue
			}
			if _, ok := idx[id]; !ok {
				return errors.New("orchestrator: category " + cat.Name + " prefers unknown catalog id " + id)
			}
		}
		for role, id := range cat.RolePins {
			if id == "" {
				continue
			}
			if _, ok := idx[id]; !ok {
				return errors.New("orchestrator: category " + cat.Name + " role-pin " + role + " references unknown catalog id " + id)
			}
		}
	}
	switch classifierKind {
	case "", "heuristic", "llm", "direct-model", "hybrid":
		// OK
	default:
		return errors.New("orchestrator: classifier.kind must be 'heuristic', 'llm', 'direct-model', or 'hybrid'")
	}
	if classifierKind == "llm" || classifierKind == "direct-model" || classifierKind == "hybrid" {
		llm := c.Classifier.LLM
		if llm.Enabled && strings.TrimSpace(llm.Model) == "" {
			return errors.New("orchestrator: classifier.llm.model is required when llm classifier is enabled")
		}
	}
	if classifierKind == "direct-model" && len(c.Catalog) == 0 {
		return errors.New("orchestrator: classifier.kind 'direct-model' requires a non-empty catalog")
	}
	if def := strings.TrimSpace(c.Classifier.LLM.DefaultCategory); def != "" {
		found := false
		for _, cat := range c.Categories {
			if strings.EqualFold(strings.TrimSpace(cat.Name), def) {
				found = true
				break
			}
		}
		if !found {
			return errors.New("orchestrator: classifier.llm.default-category " + def + " is not a configured category")
		}
	}
	return nil
}

// itoa is a tiny local helper so we don't pull strconv just for two
// validation messages.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// modeNormalized returns the lowercase canonical mode for runtime dispatch.
func (c Config) modeNormalized() string {
	m := strings.ToLower(strings.TrimSpace(c.Mode))
	if m == "" {
		return "auto"
	}
	return m
}

// apiKeyAllowed reports whether the supplied client API key is whitelisted.
// A wildcard entry "*" enables every key. Comparison is case-sensitive — API
// keys are opaque tokens.
func (c Config) apiKeyAllowed(apiKey string) bool {
	if len(c.EnabledForAPIKeys) == 0 {
		return false
	}
	for _, allowed := range c.EnabledForAPIKeys {
		if allowed == "*" {
			return true
		}
		if allowed == apiKey {
			return true
		}
	}
	return false
}
