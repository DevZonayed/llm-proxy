package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// fakeCaller lets a test pretend to be the master model.
type fakeCaller struct {
	model       string
	reason      string
	wrapInProse bool
	err         error
	delay       time.Duration
	called      int
	lastModel   string
	lastBody    []byte
}

func (f *fakeCaller) call(ctx context.Context, model string, raw []byte) ([]byte, error) {
	f.called++
	f.lastModel = model
	f.lastBody = raw
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	content := fmt.Sprintf(`{"model":%q,"reason":%q}`, f.model, f.reason)
	if f.wrapInProse {
		content = "Sure, here is the pick:\n```json\n" + content + "\n```\nhope that helps!"
	}
	body := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": content}},
		},
	}
	return json.Marshal(body)
}

func newCfg(enabled bool) *config.Orchestrator {
	return &config.Orchestrator{
		Enabled:     enabled,
		MasterModel: "test-master",
		RouterAlias: "master",
		Fallback:    "fallback-model",
		TimeoutMs:   500,
	}
}

func TestShouldRoute(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Orchestrator
		model   string
		want    bool
		enabled bool
	}{
		{"disabled returns false", &config.Orchestrator{Enabled: false, MasterModel: "x", RouterAlias: "master"}, "master", false, false},
		{"empty master model disables", &config.Orchestrator{Enabled: true, MasterModel: "", RouterAlias: "master"}, "master", false, false},
		{"empty alias disables", &config.Orchestrator{Enabled: true, MasterModel: "x", RouterAlias: ""}, "master", false, false},
		{"alias matches", newCfg(true), "master", true, true},
		{"alias matches case-insensitive", newCfg(true), "MASTER", true, true},
		{"alias matches with spaces", newCfg(true), "  master  ", true, true},
		{"non-alias model returns false", newCfg(true), "gpt-5.5", false, true},
		{"empty model returns false", newCfg(true), "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.cfg, nil)
			if got := r.IsEnabled(); got != tt.enabled {
				t.Fatalf("IsEnabled: got %v want %v", got, tt.enabled)
			}
			if got := r.ShouldRoute(tt.model); got != tt.want {
				t.Fatalf("ShouldRoute(%q): got %v want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestRoute_HappyPath(t *testing.T) {
	caller := &fakeCaller{model: "gpt-5.3-codex-spark", reason: "code edit"}
	r := New(newCfg(true), caller.call)
	req := []byte(`{"model":"master","messages":[{"role":"user","content":"fix this bug"}]}`)

	dec, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("Route err: %v", err)
	}
	if dec.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("Model: got %q want gpt-5.3-codex-spark", dec.Model)
	}
	if dec.Reason != "code edit" {
		t.Fatalf("Reason: got %q want %q", dec.Reason, "code edit")
	}
	if caller.called != 1 {
		t.Fatalf("caller invocations: got %d want 1", caller.called)
	}
	if caller.lastModel != "test-master" {
		t.Fatalf("master model: got %q want test-master", caller.lastModel)
	}
	// Verify the body sent to the master mentions the user request.
	var body map[string]any
	if err := json.Unmarshal(caller.lastBody, &body); err != nil {
		t.Fatalf("master body invalid JSON: %v", err)
	}
	if model, _ := body["model"].(string); model != "test-master" {
		t.Fatalf("master body model: got %v", body["model"])
	}
	if rf, _ := body["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Fatalf("response_format not json_object: %v", body["response_format"])
	}
}

func TestRoute_DecisionWrappedInProse(t *testing.T) {
	caller := &fakeCaller{model: "claude-opus-4-8", reason: "reasoning", wrapInProse: true}
	r := New(newCfg(true), caller.call)
	dec, err := r.Route(context.Background(), []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("Route err: %v", err)
	}
	if dec.Model != "claude-opus-4-8" {
		t.Fatalf("Model: got %q", dec.Model)
	}
}

func TestRoute_AllowedModelsWhitelist(t *testing.T) {
	cfg := newCfg(true)
	cfg.AllowedModels = []string{"gpt-5.5", "gemini-3-flash"}
	caller := &fakeCaller{model: "claude-opus-4-8", reason: "thinking"}
	r := New(cfg, caller.call)

	dec, err := r.Route(context.Background(), []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err == nil {
		t.Fatalf("expected error for disallowed model, got dec=%+v", dec)
	}
	if dec.Model != "fallback-model" {
		t.Fatalf("fallback model: got %q want fallback-model", dec.Model)
	}
}

func TestRoute_FallbackOnCallerError(t *testing.T) {
	caller := &fakeCaller{err: errors.New("upstream 500")}
	r := New(newCfg(true), caller.call)
	dec, err := r.Route(context.Background(), []byte(`{"messages":[]}`))
	if err == nil {
		t.Fatalf("expected error from caller")
	}
	if dec.Model != "fallback-model" {
		t.Fatalf("fallback model: got %q", dec.Model)
	}
}

func TestRoute_FallbackToMasterWhenFallbackEmpty(t *testing.T) {
	cfg := newCfg(true)
	cfg.Fallback = ""
	caller := &fakeCaller{err: errors.New("boom")}
	r := New(cfg, caller.call)
	dec, _ := r.Route(context.Background(), []byte(`{"messages":[]}`))
	if dec.Model != "test-master" {
		t.Fatalf("fallback to master: got %q want test-master", dec.Model)
	}
}

func TestRoute_TimeoutEnforced(t *testing.T) {
	caller := &fakeCaller{delay: 200 * time.Millisecond, model: "x", reason: "y"}
	cfg := newCfg(true)
	cfg.TimeoutMs = 20
	r := New(cfg, caller.call)
	dec, err := r.Route(context.Background(), []byte(`{"messages":[]}`))
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if dec.Model != "fallback-model" {
		t.Fatalf("Model: got %q", dec.Model)
	}
}

func TestRoute_DisabledReturnsError(t *testing.T) {
	r := New(newCfg(false), nil)
	_, err := r.Route(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatalf("expected error when disabled")
	}
}

func TestReloadSwapsConfig(t *testing.T) {
	caller := &fakeCaller{model: "gpt-5.5", reason: "general"}
	r := New(newCfg(true), caller.call)
	if !r.ShouldRoute("master") {
		t.Fatalf("ShouldRoute should be true initially")
	}
	// Disable via reload.
	r.Reload(&config.Orchestrator{Enabled: false})
	if r.ShouldRoute("master") {
		t.Fatalf("ShouldRoute should be false after disabling reload")
	}
	// Re-enable with a different alias.
	r.Reload(&config.Orchestrator{Enabled: true, MasterModel: "m", RouterAlias: "captain"})
	if r.ShouldRoute("master") {
		t.Fatalf("old alias must not match anymore")
	}
	if !r.ShouldRoute("captain") {
		t.Fatalf("new alias must match")
	}
	// Nil reload disables.
	r.Reload(nil)
	if r.IsEnabled() {
		t.Fatalf("nil reload should disable")
	}
}

func TestExtractUserMessages_StringContent(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"world"}]}`)
	got, err := extractUserMessages(raw, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Fatalf("got %v", got)
	}
}

func TestExtractUserMessages_PartArrayContent(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"alpha"},{"type":"text","text":"beta"}]}]}`)
	got, err := extractUserMessages(raw, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "alpha") || !strings.Contains(got[0], "beta") {
		t.Fatalf("got %v", got)
	}
}

func TestExtractJSONObject_BalancedBraces(t *testing.T) {
	cases := map[string]string{
		"plain":       `{"a":1}`,
		"wrapped":     "noise {\"a\":1} more noise",
		"with-braces": `{"a":"{not}"}`,
		"nested":      `{"a":{"b":2}}`,
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			b := extractJSONObject(s)
			if b == nil {
				t.Fatalf("nil object")
			}
			var v any
			if err := json.Unmarshal(b, &v); err != nil {
				t.Fatalf("not valid JSON: %s err=%v", b, err)
			}
		})
	}
}

func TestBuildModelMenu_EmptyAllowedUsesCatalog(t *testing.T) {
	menu := buildModelMenu(nil)
	if !strings.Contains(menu, "gpt-5.5") {
		t.Fatalf("default menu missing gpt-5.5: %s", menu)
	}
	if !strings.Contains(menu, "claude-opus-4-8") {
		t.Fatalf("default menu missing claude-opus-4-8")
	}
}

func TestBuildModelMenu_AllowedRestricts(t *testing.T) {
	menu := buildModelMenu([]string{"gpt-5.5", "gemini-3-flash"})
	if !strings.Contains(menu, "gpt-5.5") || !strings.Contains(menu, "gemini-3-flash") {
		t.Fatalf("missing allowed entries: %s", menu)
	}
	if strings.Contains(menu, "claude-opus-4-8") {
		t.Fatalf("allowed-list ignored: %s", menu)
	}
}

func TestNilRouterIsSafe(t *testing.T) {
	var r *Router
	if r.IsEnabled() {
		t.Fatal("nil router must be disabled")
	}
	if r.ShouldRoute("master") {
		t.Fatal("nil router must not route")
	}
	_, err := r.Route(context.Background(), nil)
	if err == nil {
		t.Fatal("nil router must error on Route")
	}
}
