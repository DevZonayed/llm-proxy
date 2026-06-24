package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// AuthManager is the subset of *coreauth.Manager the orchestrator depends
// on. Defining it as an interface keeps this package testable without
// pulling in the full auth lifecycle machinery.
type AuthManager interface {
	Execute(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error)
	ExecuteStream(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error)
}

// Orchestrator is the public entry point. Construct one per running
// server via New. Methods are safe for concurrent use.
type Orchestrator struct {
	cfg        Config
	policy     Policy
	classifier *Classifier
	recorder   *TraceRecorder
	mgr        AuthManager

	mu     sync.Mutex
	closed bool
}

// New constructs an Orchestrator from configuration and the auth manager
// it will dispatch through. Returns nil when the configuration disables
// orchestration so callers may treat a nil receiver as a permanent
// no-op.
func New(cfg Config, mgr AuthManager) (*Orchestrator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, nil
	}
	if mgr == nil {
		return nil, fmt.Errorf("orchestrator: AuthManager is required")
	}

	rules := NewRulesPolicy(cfg.Policy.Rules)
	var policy Policy = rules
	if strings.EqualFold(strings.TrimSpace(cfg.Policy.Kind), "learned") {
		policy = NewLearnedPolicy(cfg.Policy.Learned, rules)
	}

	return &Orchestrator{
		cfg:        cfg,
		policy:     policy,
		classifier: NewClassifier(cfg.Difficulty),
		recorder:   NewTraceRecorder(cfg.Trace),
		mgr:        mgr,
	}, nil
}

// Close releases any background resources held by the orchestrator. Safe
// to call multiple times and on a nil receiver.
func (o *Orchestrator) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	if o.recorder != nil {
		return o.recorder.Close()
	}
	return nil
}

// IsEnabled reports whether the orchestrator should engage for the given
// inbound request. A nil receiver always returns false.
func (o *Orchestrator) IsEnabled(apiKey string, hdr http.Header) bool {
	if o == nil || !o.cfg.Enabled {
		return false
	}
	if o.cfg.RespectRequestHeaders && hdr != nil {
		switch strings.ToLower(strings.TrimSpace(hdr.Get("X-Orchestrator"))) {
		case "off", "false", "0", "no":
			return false
		}
	}
	if !o.cfg.apiKeyAllowed(apiKey) {
		return false
	}
	return true
}

// DecideRequest is the input to Decide.
type DecideRequest struct {
	// HandlerType is the inbound schema identifier, e.g. "openai" or
	// "claude". Determines how the payload is parsed for role prompts.
	HandlerType string
	// Providers is the candidate provider set computed by the existing
	// model resolution. Must contain at least one entry.
	Providers []string
	// NormalizedModel is the resolved model name the request will be
	// dispatched under.
	NormalizedModel string
	// Payload is the inbound request body bytes.
	Payload []byte
	// Headers are the inbound request headers (for X-Orchestrator-Mode).
	Headers http.Header
	// APIKey is the inbound client API key. Recorded as a hash in
	// traces; never persisted in plaintext.
	APIKey string
}

// Decision is the orchestrator's per-request verdict.
type Decision struct {
	// DecisionID uniquely identifies this orchestrated request. Use it
	// to join traces with downstream usage logs.
	DecisionID string
	// Providers is the narrowed candidate list the caller should use
	// for the underlying dispatch. Always contains at least one entry.
	Providers []string
	// UseLoop reports whether the tri-role loop should be executed. If
	// false, callers proceed with their existing single-call dispatch
	// using the narrowed Providers list. If true, callers must invoke
	// RunStream or RunNonStream and forward the result.
	UseLoop bool
	// Difficulty is the classifier verdict. Surfaced for logging.
	Difficulty Bucket
	// Mode is the resolved mode string ("single-shot" or "tri-role").
	Mode string
}

// Decide computes the orchestrator's verdict for a single request. It
// must be safe to call concurrently. On any error the returned Decision
// safely falls back to the original Providers and UseLoop == false so
// the handler can continue exactly as it does today.
func (o *Orchestrator) Decide(ctx context.Context, req DecideRequest) Decision {
	if o == nil {
		return Decision{
			DecisionID: "",
			Providers:  req.Providers,
			UseLoop:    false,
			Mode:       "single-shot",
		}
	}

	if len(req.Providers) == 0 {
		return Decision{
			DecisionID: "",
			Providers:  req.Providers,
			UseLoop:    false,
			Mode:       "single-shot",
		}
	}

	bucket := o.classifier.Classify(ClassifyInput{
		Payload:      req.Payload,
		SourceFormat: req.HandlerType,
	})

	mode := o.resolveMode(bucket, req.Headers)
	useLoop := mode == "tri-role" && supportsLoop(req.HandlerType)

	// For the tri-role path we keep the full candidate set: the loop's
	// own per-turn policy calls will narrow per turn. For single-shot we
	// ask the policy for a Worker-shaped action (override difficulty so
	// the rules policy returns Worker rather than Thinker).
	if useLoop {
		return Decision{
			DecisionID: newDecisionID(),
			Providers:  req.Providers,
			UseLoop:    true,
			Difficulty: bucket,
			Mode:       mode,
		}
	}

	state := TurnState{
		UserModelHint: req.NormalizedModel,
		Providers:     req.Providers,
		Difficulty:    BucketMedium,
		Turn:          0,
		Budget:        o.cfg.Budgets.MaxTurns,
	}

	deadline := o.cfg.Budgets.SingleShotFallback
	policyCtx := ctx
	var cancel context.CancelFunc
	if deadline > 0 {
		policyCtx, cancel = context.WithTimeout(ctx, deadline)
	}
	action, err := o.policy.Decide(policyCtx, state)
	if cancel != nil {
		cancel()
	}

	providers := req.Providers
	if err == nil && action.Provider != "" {
		if pickFirstAvailable([]string{action.Provider}, req.Providers) != "" {
			providers = []string{action.Provider}
		}
	}

	return Decision{
		DecisionID: newDecisionID(),
		Providers:  providers,
		UseLoop:    false,
		Difficulty: bucket,
		Mode:       mode,
	}
}

// resolveMode picks "single-shot" or "tri-role" from configuration,
// difficulty bucket, and optional per-request header override.
func (o *Orchestrator) resolveMode(bucket Bucket, hdr http.Header) string {
	override := ""
	if o.cfg.RespectRequestHeaders && hdr != nil {
		override = strings.ToLower(strings.TrimSpace(hdr.Get("X-Orchestrator-Mode")))
	}
	switch override {
	case "ultra", "tri-role":
		return "tri-role"
	case "fast", "single-shot":
		return "single-shot"
	}

	switch o.cfg.modeNormalized() {
	case "single-shot":
		return "single-shot"
	case "tri-role":
		return "tri-role"
	default:
		// auto
		if bucket == BucketHard {
			return "tri-role"
		}
		return "single-shot"
	}
}

// supportsLoop reports whether the tri-role loop is implemented for the
// given inbound schema. v0 only supports OpenAI-shaped payloads —
// constructing Thinker/Verifier prompts in arbitrary provider schemas is
// deferred.
func supportsLoop(handlerType string) bool {
	switch strings.ToLower(strings.TrimSpace(handlerType)) {
	case "openai", "":
		return true
	default:
		return false
	}
}

// newDecisionID returns a fresh ULID-ish identifier. We use UUID v4
// because the codebase already depends on google/uuid; collision risk is
// negligible at the scales involved.
func newDecisionID() string {
	return uuid.NewString()
}

// RunRequest is the input to RunStream/RunNonStream. It carries the same
// data the handler would otherwise pass to Manager.Execute*.
type RunRequest struct {
	DecisionID      string
	Difficulty      Bucket
	HandlerType     string
	Providers       []string
	NormalizedModel string
	Payload         []byte
	OriginalRequest []byte
	Headers         http.Header
	Alt             string
	APIKey          string

	// Metadata is forwarded into coreexecutor.Options.Metadata. The
	// orchestrator augments it but never strips fields.
	Metadata map[string]any
}

// RunResult is the non-streaming run result.
type RunResult struct {
	Payload []byte
	Headers http.Header
	// HaltedOn names the loop exit reason ("verifier_accept",
	// "turns_exhausted", "wall_budget_exhausted", "policy_error").
	HaltedOn string
}

// RunNonStream executes the tri-role loop and returns the final Worker
// payload. Used when the client requested a non-streaming endpoint.
func (o *Orchestrator) RunNonStream(ctx context.Context, req RunRequest) (RunResult, error) {
	if o == nil {
		return RunResult{}, fmt.Errorf("orchestrator: nil receiver")
	}
	final, _, err := o.runLoop(ctx, req, false)
	if err != nil {
		return RunResult{}, err
	}
	headers := http.Header{}
	if final.Headers != nil {
		headers = final.Headers
	}
	return RunResult{
		Payload:  final.Payload,
		Headers:  headers,
		HaltedOn: final.HaltedOn,
	}, nil
}

// StreamResult carries a streaming response back to the caller.
type StreamResult struct {
	Chunks   <-chan []byte
	Errors   <-chan error
	Headers  http.Header
	HaltedOn string
}

// RunStream executes the tri-role loop, then re-issues the final Worker
// turn as a streaming call and returns the result.
func (o *Orchestrator) RunStream(ctx context.Context, req RunRequest) (StreamResult, error) {
	if o == nil {
		return StreamResult{}, fmt.Errorf("orchestrator: nil receiver")
	}
	_, stream, err := o.runLoop(ctx, req, true)
	if err != nil {
		return StreamResult{}, err
	}
	return stream, nil
}

// loopFinal is the non-stream outcome of the tri-role loop. Either
// Payload (non-stream) or stream.Chunks (stream) is set depending on the
// caller's request.
type loopFinal struct {
	Payload  []byte
	Headers  http.Header
	HaltedOn string
}

// runLoop is the shared driver for RunNonStream and RunStream. It
// returns:
//   - the non-stream outcome when stream == false,
//   - a StreamResult when stream == true.
func (o *Orchestrator) runLoop(ctx context.Context, req RunRequest, stream bool) (loopFinal, StreamResult, error) {
	if len(req.Providers) == 0 {
		return loopFinal{}, StreamResult{}, fmt.Errorf("orchestrator: empty provider set")
	}

	// Apply wall-clock budget.
	if o.cfg.Budgets.WallBudget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.cfg.Budgets.WallBudget)
		defer cancel()
	}

	state := TurnState{
		UserModelHint: req.NormalizedModel,
		Providers:     req.Providers,
		Difficulty:    req.Difficulty,
		Budget:        o.cfg.Budgets.MaxTurns,
	}

	traceRec := TraceRecord{
		DecisionID: req.DecisionID,
		Time:       time.Now().UTC(),
		APIKeyHash: hashAPIKey(req.APIKey),
		Mode:       "tri-role",
		Difficulty: req.Difficulty.String(),
		UserHint:   req.NormalizedModel,
		Providers:  append([]string(nil), req.Providers...),
	}
	defer func() {
		// Persist whatever we accumulated, success or failure.
		o.recorder.Record(traceRec)
	}()

	var (
		lastWorker      *workerOutput
		verifierAccepts = 0
	)

	for turn := 0; turn < o.cfg.Budgets.MaxTurns; turn++ {
		state.Turn = turn
		action, err := o.policy.Decide(ctx, state)
		if err != nil {
			traceRec.Final = TraceFinal{TurnIndex: turn, HaltedOn: "policy_error", WorkerError: err.Error()}
			return loopFinal{}, StreamResult{}, fmt.Errorf("policy: %w", err)
		}

		// Enforce the provider-must-be-in-candidate-set invariant.
		if pickFirstAvailable([]string{action.Provider}, req.Providers) == "" {
			action.Provider = req.Providers[0]
		}

		// Halt-from-policy shortcut: replay last Worker if we have one.
		if action.Halt && lastWorker != nil {
			traceRec.Final = TraceFinal{TurnIndex: turn, HaltedOn: "policy_halt"}
			return o.finalizeWorker(ctx, req, lastWorker, stream, &traceRec)
		}

		switch action.Role {
		case RoleThinker:
			out, terr := o.runThinker(ctx, req, action, state, &traceRec, turn)
			if terr != nil {
				// Thinker failure is non-fatal — continue to Worker.
				traceRec.Turns = append(traceRec.Turns, TraceTurn{
					Index: turn, Role: action.Role.String(), Provider: action.Provider,
					Model: action.Model, Error: terr.Error(),
				})
			} else {
				state.History = append(state.History, TurnHistory{
					Index:    turn,
					Role:     action.Role,
					Provider: action.Provider,
					Model:    action.Model,
					Summary:  out,
				})
			}
		case RoleWorker:
			out, werr := o.runWorker(ctx, req, action, state, &traceRec, turn)
			if werr != nil {
				traceRec.Final = TraceFinal{TurnIndex: turn, HaltedOn: "worker_error", WorkerError: werr.Error()}
				return loopFinal{}, StreamResult{}, fmt.Errorf("worker: %w", werr)
			}
			lastWorker = out
			state.History = append(state.History, TurnHistory{
				Index:    turn,
				Role:     action.Role,
				Provider: action.Provider,
				Model:    action.Model,
				Summary:  out.Excerpt,
			})
		case RoleVerifier:
			verdict, diag, verr := o.runVerifier(ctx, req, action, state, lastWorker, &traceRec, turn)
			if verr != nil {
				// Verifier failure is non-fatal — treat as REVISE.
				verdict = "REVISE"
				diag = verr.Error()
			}
			state.History = append(state.History, TurnHistory{
				Index:    turn,
				Role:     action.Role,
				Provider: action.Provider,
				Model:    action.Model,
				Verdict:  verdict,
			})
			if strings.EqualFold(strings.TrimSpace(verdict), "ACCEPT") {
				verifierAccepts++
				if verifierAccepts >= o.cfg.Budgets.MinVerifierTurns && lastWorker != nil {
					traceRec.Reward = &TraceReward{VerifierAccept: true}
					traceRec.Final = TraceFinal{TurnIndex: turn, HaltedOn: "verifier_accept"}
					return o.finalizeWorker(ctx, req, lastWorker, stream, &traceRec)
				}
			} else {
				// REVISE — drop the last Worker so the next loop
				// iteration plans anew.
				_ = diag
			}
		}

		// Wall budget check.
		if err := ctx.Err(); err != nil {
			traceRec.Final = TraceFinal{TurnIndex: turn, HaltedOn: "wall_budget_exhausted"}
			if lastWorker != nil {
				return o.finalizeWorker(ctx, req, lastWorker, stream, &traceRec)
			}
			return loopFinal{}, StreamResult{}, fmt.Errorf("orchestrator: wall budget exhausted: %w", err)
		}
	}

	// Budget exhausted.
	if lastWorker != nil {
		traceRec.Final = TraceFinal{TurnIndex: o.cfg.Budgets.MaxTurns, HaltedOn: "turns_exhausted"}
		return o.finalizeWorker(ctx, req, lastWorker, stream, &traceRec)
	}
	traceRec.Final = TraceFinal{TurnIndex: o.cfg.Budgets.MaxTurns, HaltedOn: "no_worker_output"}
	return loopFinal{}, StreamResult{}, fmt.Errorf("orchestrator: turns exhausted without worker output")
}

// finalizeWorker takes the last Worker output and either returns it
// directly (non-stream) or re-issues the Worker request as a stream and
// returns the new stream channel.
func (o *Orchestrator) finalizeWorker(ctx context.Context, req RunRequest, w *workerOutput, stream bool, rec *TraceRecord) (loopFinal, StreamResult, error) {
	if !stream {
		return loopFinal{
			Payload:  w.RawPayload,
			Headers:  w.Headers,
			HaltedOn: rec.Final.HaltedOn,
		}, StreamResult{}, nil
	}

	// Re-issue Worker as a stream so the client receives SSE bytes
	// end-to-end. This is the "Option A" from the design doc.
	execReq := coreexecutor.Request{
		Model:   w.Model,
		Payload: w.RequestPayload,
	}
	opts := coreexecutor.Options{
		Stream:          true,
		Alt:             req.Alt,
		OriginalRequest: req.OriginalRequest,
		SourceFormat:    sdktranslator.FromString(req.HandlerType),
		Headers:         req.Headers,
		Metadata:        cloneMeta(req.Metadata),
	}
	if opts.Metadata == nil {
		opts.Metadata = map[string]any{}
	}
	opts.Metadata[coreexecutor.RequestedModelMetadataKey] = req.NormalizedModel

	streamResult, err := o.mgr.ExecuteStream(ctx, []string{w.Provider}, execReq, opts)
	if err != nil {
		return loopFinal{}, StreamResult{}, fmt.Errorf("orchestrator: stream replay: %w", err)
	}

	chunkCh := make(chan []byte)
	errCh := make(chan error, 1)
	go func() {
		defer close(chunkCh)
		defer close(errCh)
		for chunk := range streamResult.Chunks {
			if chunk.Err != nil {
				errCh <- chunk.Err
				return
			}
			select {
			case <-ctx.Done():
				return
			case chunkCh <- append([]byte(nil), chunk.Payload...):
			}
		}
	}()
	return loopFinal{}, StreamResult{
		Chunks:   chunkCh,
		Errors:   errCh,
		Headers:  streamResult.Headers,
		HaltedOn: rec.Final.HaltedOn,
	}, nil
}

// cloneMeta returns a shallow copy of the metadata map.
func cloneMeta(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
