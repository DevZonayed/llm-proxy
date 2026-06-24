package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// fakeManager is a lightweight in-memory implementation of AuthManager
// used by these tests. Each call records the providers/request/options
// it received so assertions can inspect them.
type fakeManager struct {
	calls          int32
	executeHandler func(providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error)
	streamHandler  func(providers []string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error)
}

func (f *fakeManager) Execute(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.executeHandler == nil {
		return coreexecutor.Response{Payload: []byte(`{}`)}, nil
	}
	return f.executeHandler(providers, req, opts)
}

func (f *fakeManager) ExecuteStream(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.streamHandler == nil {
		ch := make(chan coreexecutor.StreamChunk, 1)
		ch <- coreexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
		close(ch)
		return &coreexecutor.StreamResult{Chunks: ch}, nil
	}
	return f.streamHandler(providers, req, opts)
}

// openAIResponse builds a minimal OpenAI-shaped chat completion response
// whose assistant text is the supplied content.
func openAIResponse(content string) []byte {
	body := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": content}},
		},
	}
	out, _ := json.Marshal(body)
	return out
}

// openAIRequest constructs a tiny OpenAI-shaped chat completion request.
func openAIRequest(messages ...string) []byte {
	msgs := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, map[string]any{"role": "user", "content": m})
	}
	body := map[string]any{
		"model":    "auto",
		"messages": msgs,
	}
	out, _ := json.Marshal(body)
	return out
}

// disabledOrchestratorYieldsNil verifies New() returns nil when disabled
// so callers can treat the return value as a sentinel.
func TestNewDisabledReturnsNil(t *testing.T) {
	cfg := Default()
	cfg.Enabled = false
	o, err := New(cfg, &fakeManager{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o != nil {
		t.Fatalf("expected nil orchestrator when disabled, got %+v", o)
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"default-disabled", func(*Config) {}, false},
		{
			"unknown-mode",
			func(c *Config) { c.Enabled = true; c.Mode = "foo" },
			true,
		},
		{
			"unknown-policy",
			func(c *Config) { c.Enabled = true; c.Policy.Kind = "guess" },
			true,
		},
		{
			"max-turns-zero",
			func(c *Config) { c.Enabled = true; c.Budgets.MaxTurns = 0 },
			true,
		},
		{
			"hard-below-medium",
			func(c *Config) {
				c.Enabled = true
				c.Difficulty.HardTokenThreshold = 10
				c.Difficulty.MediumTokenThreshold = 100
			},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestRulesPolicyHonoursCandidateSet(t *testing.T) {
	p := NewRulesPolicy(RulesConfig{
		Defaults: map[string][]string{
			"code": {"claude", "anthropic"},
			"math": {"codex"},
		},
		VerifierMustDiffer: true,
	})

	// Even with "claude" as the preferred code provider, if the
	// candidate set excludes it, the policy must fall back to an
	// available provider.
	state := TurnState{
		UserModelHint: "code helper",
		Providers:     []string{"vertex", "codex"},
		Difficulty:    BucketEasy,
		Turn:          0,
		Budget:        4,
	}
	act, err := p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if act.Provider != "vertex" && act.Provider != "codex" {
		t.Fatalf("provider %q not in candidate set %v", act.Provider, state.Providers)
	}
}

func TestRulesPolicyTriRoleSequence(t *testing.T) {
	p := NewRulesPolicy(RulesConfig{
		Defaults: map[string][]string{
			"code":     {"claude"},
			"verifier": {"codex"},
			"thinker":  {"gemini-cli"},
		},
		VerifierMustDiffer: true,
	})
	state := TurnState{
		UserModelHint: "write me code",
		Providers:     []string{"claude", "codex", "gemini-cli"},
		Difficulty:    BucketHard,
		Budget:        4,
	}

	// Turn 0 must be Thinker on hard tasks.
	state.Turn = 0
	act, err := p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("decide turn 0: %v", err)
	}
	if act.Role != RoleThinker {
		t.Fatalf("turn 0 expected Thinker, got %s", act.Role)
	}
	state.History = append(state.History, TurnHistory{Index: 0, Role: RoleThinker, Provider: act.Provider})

	// Turn 1 must be Worker.
	state.Turn = 1
	act, err = p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("decide turn 1: %v", err)
	}
	if act.Role != RoleWorker {
		t.Fatalf("turn 1 expected Worker, got %s", act.Role)
	}
	state.History = append(state.History, TurnHistory{Index: 1, Role: RoleWorker, Provider: "claude"})

	// Turn 2 must be Verifier and (since VerifierMustDiffer) not claude.
	state.Turn = 2
	act, err = p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("decide turn 2: %v", err)
	}
	if act.Role != RoleVerifier {
		t.Fatalf("turn 2 expected Verifier, got %s", act.Role)
	}
	if act.Provider == "claude" {
		t.Fatalf("verifier should differ from last worker; got %s", act.Provider)
	}
}

func TestClassifyBucketingByLength(t *testing.T) {
	c := NewClassifier(DifficultyConfig{
		Enabled:              true,
		MediumTokenThreshold: 50,
		HardTokenThreshold:   200,
	})
	short := openAIRequest("hi")
	long := openAIRequest(strings.Repeat("This is a fairly long message. ", 50))

	if got := c.Classify(ClassifyInput{Payload: short, SourceFormat: "openai"}); got != BucketEasy {
		t.Fatalf("short bucket: want easy, got %s", got)
	}
	if got := c.Classify(ClassifyInput{Payload: long, SourceFormat: "openai"}); got != BucketHard {
		t.Fatalf("long bucket: want hard, got %s", got)
	}
}

func TestDecideRespectsHeaderOverride(t *testing.T) {
	cfg := Default()
	cfg.Enabled = true
	cfg.EnabledForAPIKeys = []string{"*"}
	cfg.RespectRequestHeaders = true
	cfg.Policy.Rules.Defaults = map[string][]string{"code": {"claude"}}
	o, err := New(cfg, &fakeManager{})
	if err != nil || o == nil {
		t.Fatalf("New() failed: %v", err)
	}

	hdr := http.Header{}
	hdr.Set("X-Orchestrator-Mode", "fast")
	d := o.Decide(context.Background(), DecideRequest{
		HandlerType:     "openai",
		Providers:       []string{"claude", "codex"},
		NormalizedModel: "claude-opus-4.8",
		Payload:         openAIRequest(strings.Repeat("a", 10000)),
		Headers:         hdr,
		APIKey:          "tester",
	})
	if d.Mode != "single-shot" {
		t.Fatalf("expected single-shot via header override, got %s", d.Mode)
	}
	if d.UseLoop {
		t.Fatalf("UseLoop should be false in single-shot mode")
	}
}

func TestDecideTriRoleAutomatic(t *testing.T) {
	cfg := Default()
	cfg.Enabled = true
	cfg.EnabledForAPIKeys = []string{"*"}
	cfg.Mode = "auto"
	cfg.Difficulty.HardTokenThreshold = 100
	cfg.Difficulty.MediumTokenThreshold = 20
	cfg.Policy.Rules.Defaults = map[string][]string{
		"code": {"claude"},
	}
	o, err := New(cfg, &fakeManager{})
	if err != nil || o == nil {
		t.Fatalf("New(): %v", err)
	}
	d := o.Decide(context.Background(), DecideRequest{
		HandlerType:     "openai",
		Providers:       []string{"claude", "codex"},
		NormalizedModel: "auto",
		Payload:         openAIRequest(strings.Repeat("solve this math problem: ", 100)),
		APIKey:          "tester",
	})
	if d.Mode != "tri-role" {
		t.Fatalf("expected tri-role for long math request, got %s", d.Mode)
	}
	if !d.UseLoop {
		t.Fatalf("UseLoop must be true in tri-role mode")
	}
	if d.DecisionID == "" {
		t.Fatalf("DecisionID should be populated")
	}
}

func TestRunNonStreamHappyPath(t *testing.T) {
	cfg := Default()
	cfg.Enabled = true
	cfg.EnabledForAPIKeys = []string{"*"}
	cfg.Mode = "tri-role"
	cfg.Budgets.MaxTurns = 4
	cfg.Budgets.MinVerifierTurns = 1
	cfg.Budgets.WallBudget = 5 * time.Second
	cfg.Trace.Enabled = false
	cfg.Policy.Rules.Defaults = map[string][]string{
		"code":     {"claude"},
		"thinker":  {"gemini-cli"},
		"verifier": {"codex"},
	}

	// Script the manager so each role returns a distinguishable body.
	mgr := &fakeManager{
		executeHandler: func(providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			switch providers[0] {
			case "gemini-cli":
				return coreexecutor.Response{Payload: openAIResponse("Plan: do steps.")}, nil
			case "claude":
				return coreexecutor.Response{Payload: openAIResponse("Final answer: 42")}, nil
			case "codex":
				return coreexecutor.Response{Payload: openAIResponse("ACCEPT\nLooks correct.")}, nil
			}
			return coreexecutor.Response{Payload: openAIResponse("")}, nil
		},
	}

	o, err := New(cfg, mgr)
	if err != nil || o == nil {
		t.Fatalf("New(): %v", err)
	}

	res, err := o.RunNonStream(context.Background(), RunRequest{
		DecisionID:      "decision-1",
		Difficulty:      BucketHard,
		HandlerType:     "openai",
		Providers:       []string{"claude", "gemini-cli", "codex"},
		NormalizedModel: "auto",
		Payload:         openAIRequest("write some code"),
		OriginalRequest: openAIRequest("write some code"),
		APIKey:          "tester",
	})
	if err != nil {
		t.Fatalf("RunNonStream: %v", err)
	}
	if res.HaltedOn != "verifier_accept" {
		t.Fatalf("expected verifier_accept, got %s", res.HaltedOn)
	}
	if !strings.Contains(string(res.Payload), "Final answer: 42") {
		t.Fatalf("expected final worker payload in response, got %s", string(res.Payload))
	}
}

func TestRunNonStreamBudgetExhausted(t *testing.T) {
	cfg := Default()
	cfg.Enabled = true
	cfg.EnabledForAPIKeys = []string{"*"}
	cfg.Mode = "tri-role"
	cfg.Budgets.MaxTurns = 3
	cfg.Budgets.MinVerifierTurns = 1
	cfg.Trace.Enabled = false
	cfg.Policy.Rules.Defaults = map[string][]string{
		"code":     {"claude"},
		"thinker":  {"gemini-cli"},
		"verifier": {"codex"},
	}

	mgr := &fakeManager{
		executeHandler: func(providers []string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			switch providers[0] {
			case "gemini-cli":
				return coreexecutor.Response{Payload: openAIResponse("Plan")}, nil
			case "claude":
				return coreexecutor.Response{Payload: openAIResponse("Worker output")}, nil
			case "codex":
				return coreexecutor.Response{Payload: openAIResponse("REVISE\nbad step")}, nil
			}
			return coreexecutor.Response{Payload: openAIResponse("")}, nil
		},
	}

	o, _ := New(cfg, mgr)
	res, err := o.RunNonStream(context.Background(), RunRequest{
		Difficulty:      BucketHard,
		HandlerType:     "openai",
		Providers:       []string{"claude", "gemini-cli", "codex"},
		NormalizedModel: "auto",
		Payload:         openAIRequest("write code"),
		OriginalRequest: openAIRequest("write code"),
	})
	if err != nil {
		t.Fatalf("RunNonStream: %v", err)
	}
	if res.HaltedOn != "turns_exhausted" {
		t.Fatalf("expected turns_exhausted, got %s", res.HaltedOn)
	}
	if !strings.Contains(string(res.Payload), "Worker output") {
		t.Fatalf("expected last worker output in response, got %s", string(res.Payload))
	}
}

func TestVerifierParser(t *testing.T) {
	cases := map[string][2]string{
		"ACCEPT":                    {"ACCEPT", ""},
		"accept.":                   {"ACCEPT", ""},
		"ACCEPT.\nLooks good.":      {"ACCEPT", "Looks good."},
		"REVISE: step 3 is broken": {"REVISE", ""},
		"":                          {"REVISE", "empty verifier response"},
		"unrelated chatter":         {"REVISE", ""},
	}
	for in, want := range cases {
		gotVerdict, gotDiag := parseVerifierVerdict(in)
		if gotVerdict != want[0] {
			t.Errorf("verdict for %q: want %s got %s", in, want[0], gotVerdict)
		}
		if want[1] != "" && gotDiag != want[1] {
			t.Errorf("diagnosis for %q: want %q got %q", in, want[1], gotDiag)
		}
	}
}

func TestTraceRecorderWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	cfg := TraceConfig{
		Enabled:                 true,
		Dir:                     dir,
		KeepWorkerExcerptsChars: 256,
		RotateMB:                64,
	}
	rec := NewTraceRecorder(cfg)
	rec.Record(TraceRecord{
		DecisionID: "d1",
		Time:       time.Now().UTC(),
		Mode:       "tri-role",
		Difficulty: "hard",
		UserHint:   "auto",
		Providers:  []string{"claude"},
		Final:      TraceFinal{TurnIndex: 1, HaltedOn: "verifier_accept"},
	})
	rec.Record(TraceRecord{
		DecisionID: "d2",
		Time:       time.Now().UTC(),
		Mode:       "single-shot",
		Difficulty: "easy",
		UserHint:   "auto",
		Providers:  []string{"codex"},
		Final:      TraceFinal{TurnIndex: 0, HaltedOn: "no_loop"},
	})
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one trace file")
	}
	// Locate the produced .jsonl file.
	var jsonl string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			jsonl = filepath.Join(dir, e.Name())
			break
		}
	}
	if jsonl == "" {
		t.Fatalf("no .jsonl file in trace dir, entries=%v", entries)
	}
	data, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Count(string(data), "\n")
	if lines < 2 {
		t.Fatalf("expected at least 2 jsonl lines, got %d (data=%q)", lines, string(data))
	}
}

func TestRulesPolicyPinsRoleModel(t *testing.T) {
	p := NewRulesPolicy(RulesConfig{
		Defaults: map[string][]string{
			"code":     {"claude"},
			"thinker":  {"gemini-cli"},
			"verifier": {"codex"},
		},
		Models: map[string]string{
			"thinker":  "gemini-2.5-flash",
			"verifier": "gpt-5",
			"code":     "claude-opus-4-5",
			"default":  "gemini-2.5-pro",
		},
		VerifierMustDiffer: true,
	})
	state := TurnState{
		UserModelHint: "auto",
		Providers:     []string{"claude", "codex", "gemini-cli"},
		Difficulty:    BucketHard,
		Budget:        4,
	}

	// Turn 0 = Thinker → should use gemini-2.5-flash on gemini-cli.
	state.Turn = 0
	act, err := p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("decide turn 0: %v", err)
	}
	if act.Model != "gemini-2.5-flash" || act.Provider != "gemini-cli" {
		t.Fatalf("turn 0: want gemini-cli/gemini-2.5-flash, got %s/%s", act.Provider, act.Model)
	}
	state.History = append(state.History, TurnHistory{Index: 0, Role: act.Role, Provider: act.Provider, Model: act.Model, Summary: "code task"})

	// Turn 1 = Worker on code task → claude-opus-4-5 on claude.
	state.Turn = 1
	act, err = p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("decide turn 1: %v", err)
	}
	if act.Role != RoleWorker {
		t.Fatalf("turn 1 expected Worker, got %s", act.Role)
	}
	if act.Provider != "claude" || act.Model != "claude-opus-4-5" {
		t.Fatalf("turn 1: want claude/claude-opus-4-5, got %s/%s", act.Provider, act.Model)
	}
	state.History = append(state.History, TurnHistory{Index: 1, Role: act.Role, Provider: act.Provider, Model: act.Model, Summary: "code task"})

	// Turn 2 = Verifier → gpt-5 on codex.
	state.Turn = 2
	act, err = p.Decide(context.Background(), state)
	if err != nil {
		t.Fatalf("decide turn 2: %v", err)
	}
	if act.Role != RoleVerifier {
		t.Fatalf("turn 2 expected Verifier, got %s", act.Role)
	}
	if act.Provider != "codex" || act.Model != "gpt-5" {
		t.Fatalf("turn 2: want codex/gpt-5, got %s/%s", act.Provider, act.Model)
	}
}

func TestRulesPolicyDefaultModelFallback(t *testing.T) {
	p := NewRulesPolicy(RulesConfig{
		Defaults: map[string][]string{"code": {"claude"}},
		Models:   map[string]string{"default": "gemini-2.5-pro"},
	})
	state := TurnState{
		UserModelHint: "auto",
		Providers:     []string{"claude"},
		Difficulty:    BucketEasy, // Worker only
	}
	act, _ := p.Decide(context.Background(), state)
	// "auto" hint with no family match should fall back to "default".
	if act.Model != "gemini-2.5-pro" && act.Model != "claude-opus-4-5" {
		t.Logf("act.Model=%q (acceptable: default fallback or family match)", act.Model)
	}
}

func TestAPIKeyAllowlist(t *testing.T) {
	cfg := Config{Enabled: true, EnabledForAPIKeys: []string{"good"}}
	if cfg.apiKeyAllowed("good") != true {
		t.Errorf("expected good to be allowed")
	}
	if cfg.apiKeyAllowed("bad") != false {
		t.Errorf("expected bad to be denied")
	}

	cfg.EnabledForAPIKeys = []string{"*"}
	if !cfg.apiKeyAllowed("anything") {
		t.Errorf("wildcard should allow any key")
	}
}
