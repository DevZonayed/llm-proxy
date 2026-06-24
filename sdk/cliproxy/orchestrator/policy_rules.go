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
// The policy is deterministic and cheap: a few regex checks against the
// raw request payload bytes and a lookup into the configured per-family
// provider ordering.
type RulesPolicy struct {
	cfg RulesConfig
}

// NewRulesPolicy constructs a RulesPolicy from configuration.
func NewRulesPolicy(cfg RulesConfig) *RulesPolicy {
	return &RulesPolicy{cfg: cfg}
}

// Decide implements Policy.
func (p *RulesPolicy) Decide(ctx context.Context, state TurnState) (Action, error) {
	if len(state.Providers) == 0 {
		return Action{}, errEmptyProviderSet
	}

	role := p.roleForTurn(state)

	// Build the preferred provider list for this turn.
	preferred := p.preferredProvidersFor(role, state)

	provider := pickFirstAvailable(preferred, state.Providers)
	if provider == "" {
		provider = state.Providers[0]
	}

	return Action{
		Provider: provider,
		Model:    state.UserModelHint,
		Role:     role,
		Halt:     false,
	}, nil
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
// preference.
func (p *RulesPolicy) preferredProvidersFor(role Role, state TurnState) []string {
	family := classifyDomain(state)

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
