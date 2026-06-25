package orchestrator

import (
	"context"
	"fmt"
)

// Role is the assignment given to a worker model for a single turn of the
// tri-role loop, mirroring the TRINITY paper's Thinker/Worker/Verifier
// roles. Single-shot mode always uses RoleWorker.
type Role int

const (
	// RoleWorker produces concrete content (the final answer or an
	// intermediate solution the Verifier will judge). The Worker is the
	// only role whose output is streamed back to the client.
	RoleWorker Role = iota
	// RoleThinker produces a plan or critique; its output is folded into
	// subsequent Worker turns as additional system context.
	RoleThinker
	// RoleVerifier evaluates a Worker output and emits ACCEPT or REVISE.
	// A single ACCEPT halts the loop.
	RoleVerifier
)

// String returns the canonical lowercase name for the role.
func (r Role) String() string {
	switch r {
	case RoleThinker:
		return "thinker"
	case RoleWorker:
		return "worker"
	case RoleVerifier:
		return "verifier"
	default:
		return fmt.Sprintf("role(%d)", int(r))
	}
}

// Bucket categorises a request's predicted difficulty.
type Bucket int

const (
	// BucketEasy is the default — single-shot, no Verifier shadow turn.
	BucketEasy Bucket = iota
	// BucketMedium runs single-shot, optionally with a Verifier shadow
	// turn that is logged but does not gate the response.
	BucketMedium
	// BucketHard runs the full tri-role loop.
	BucketHard
)

// String returns the canonical lowercase bucket name.
func (b Bucket) String() string {
	switch b {
	case BucketEasy:
		return "easy"
	case BucketMedium:
		return "medium"
	case BucketHard:
		return "hard"
	default:
		return fmt.Sprintf("bucket(%d)", int(b))
	}
}

// Action is the policy's per-turn decision. It picks a provider/model pair
// and the role that worker should play for this turn.
type Action struct {
	// Provider is the provider key the next call will be routed to. Must
	// be one of the values in TurnState.Providers.
	Provider string

	// Model is the upstream model name to use for the call. Typically the
	// same value across turns (the request's normalized model), but the
	// policy may override it.
	Model string

	// Role is the assignment for the worker model on this turn.
	Role Role

	// Halt signals the runner to stop the loop. Set by the Verifier when
	// the cumulative Worker output is acceptable. The Worker's last
	// response will be replayed to the client.
	Halt bool
}

// TurnHistory captures the outcome of a previously executed turn so the
// policy can make context-aware decisions on subsequent turns.
type TurnHistory struct {
	// Index is the turn number, starting at 0.
	Index int
	// Role records the assignment that ran on this turn.
	Role Role
	// Provider records the provider that served this turn.
	Provider string
	// Model records the upstream model used for this turn.
	Model string
	// Summary is a short, human-readable extraction of the turn's output
	// (capped to the configured excerpt length for Worker turns).
	Summary string
	// Verdict is the Verifier's verdict — "ACCEPT" or "REVISE" — when the
	// turn ran as a Verifier. Empty otherwise.
	Verdict string
}

// TurnState is the input the policy sees for each Decide call.
type TurnState struct {
	// UserModelHint is the model name the client originally requested
	// (after autoresolution but before alias expansion).
	UserModelHint string

	// Providers is the candidate provider set computed by the existing
	// model resolution layer. The policy MUST return a provider from
	// this set; the runner enforces the constraint and falls back to
	// providers[0] on violation.
	Providers []string

	// Difficulty is the pre-classifier verdict.
	Difficulty Bucket

	// Turn is the 0-indexed turn number.
	Turn int

	// History is the ordered list of previously executed turns.
	History []TurnHistory

	// Budget is the maximum number of turns the runner is willing to run.
	Budget int

	// CategoryHint is the category name the orchestrator pre-classified
	// the request into, or "" when no category matched. The rules policy
	// consults the configured Catalog/Categories tables first; when
	// CategoryHint is empty it falls back to the legacy domain
	// heuristic. The hint is stable across all turns of a single
	// request — re-classification mid-loop is intentionally avoided.
	CategoryHint string

	// ModelCatalogID is set when the direct-model classifier picks a
	// specific catalog entry for the request. The rules policy uses
	// this as the highest-priority signal for the Worker role: look
	// the entry up in the catalog and route to its (provider, model)
	// directly. Empty when direct-model classification was skipped
	// or returned no match. Thinker/Verifier turns ignore this hint
	// and fall back to category role-pins or Roles-tagged catalog
	// entries.
	ModelCatalogID string
}

// Policy decides what to do on each turn of an orchestrated request.
// Implementations must be safe for concurrent use.
type Policy interface {
	// Decide picks an Action for the supplied state. Returning a non-nil
	// error causes the runner to fall back to single-shot dispatch.
	Decide(ctx context.Context, state TurnState) (Action, error)
}
