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

// helper: build a minimal CategoryInput from raw user text.
func ciFromText(text string) CategoryInput {
	t := strings.ToLower(text)
	return CategoryInput{
		UserText:     t,
		ApproxTokens: len(t) / 4,
	}
}

func TestCategoryClassifierHeuristicKeywords(t *testing.T) {
	cats := []Category{
		{
			Name:         "code-review",
			Instructions: "Review code for bugs.",
			Match:        CategoryMatch{Keywords: []string{"review", "refactor"}, RequireCodeBlock: true},
		},
		{
			Name:         "math",
			Instructions: "Math problems.",
			Match:        CategoryMatch{Keywords: []string{"prove", "theorem"}},
		},
		{
			Name:         "default",
			Instructions: "Catch-all.",
		},
	}
	cc := newCategoryClassifier(cats, ClassifierConfig{
		Kind:      "heuristic",
		Heuristic: ClassifierHeuristicConfig{FirstMatchWins: true},
	})

	// Plain "review" text without a code block must fail code-review and
	// fall through to default (no positive matchers).
	got := cc.classify(ciFromText("please review this approach"))
	if got != "default" {
		t.Fatalf("expected default (no code block), got %q", got)
	}

	// With a code block the code-review category wins.
	got = cc.classify(ciFromText("please review this approach: ```go\nfunc f(){}\n```"))
	if got != "code-review" {
		t.Fatalf("expected code-review, got %q", got)
	}

	// Math keyword wins for math text.
	got = cc.classify(ciFromText("Please prove the Pythagorean theorem."))
	if got != "math" {
		t.Fatalf("expected math, got %q", got)
	}
}

func TestCategoryClassifierTokenBounds(t *testing.T) {
	cats := []Category{
		{
			Name:  "long-context",
			Match: CategoryMatch{MinTokens: 200},
		},
		{
			Name: "short-default",
		},
	}
	cc := newCategoryClassifier(cats, ClassifierConfig{Kind: "heuristic"})

	short := CategoryInput{UserText: "tiny", ApproxTokens: 10}
	if got := cc.classify(short); got != "short-default" {
		t.Fatalf("short request: want short-default, got %q", got)
	}
	long := CategoryInput{
		UserText:     strings.Repeat("a", 1000),
		ApproxTokens: 1000,
	}
	if got := cc.classify(long); got != "long-context" {
		t.Fatalf("long request: want long-context, got %q", got)
	}
}

func TestCategoryClassifierRegexAndNoneOf(t *testing.T) {
	cats := []Category{
		{
			Name:  "bn-en-translation",
			Match: CategoryMatch{Regex: []string{`[\x{0980}-\x{09FF}]`}},
		},
		{
			Name: "not-translation",
			Match: CategoryMatch{
				Keywords: []string{"translate"},
				NoneOf:   []string{"bengali"},
			},
		},
		{Name: "default"},
	}
	cc := newCategoryClassifier(cats, ClassifierConfig{Kind: "heuristic"})

	// Bengali text matches the first regex-based category.
	if got := cc.classify(ciFromText("নমস্কার দাদা")); got != "bn-en-translation" {
		t.Fatalf("bengali: want bn-en-translation, got %q", got)
	}
	// "translate this" without "bengali" hits not-translation.
	if got := cc.classify(ciFromText("translate this please")); got != "not-translation" {
		t.Fatalf("want not-translation, got %q", got)
	}
	// "translate bengali" is excluded by NoneOf → falls through to default.
	if got := cc.classify(ciFromText("translate bengali please")); got != "default" {
		t.Fatalf("want default (NoneOf excluded), got %q", got)
	}
}

func TestCatalogIndexPickForRole(t *testing.T) {
	catalog := []CatalogEntry{
		{ID: "opus", Provider: "anthropic", Model: "claude-opus-4.8"},
		{ID: "gpt-mini", Provider: "openai", Model: "gpt-5-mini"},
		{ID: "haiku", Provider: "anthropic", Model: "claude-haiku-4.5"},
	}
	idx := newCatalogIndex(catalog)

	cat := Category{
		Name: "code",
		Prefer: []string{"opus", "gpt-mini"},
		RolePins: map[string]string{
			"thinker":  "haiku",
			"verifier": "gpt-mini",
		},
	}

	// Worker role uses Prefer in order.
	entry, ok := idx.pickForRole(cat, "worker", []string{"anthropic", "openai"})
	if !ok || entry.Model != "claude-opus-4.8" {
		t.Fatalf("worker: want claude-opus-4.8, got %+v ok=%v", entry, ok)
	}

	// If anthropic isn't available, fall through to next in Prefer.
	entry, ok = idx.pickForRole(cat, "worker", []string{"openai"})
	if !ok || entry.Model != "gpt-5-mini" {
		t.Fatalf("worker without anthropic: want gpt-5-mini, got %+v ok=%v", entry, ok)
	}

	// Thinker honours RolePin.
	entry, ok = idx.pickForRole(cat, "thinker", []string{"anthropic", "openai"})
	if !ok || entry.Model != "claude-haiku-4.5" {
		t.Fatalf("thinker: want claude-haiku-4.5, got %+v ok=%v", entry, ok)
	}

	// Verifier RolePin maps to openai.
	entry, ok = idx.pickForRole(cat, "verifier", []string{"anthropic", "openai"})
	if !ok || entry.Model != "gpt-5-mini" {
		t.Fatalf("verifier: want gpt-5-mini, got %+v ok=%v", entry, ok)
	}

	// Unknown id silently skipped — empty Prefer → not ok.
	emptyCat := Category{Name: "x", Prefer: []string{"does-not-exist"}}
	if _, ok := idx.pickForRole(emptyCat, "worker", []string{"anthropic"}); ok {
		t.Fatalf("unknown id should yield no entry")
	}
}

func TestRulesPolicyUsesCategoryBeforeLegacy(t *testing.T) {
	catalog := []CatalogEntry{
		{ID: "opus", Provider: "anthropic", Model: "claude-opus-4.8"},
		{ID: "gpt-mini", Provider: "openai", Model: "gpt-5-mini"},
	}
	cats := []Category{
		{
			Name:   "review",
			Match:  CategoryMatch{Keywords: []string{"review"}},
			Prefer: []string{"opus"},
		},
	}
	p := NewRulesPolicy(
		RulesConfig{
			// Legacy defaults that would otherwise route code → some
			// other provider. We expect the category path to win.
			Defaults: map[string][]string{"code": {"openai", "anthropic"}},
		},
		WithCatalog(catalog),
		WithCategories(cats),
	)

	state := TurnState{
		UserModelHint: "code review my function",
		Providers:     []string{"anthropic", "openai"},
		Difficulty:    BucketEasy,
		Turn:          0,
		Budget:        4,
		CategoryHint:  "review",
	}
	act, err := p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if act.Provider != "anthropic" || act.Model != "claude-opus-4.8" {
		t.Fatalf("category did not win: got %+v", act)
	}
}

func TestRulesPolicyFallsBackToLegacyWhenCategoryEmpty(t *testing.T) {
	catalog := []CatalogEntry{
		{ID: "opus", Provider: "anthropic", Model: "claude-opus-4.8"},
	}
	p := NewRulesPolicy(
		RulesConfig{
			Defaults: map[string][]string{"code": {"openai"}},
			Models:   map[string]string{"code": "gpt-5"},
		},
		WithCatalog(catalog),
	)
	state := TurnState{
		UserModelHint: "write some code",
		Providers:     []string{"openai", "anthropic"},
		Difficulty:    BucketEasy,
		Turn:          0,
		Budget:        4,
		CategoryHint:  "", // no category — must take legacy path.
	}
	act, err := p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if act.Provider != "openai" || act.Model != "gpt-5" {
		t.Fatalf("legacy did not win: got %+v", act)
	}
}

func TestConfigValidateCatalogCategories(t *testing.T) {
	cfg := Default()
	cfg.Enabled = true
	cfg.Catalog = []CatalogEntry{
		{ID: "opus", Provider: "anthropic", Model: "claude-opus-4.8"},
	}
	cfg.Categories = []Category{
		{Name: "code", Prefer: []string{"opus"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	// Reference to unknown catalog id must fail.
	cfg.Categories = []Category{
		{Name: "code", Prefer: []string{"unknown"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for unknown id")
	}

	// Duplicate catalog id must fail.
	cfg.Catalog = []CatalogEntry{
		{ID: "opus", Provider: "anthropic", Model: "claude-opus-4.8"},
		{ID: "opus", Provider: "anthropic", Model: "claude-opus-4.8"},
	}
	cfg.Categories = nil
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for duplicate id")
	}
}

func TestLLMClassifierNormalizeChoice(t *testing.T) {
	cats := []Category{
		{Name: "Code-Review"},
		{Name: "Math"},
		{Name: "Translation"},
	}
	cc := newCategoryClassifier(cats, ClassifierConfig{Kind: "llm"})
	c := newLLMCategoryClassifier(ClassifierLLMConfig{
		Enabled: true,
		Model:   "fake-mini",
	}, cc, nil)

	cases := []struct {
		in   string
		want string
	}{
		{"Math", "Math"},
		{"math", "Math"},
		{"Category: Translation", "Translation"},
		{"code-review", "Code-Review"},
		{`"Math"`, "Math"},
		{"I think this is Math, primarily.", "Math"},
		{"unknown", ""},
	}
	for _, tc := range cases {
		got := c.normalizeChoice(tc.in)
		if got != tc.want {
			t.Errorf("normalize(%q): want %q, got %q", tc.in, tc.want, got)
		}
	}
}

func TestLLMClassifierCallsManagerWithOpenAIPayload(t *testing.T) {
	cats := []Category{
		{Name: "Math", Instructions: "Math problems."},
		{Name: "Code-Review", Instructions: "Code review."},
	}
	cc := newCategoryClassifier(cats, ClassifierConfig{Kind: "llm"})

	var seenProviders []string
	var seenPayload []byte
	mgr := &fakeManager{
		executeHandler: func(providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			seenProviders = append([]string(nil), providers...)
			seenPayload = append([]byte(nil), req.Payload...)
			return coreexecutor.Response{Payload: openAIResponse("Math")}, nil
		},
	}

	llm := newLLMCategoryClassifier(ClassifierLLMConfig{
		Enabled:       true,
		Provider:      "openai",
		Model:         "gpt-5-mini",
		Timeout:       2 * time.Second,
		MaxInputChars: 100,
	}, cc, mgr)

	pick := llm.classify(context.Background(), ciFromText("solve x^2 = 4"), DecideRequest{
		Providers: []string{"openai", "anthropic"},
	})
	if pick != "Math" {
		t.Fatalf("classifier pick: want Math, got %q", pick)
	}
	if len(seenProviders) != 1 || seenProviders[0] != "openai" {
		t.Fatalf("expected classifier to call only openai; got %v", seenProviders)
	}
	// Confirm payload shape: model + messages with the classifier prompt.
	var body map[string]any
	if err := json.Unmarshal(seenPayload, &body); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if body["model"] != "gpt-5-mini" {
		t.Fatalf("payload model: want gpt-5-mini, got %v", body["model"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("payload messages: want >=2, got %d", len(msgs))
	}
}

func TestLLMClassifierFallsBackOnError(t *testing.T) {
	cats := []Category{
		{Name: "math", Prefer: []string{"x"}, Match: CategoryMatch{Keywords: []string{"prove"}}},
		{Name: "default", Prefer: []string{"x"}},
	}

	var callCount int32
	mgr := &fakeManager{
		executeHandler: func(providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			atomic.AddInt32(&callCount, 1)
			return coreexecutor.Response{}, context.DeadlineExceeded
		},
	}
	cfg := Default()
	cfg.Enabled = true
	cfg.EnabledForAPIKeys = []string{"*"}
	cfg.Catalog = []CatalogEntry{
		{ID: "x", Provider: "openai", Model: "gpt-5-mini"},
	}
	cfg.Categories = cats
	cfg.Classifier = ClassifierConfig{
		Kind: "hybrid",
		LLM: ClassifierLLMConfig{
			Enabled:         true,
			Provider:        "openai",
			Model:           "gpt-5-mini",
			Timeout:         10 * time.Millisecond,
			MaxInputChars:   100,
			FallbackOnError: "heuristic",
		},
	}

	o, err := New(cfg, mgr)
	if err != nil || o == nil {
		t.Fatalf("New(): %v", err)
	}

	// In hybrid mode the heuristic runs first — the LLM should not be
	// consulted because "prove" matches the math category.
	d := o.Decide(context.Background(), DecideRequest{
		HandlerType:     "openai",
		Providers:       []string{"openai"},
		NormalizedModel: "auto",
		Payload:         openAIRequest("please prove this theorem"),
		APIKey:          "tester",
	})
	if d.CategoryHint != "math" {
		t.Fatalf("hybrid hit heuristic first: want math, got %q", d.CategoryHint)
	}
	if atomic.LoadInt32(&callCount) != 0 {
		t.Fatalf("hybrid should not call LLM when heuristic matched; calls=%d", callCount)
	}
}
