package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// catalogFixture is a small worked-example catalog the direct-model
// tests share. Each entry has a paragraph-level Instructions field —
// the whole point of the v2.1 design.
func catalogFixture() []CatalogEntry {
	return []CatalogEntry{
		{
			ID:       "claude-opus-4.8",
			Provider: "claude",
			Model:    "claude-opus-4-5-20251101",
			Tags:     []string{"code", "reasoning"},
			Instructions: "Best for complex software engineering: multi-file refactors, " +
				"security audits, deep code review, debugging tricky production " +
				"issues, writing new code in any language. Prefer over GPT for SWE.",
			Roles: []string{"worker"},
		},
		{
			ID:       "gpt-5-thinking",
			Provider: "openai",
			Model:    "gpt-5-thinking",
			Tags:     []string{"math", "reasoning"},
			Instructions: "Strongest for math, multi-step planning, formal reasoning, " +
				"chain-of-thought problems. Pick over Claude for math proofs.",
			Roles: []string{"worker", "verifier"},
		},
		{
			ID:       "haiku-fast",
			Provider: "claude",
			Model:    "claude-haiku-4-5-20251001",
			Tags:     []string{"cheap", "fast"},
			Instructions: "Fast, cheap model for short summarization, simple Q&A, " +
				"classification. Use as Thinker / planner role.",
			Roles: []string{"thinker", "classifier"},
		},
	}
}

func TestRenderCatalogBlock(t *testing.T) {
	entries := catalogFixture()
	out := renderCatalogBlock(entries)
	for _, e := range entries {
		if !strings.Contains(out, "["+e.ID+"]") {
			t.Errorf("missing id header for %s in:\n%s", e.ID, out)
		}
		// At least the first ~30 chars of the instructions should appear.
		head := e.Instructions
		if len(head) > 30 {
			head = head[:30]
		}
		if !strings.Contains(out, head) {
			t.Errorf("missing instructions head %q for %s", head, e.ID)
		}
	}
}

func TestDirectModelClassifierNormalizeChoice(t *testing.T) {
	cat := catalogFixture()
	c := newDirectModelClassifier(ClassifierLLMConfig{
		Enabled: true,
		Model:   "fake-mini",
	}, cat, nil)
	cases := []struct {
		raw  string
		want string
	}{
		{"claude-opus-4.8", "claude-opus-4.8"},
		{"CLAUDE-OPUS-4.8", "claude-opus-4.8"},
		{`"gpt-5-thinking"`, "gpt-5-thinking"},
		{"model: haiku-fast", "haiku-fast"},
		{"After reviewing, I pick gpt-5-thinking for this math question.", "gpt-5-thinking"},
		{"none-of-the-above", ""},
	}
	for _, tc := range cases {
		got := c.normalizeChoice(tc.raw, cat)
		if got != tc.want {
			t.Errorf("normalizeChoice(%q): want %q, got %q", tc.raw, tc.want, got)
		}
	}
}

func TestDirectModelClassifierEligibleEntries(t *testing.T) {
	cat := catalogFixture()
	c := newDirectModelClassifier(ClassifierLLMConfig{
		Enabled: true,
		Model:   "fake-mini",
	}, cat, nil)

	// Only "claude" available — drops gpt-5-thinking.
	got := c.eligibleEntries([]string{"claude"})
	if len(got) != 2 {
		t.Fatalf("expected 2 claude entries, got %d", len(got))
	}
	for _, e := range got {
		if e.Provider != "claude" {
			t.Fatalf("non-claude entry leaked through: %+v", e)
		}
	}
}

func TestDirectModelClassifierCallsManagerWithCatalogPrompt(t *testing.T) {
	cat := catalogFixture()
	var seenPayload []byte
	mgr := &fakeManager{
		executeHandler: func(providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			seenPayload = append([]byte(nil), req.Payload...)
			return coreexecutor.Response{Payload: openAIResponse("claude-opus-4.8")}, nil
		},
	}
	c := newDirectModelClassifier(ClassifierLLMConfig{
		Enabled:       true,
		Provider:      "openai",
		Model:         "gpt-5-mini",
		Timeout:       2 * time.Second,
		MaxInputChars: 200,
	}, cat, mgr)

	got := c.classify(context.Background(), ciFromText("refactor this whole package"), DecideRequest{
		Providers: []string{"claude", "openai"},
	})
	if got != "claude-opus-4.8" {
		t.Fatalf("picked id: want claude-opus-4.8, got %q", got)
	}

	var body map[string]any
	if err := json.Unmarshal(seenPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("expected >=2 messages, got %d", len(msgs))
	}
	userMsg, _ := msgs[1].(map[string]any)
	content, _ := userMsg["content"].(string)
	if !strings.Contains(content, "[claude-opus-4.8]") || !strings.Contains(content, "[gpt-5-thinking]") {
		t.Fatalf("user message missing catalog headers:\n%s", content)
	}
	if !strings.Contains(content, "refactor this whole package") {
		t.Fatalf("user message missing request excerpt:\n%s", content)
	}
}

func TestDirectModelClassifierProviderMustBeInCandidateSet(t *testing.T) {
	cat := catalogFixture()
	var called int32
	mgr := &fakeManager{
		executeHandler: func(providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			atomic.AddInt32(&called, 1)
			return coreexecutor.Response{}, nil
		},
	}
	c := newDirectModelClassifier(ClassifierLLMConfig{
		Enabled:  true,
		Provider: "openai", // not in candidate set
		Model:    "gpt-5-mini",
	}, cat, mgr)

	got := c.classify(context.Background(), ciFromText("hi"), DecideRequest{
		Providers: []string{"claude"},
	})
	if got != "" {
		t.Fatalf("want empty pick when provider absent, got %q", got)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Fatalf("manager should not be called when classifier provider absent")
	}
}

func TestRulesPolicyHonoursModelCatalogID(t *testing.T) {
	cat := catalogFixture()
	p := NewRulesPolicy(
		RulesConfig{
			// Legacy default that would otherwise win.
			Defaults: map[string][]string{"code": {"claude"}},
		},
		WithCatalog(cat),
	)
	state := TurnState{
		UserModelHint:  "anything",
		Providers:      []string{"claude", "openai"},
		Difficulty:     BucketEasy,
		Turn:           0,
		Budget:         4,
		ModelCatalogID: "gpt-5-thinking",
	}
	act, err := p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if act.Provider != "openai" || act.Model != "gpt-5-thinking" {
		t.Fatalf("direct-model id ignored; got %+v", act)
	}
}

func TestRulesPolicyFallsBackWhenCatalogIDProviderMissing(t *testing.T) {
	cat := catalogFixture()
	p := NewRulesPolicy(
		RulesConfig{
			Defaults: map[string][]string{"code": {"claude"}},
		},
		WithCatalog(cat),
	)
	// Picked id points at openai, but openai is not in candidates.
	state := TurnState{
		UserModelHint:  "write code",
		Providers:      []string{"claude"},
		Difficulty:     BucketEasy,
		Turn:           0,
		Budget:         4,
		ModelCatalogID: "gpt-5-thinking",
	}
	act, err := p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	// Should drop the ModelCatalogID hint and fall through to legacy
	// code → claude.
	if act.Provider != "claude" {
		t.Fatalf("expected fallback to claude, got %+v", act)
	}
}

func TestRulesPolicyThinkerVerifierUseRolesTaggedCatalog(t *testing.T) {
	cat := catalogFixture()
	p := NewRulesPolicy(
		RulesConfig{
			VerifierMustDiffer: true,
		},
		WithCatalog(cat),
	)
	// Hard difficulty so the role machinery engages.
	state := TurnState{
		UserModelHint: "tough math problem",
		Providers:     []string{"claude", "openai"},
		Difficulty:    BucketHard,
		Budget:        4,
	}

	// Turn 0: Thinker. Catalog has "haiku-fast" tagged thinker.
	state.Turn = 0
	act, err := p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("turn 0: %v", err)
	}
	if act.Role != RoleThinker {
		t.Fatalf("turn 0 want Thinker, got %s", act.Role)
	}
	if act.Model != "claude-haiku-4-5-20251001" {
		t.Fatalf("turn 0 want haiku model, got %+v", act)
	}
	state.History = append(state.History, TurnHistory{Index: 0, Role: RoleThinker, Provider: act.Provider, Model: act.Model})

	// Turn 1: Worker. No ModelCatalogID, no Category — fall back to
	// legacy. We didn't configure Defaults, so it goes to first
	// candidate (claude). That's fine for this test; we're checking
	// that Role=Thinker/Verifier specifically uses Roles-tagged
	// entries while Worker doesn't.
	state.Turn = 1
	act, err = p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if act.Role != RoleWorker {
		t.Fatalf("turn 1 want Worker, got %s", act.Role)
	}
	state.History = append(state.History, TurnHistory{Index: 1, Role: RoleWorker, Provider: act.Provider, Model: act.Model})

	// Turn 2: Verifier. Catalog has "gpt-5-thinking" tagged verifier.
	// VerifierMustDiffer: pruned set excludes the last Worker
	// provider — but haiku-fast is thinker-only, opus is worker-only,
	// gpt-5-thinking is worker+verifier. The verifier walk picks the
	// first Roles-tagged-verifier entry whose provider is in the
	// pruned set.
	state.Turn = 2
	act, err = p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if act.Role != RoleVerifier {
		t.Fatalf("turn 2 want Verifier, got %s", act.Role)
	}
	if act.Model != "gpt-5-thinking" {
		t.Fatalf("turn 2 want gpt-5-thinking, got %+v", act)
	}
}

func TestConfigValidateDirectModelRequiresCatalog(t *testing.T) {
	cfg := Default()
	cfg.Enabled = true
	cfg.Classifier = ClassifierConfig{
		Kind: "direct-model",
		LLM:  ClassifierLLMConfig{Enabled: true, Model: "gpt-5-mini"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error: direct-model requires catalog")
	}

	cfg.Catalog = []CatalogEntry{
		{ID: "x", Provider: "openai", Model: "gpt-5-mini"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validation unexpectedly failed with catalog: %v", err)
	}
}

func TestOrchestratorDecideRunsDirectModelClassifier(t *testing.T) {
	cat := catalogFixture()

	mgr := &fakeManager{
		executeHandler: func(providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			// Both the classifier call and downstream worker calls
			// land here. The classifier asks the "openai" provider
			// for the gpt-5-mini model; reply with our picked id.
			if req.Model == "gpt-5-mini" {
				return coreexecutor.Response{Payload: openAIResponse("claude-opus-4.8")}, nil
			}
			return coreexecutor.Response{Payload: openAIResponse("worker ok")}, nil
		},
	}

	cfg := Default()
	cfg.Enabled = true
	cfg.EnabledForAPIKeys = []string{"*"}
	cfg.Mode = "single-shot"
	cfg.Catalog = cat
	cfg.Classifier = ClassifierConfig{
		Kind: "direct-model",
		LLM: ClassifierLLMConfig{
			Enabled:       true,
			Provider:      "openai",
			Model:         "gpt-5-mini",
			Timeout:       2 * time.Second,
			MaxInputChars: 200,
		},
	}

	o, err := New(cfg, mgr)
	if err != nil || o == nil {
		t.Fatalf("New(): %v err=%v", o, err)
	}

	d := o.Decide(context.Background(), DecideRequest{
		HandlerType:     "openai",
		Providers:       []string{"openai", "claude"},
		NormalizedModel: "auto",
		Payload:         openAIRequest("refactor this large package"),
		APIKey:          "tester",
	})
	if d.ModelCatalogID != "claude-opus-4.8" {
		t.Fatalf("want catalog id claude-opus-4.8, got %q", d.ModelCatalogID)
	}
	if d.NormalizedModel != "claude-opus-4-5-20251101" {
		t.Fatalf("want resolved model name, got %q", d.NormalizedModel)
	}
	if len(d.Providers) != 1 || d.Providers[0] != "claude" {
		t.Fatalf("want providers narrowed to [claude], got %v", d.Providers)
	}
}
