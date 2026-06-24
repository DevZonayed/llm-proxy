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
