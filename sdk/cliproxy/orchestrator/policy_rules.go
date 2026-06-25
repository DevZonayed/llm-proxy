package orchestrator

import (
	"context"
	"regexp"
	"strings"
)

// RulesPolicy implements Policy with hand-written heuristics derived from
// the request payload. It is the v0 default and serves both as the
// shipping policy and as the fallback for the learned variant.
//
// The policy is deterministic and cheap: a category lookup against the
// optional Catalog + Categories tables, falling back to a few regex
// checks against the raw request payload bytes and a lookup into the
// configured per-family provider ordering when no category matches.
type RulesPolicy struct {
	cfg          RulesConfig
	catalog      *catalogIndex // nil-safe
	catalogOrder []CatalogEntry // declared order, for deterministic Roles iteration
	categories   []Category
}

// NewRulesPolicy constructs a RulesPolicy from configuration. Pass nil
// for catalog and categories to keep the legacy provider-family
// routing — every existing config still works.
func NewRulesPolicy(cfg RulesConfig, opts ...RulesPolicyOption) *RulesPolicy {
	p := &RulesPolicy{
		cfg:     cfg,
		catalog: newCatalogIndex(nil),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// RulesPolicyOption customizes a RulesPolicy at construction time. We
// use functional options instead of widening the constructor signature
// so existing callers and tests (NewRulesPolicy(cfg)) keep compiling.
type RulesPolicyOption func(*RulesPolicy)

// WithCatalog wires a model catalog into the rules policy. The catalog
// is consulted when a request matches a Category (see WithCategories),
// when the direct-model classifier picks a specific id (state.
// ModelCatalogID), or to satisfy Thinker/Verifier roles via per-entry
// Roles hints. Pass nil for no catalog.
func WithCatalog(catalog []CatalogEntry) RulesPolicyOption {
	return func(p *RulesPolicy) {
		p.catalog = newCatalogIndex(catalog)
		p.catalogOrder = append([]CatalogEntry(nil), catalog...)
	}
}

// WithCategories wires the dynamic category table into the rules
// policy. When a request's CategoryHint matches one of these,
// preference resolution uses the category's Prefer / RolePins lists
// instead of the legacy cfg.Defaults table.
func WithCategories(categories []Category) RulesPolicyOption {
	return func(p *RulesPolicy) {
		p.categories = append([]Category(nil), categories...)
	}
}

// Decide implements Policy.
func (p *RulesPolicy) Decide(ctx context.Context, state TurnState) (Action, error) {
	if len(state.Providers) == 0 {
		return Action{}, errEmptyProviderSet
	}

	role := p.roleForTurn(state)

	// Direct-model path: highest-priority signal. For the Worker
	// role, when the orchestrator's direct-model classifier picked a
	// specific catalog entry, route there. Thinker / Verifier turns
	// don't get a direct-model pick (the classifier prompts only the
	// Worker model); they fall through to Roles-tagged entries below.
	if role == RoleWorker && state.ModelCatalogID != "" {
		if entry, ok := p.catalog.Get(state.ModelCatalogID); ok {
			if providerInSet(entry.Provider, state.Providers) {
				return Action{
					Provider: entry.Provider,
					Model:    entry.Model,
					Role:     role,
					Halt:     false,
				}, nil
			}
		}
	}

	// Roles-tagged catalog path: for Thinker / Verifier turns in
	// direct-model mode (or any mode where the catalog declares which
	// entries are good as planners/verifiers), pick the first catalog
	// entry whose Roles slice includes the current role and whose
	// provider is in the candidate set.
	if role != RoleWorker {
		if entry, ok := p.pickCatalogByRole(role, state); ok {
			return Action{
				Provider: entry.Provider,
				Model:    entry.Model,
				Role:     role,
				Halt:     false,
			}, nil
		}
	}

	// Category-aware path: if the orchestrator pre-classified this
	// request into a configured category and that category has at
	// least one usable Prefer/RolePin entry, route through the
	// catalog.
	if state.CategoryHint != "" {
		if cat, ok := p.findCategory(state.CategoryHint); ok {
			if act, ok := p.actionFromCategory(cat, role, state); ok {
				return act, nil
			}
		}
	}

	// Legacy domain-family path.
	family := classifyDomain(state)
	preferred := p.preferredProvidersFor(role, family, state)
	provider := pickFirstAvailable(preferred, state.Providers)
	if provider == "" {
		provider = state.Providers[0]
	}
	model := p.modelFor(role, family, state.UserModelHint)

	return Action{
		Provider: provider,
		Model:    model,
		Role:     role,
		Halt:     false,
	}, nil
}

// findCategory returns the configured Category struct for the supplied
// name, or zero-value + false. Lookup is case-insensitive.
func (p *RulesPolicy) findCategory(name string) (Category, bool) {
	for _, cat := range p.categories {
		if strings.EqualFold(strings.TrimSpace(cat.Name), strings.TrimSpace(name)) {
			return cat, true
		}
	}
	return Category{}, false
}

// pickCatalogByRole walks the catalog in declared order and returns
// the first entry that (a) carries the requested role hint via its
// Roles slice and (b) has its Provider in the candidate set. The role
// hint match is case-insensitive; entries with an empty Roles slice
// are treated as "any role" so a sparse catalog still produces a hit.
//
// For Verifier turns, the policy also honours VerifierMustDiffer by
// excluding the last Worker provider from the candidate set before
// walking the catalog.
func (p *RulesPolicy) pickCatalogByRole(role Role, state TurnState) (CatalogEntry, bool) {
	if p == nil || p.catalog == nil || len(p.catalog.byID) == 0 {
		return CatalogEntry{}, false
	}
	available := state.Providers
	if role == RoleVerifier && p.cfg.VerifierMustDiffer {
		if last := lastTurnByRole(state, RoleWorker); last != nil {
			pruned := excludeProvider(available, last.Provider)
			if len(pruned) > 0 {
				available = pruned
			}
		}
	}
	set := make(map[string]struct{}, len(available))
	for _, prov := range available {
		set[prov] = struct{}{}
	}
	// We rely on the catalog being iterated in some stable order. The
	// underlying map iteration is not stable, so we re-iterate the
	// per-entry slice by querying byID via the keys collected in the
	// catalogIndex. For deterministic order we fall back to the
	// original Catalog slice stored on the RulesPolicy when present.
	for _, e := range p.catalogSlice() {
		if !e.HasRole(role.String()) {
			continue
		}
		if _, ok := set[e.Provider]; ok {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// catalogSlice returns the catalog entries in their declared order so
// pickCatalogByRole iterates deterministically. The catalogIndex's
// internal map iteration is non-deterministic, so we keep a parallel
// slice on RulesPolicy when WithCatalog is used.
func (p *RulesPolicy) catalogSlice() []CatalogEntry {
	if p == nil {
		return nil
	}
	return p.catalogOrder
}

// actionFromCategory resolves a (category, role) pair to a routing
// Action. Returns ok=false when the category has no usable preference
// for this candidate provider set, letting the caller fall back to the
// legacy path.
//
// Resolution order:
//  1. Verifier turns with VerifierMustDiffer first try the category's
//     Prefer/RolePins after excluding the last Worker provider.
//  2. RolePins for the current role.
//  3. Category.Prefer walked in order.
func (p *RulesPolicy) actionFromCategory(cat Category, role Role, state TurnState) (Action, bool) {
	if p.catalog == nil {
		return Action{}, false
	}

	available := state.Providers
	if role == RoleVerifier && p.cfg.VerifierMustDiffer {
		if last := lastTurnByRole(state, RoleWorker); last != nil {
			pruned := excludeProvider(available, last.Provider)
			if len(pruned) > 0 {
				available = pruned
			}
		}
	}

	entry, ok := p.catalog.pickForRole(cat, role.String(), available)
	if !ok {
		// Worker fallback: many configs only pin RolePins for thinker /
		// verifier, leaving Worker to Prefer. If pickForRole already
		// walked Prefer we have nothing more to try here.
		return Action{}, false
	}
	return Action{
		Provider: entry.Provider,
		Model:    entry.Model,
		Role:     role,
		Halt:     false,
	}, true
}

// modelFor picks the upstream model name for an outbound role turn. It
// consults p.cfg.Models in this order:
//
//  1. The role-specific key ("thinker", "verifier"). This is the right
//     place to pin a cheap planner or a strong reviewer.
//  2. The Worker's domain-family key ("code", "math", "recall",
//     "general"). This is where you say "for code tasks always use
//     claude-opus-4.5".
//  3. The user's original request model hint.
//
// A blank entry in the map is treated as "no override" so users can
// stub keys without resetting them.
func (p *RulesPolicy) modelFor(role Role, family, userHint string) string {
	if p == nil || p.cfg.Models == nil {
		return userHint
	}
	switch role {
	case RoleThinker:
		if m := strings.TrimSpace(p.cfg.Models["thinker"]); m != "" {
			return m
		}
	case RoleVerifier:
		if m := strings.TrimSpace(p.cfg.Models["verifier"]); m != "" {
			return m
		}
	}
	// For Worker (and as fallback for other roles), use family-keyed
	// model when present.
	if m := strings.TrimSpace(p.cfg.Models[family]); m != "" {
		return m
	}
	// Generic catch-all under the "default" key.
	if m := strings.TrimSpace(p.cfg.Models["default"]); m != "" {
		return m
	}
	return userHint
}

// roleForTurn picks the role assignment for a turn based on its index and
// configured budgets. Even indices are Thinker (when room permits), odd
// indices are Worker, and the policy interleaves a Verifier turn after
// each Worker that is followed by additional budget.
func (p *RulesPolicy) roleForTurn(state TurnState) Role {
	if state.Difficulty != BucketHard {
		return RoleWorker
	}
	if state.Turn == 0 {
		return RoleThinker
	}
	if len(state.History) > 0 {
		last := state.History[len(state.History)-1]
		switch last.Role {
		case RoleThinker:
			return RoleWorker
		case RoleWorker:
			return RoleVerifier
		case RoleVerifier:
			// If the previous Verifier said ACCEPT we should never
			// have been called for another turn, so REVISE is the
			// only path here — replan with a fresh Thinker.
			if remaining := state.Budget - state.Turn; remaining >= 2 {
				return RoleThinker
			}
			return RoleWorker
		}
	}
	return RoleWorker
}

// preferredProvidersFor returns the ordered list of provider candidates
// the policy would like to use for the next turn, in decreasing order of
// preference. family is the domain classification (see classifyDomain).
func (p *RulesPolicy) preferredProvidersFor(role Role, family string, state TurnState) []string {
	switch role {
	case RoleThinker:
		// Thinker prefers a cheap, fast family. The rules config can
		// override this via a "thinker" key; otherwise we fall back to
		// the recall default which is typically the cheapest.
		if list, ok := p.cfg.Defaults["thinker"]; ok && len(list) > 0 {
			return list
		}
		if list, ok := p.cfg.Defaults["recall"]; ok && len(list) > 0 {
			return list
		}
	case RoleVerifier:
		// Verifier prefers a provider different from the last Worker
		// when configured. Fall back to the math/strict-reasoning
		// default otherwise.
		preferred := p.cfg.Defaults["verifier"]
		if len(preferred) == 0 {
			preferred = p.cfg.Defaults["math"]
		}
		if p.cfg.VerifierMustDiffer {
			lastWorker := lastTurnByRole(state, RoleWorker)
			if lastWorker != nil {
				preferred = excludeProvider(preferred, lastWorker.Provider)
				// If that left us empty, expand to the candidate
				// set minus the last Worker provider.
				if len(preferred) == 0 {
					preferred = excludeProvider(state.Providers, lastWorker.Provider)
				}
			}
		}
		return preferred
	default:
		// Worker: family-driven default.
	}

	return p.cfg.Defaults[family]
}

// classifyDomain looks at the user-visible model hint and history summary
// to bucket the request into one of "code", "math", "recall", or
// "general". The classifier is intentionally coarse — the difficulty
// classifier already filtered out trivial cases.
func classifyDomain(state TurnState) string {
	corpus := strings.ToLower(state.UserModelHint)
	for _, t := range state.History {
		corpus += " " + strings.ToLower(t.Summary)
	}
	if codePattern.MatchString(corpus) {
		return "code"
	}
	if mathPattern.MatchString(corpus) {
		return "math"
	}
	if recallPattern.MatchString(corpus) {
		return "recall"
	}
	return "general"
}

// pickFirstAvailable returns the first entry in preferred that also
// appears in available, preserving the order of preferred.
func pickFirstAvailable(preferred, available []string) string {
	if len(preferred) == 0 || len(available) == 0 {
		return ""
	}
	set := make(map[string]struct{}, len(available))
	for _, a := range available {
		set[a] = struct{}{}
	}
	for _, p := range preferred {
		if _, ok := set[p]; ok {
			return p
		}
	}
	return ""
}

// excludeProvider returns a copy of the slice with all entries equal to
// the supplied value removed.
func excludeProvider(in []string, excluded string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == excluded {
			continue
		}
		out = append(out, v)
	}
	return out
}

// lastTurnByRole returns the most recent history entry with the requested
// role, or nil when no such turn exists.
func lastTurnByRole(state TurnState, role Role) *TurnHistory {
	for i := len(state.History) - 1; i >= 0; i-- {
		if state.History[i].Role == role {
			t := state.History[i]
			return &t
		}
	}
	return nil
}

// Domain regexes — intentionally permissive. False positives only mean a
// slightly suboptimal initial route; the loop will recover.
var (
	codePattern   = regexp.MustCompile(`(?i)\b(code|function|class|method|bug|stack ?trace|compile|debug|refactor|api|sdk|json|yaml)\b|` + "`{3}|`")
	mathPattern   = regexp.MustCompile(`(?i)\b(prove|theorem|integral|derivative|matrix|equation|sum|product|inequality|geometry|algebra)\b|\\\(|\\\[|\$\$`)
	recallPattern = regexp.MustCompile(`(?i)\b(history|biography|when did|where is|who was|definition|explain|describe|summarize|cite|reference)\b`)
)

// errEmptyProviderSet is returned by Decide when no candidates are
// available. The runner treats this as a hard error.
var errEmptyProviderSet = stringError("orchestrator: empty provider candidate set")

type stringError string

func (e stringError) Error() string { return string(e) }
