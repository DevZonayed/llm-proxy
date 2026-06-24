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
	// Defaults maps a coarse task family ("code", "math", "recall") to
	// ordered provider candidate lists. The rules policy picks the first
	// provider that intersects the request's available candidate set.
	// Missing keys fall back to the candidate set's first entry.
	Defaults map[string][]string

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
	return nil
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
