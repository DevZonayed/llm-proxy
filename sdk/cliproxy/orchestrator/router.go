package orchestrator

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// Caller invokes the master model with a complete OpenAI-format
// /v1/chat/completions JSON payload. The handler layer supplies an
// implementation that delegates to the existing AuthManager.Execute path,
// so the master call reuses the proxy's OAuth pool, alias resolution, and
// retry/cooldown logic.
//
// The returned payload is the raw OpenAI-format chat completion response.
type Caller func(ctx context.Context, model string, rawJSON []byte) ([]byte, error)

// Decision is what the master picked.
type Decision struct {
	Model  string
	Reason string
}

// Router is the request-time decision maker. Construct one at server startup
// and pass it to the HTTP handler layer.
type Router struct {
	cfg    *config.Orchestrator
	caller Caller

	mu       sync.RWMutex
	cfgCache config.Orchestrator // snapshot copy; reloaded on Reload
}

// New constructs a Router. cfg must not be nil. caller must not be nil unless
// the Router will only ever be queried for IsEnabled / ShouldRoute (e.g. tests).
func New(cfg *config.Orchestrator, caller Caller) *Router {
	if cfg == nil {
		cfg = &config.Orchestrator{}
	}
	r := &Router{cfg: cfg, caller: caller}
	r.cfgCache = *cfg
	return r
}

// Reload swaps the live configuration. Safe under concurrent reads.
// Pass nil to fully disable the router.
func (r *Router) Reload(cfg *config.Orchestrator) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cfg == nil {
		r.cfgCache = config.Orchestrator{}
		r.cfg = &r.cfgCache
		return
	}
	r.cfgCache = *cfg
	r.cfg = &r.cfgCache
}

// snapshot returns a copy of the current config safe to read without locks.
func (r *Router) snapshot() config.Orchestrator {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfgCache
}

// IsEnabled reports whether the orchestrator is configured AND has both a
// master model and a router alias set.
func (r *Router) IsEnabled() bool {
	if r == nil {
		return false
	}
	c := r.snapshot()
	return c.Enabled &&
		strings.TrimSpace(c.MasterModel) != "" &&
		strings.TrimSpace(c.RouterAlias) != ""
}

// ShouldRoute returns true when the incoming model name should be replaced
// by a master decision. Caller MUST also ensure r.caller is non-nil before
// invoking Route.
func (r *Router) ShouldRoute(modelName string) bool {
	if !r.IsEnabled() {
		return false
	}
	c := r.snapshot()
	return strings.EqualFold(strings.TrimSpace(modelName), strings.TrimSpace(c.RouterAlias))
}

// Config returns a copy of the live config (for management readback).
func (r *Router) Config() config.Orchestrator {
	if r == nil {
		return config.Orchestrator{}
	}
	return r.snapshot()
}

// Route invokes the master with a routing prompt built from the user's request
// and returns the chosen model. On any failure it returns a Decision with the
// configured Fallback (or MasterModel if Fallback is empty) and a non-nil
// error so the caller can decide whether to log. The returned Model is always
// non-empty as long as the router is enabled.
//
// requestJSON is the raw OpenAI-format chat completion request from the client.
func (r *Router) Route(ctx context.Context, requestJSON []byte) (Decision, error) {
	if r == nil || !r.IsEnabled() {
		return Decision{}, errors.New("orchestrator: disabled")
	}
	c := r.snapshot()
	if r.caller == nil {
		return r.fallback(c, fmt.Errorf("orchestrator: nil caller"))
	}

	start := time.Now()
	timeout := time.Duration(c.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt, err := r.buildRouterPrompt(c, requestJSON)
	if err != nil {
		return r.fallback(c, fmt.Errorf("orchestrator: build prompt: %w", err))
	}

	payload, err := r.caller(rctx, c.MasterModel, prompt)
	if err != nil {
		return r.fallback(c, fmt.Errorf("orchestrator: master call: %w", err))
	}

	decision, err := r.parseDecision(c, payload)
	if err != nil {
		return r.fallback(c, fmt.Errorf("orchestrator: parse decision: %w", err))
	}

	if c.LogDecisions {
		log.WithFields(log.Fields{
			"requested":   c.RouterAlias,
			"picked":      decision.Model,
			"reason":      truncate(decision.Reason, 160),
			"latency_ms":  time.Since(start).Milliseconds(),
			"request_id":  requestHash(requestJSON),
		}).Info("orchestrator routed request")
	}

	return decision, nil
}

// fallback returns the safe Decision used whenever the master path errors.
// The Model is guaranteed non-empty: Fallback first, then MasterModel.
func (r *Router) fallback(c config.Orchestrator, err error) (Decision, error) {
	model := strings.TrimSpace(c.Fallback)
	if model == "" {
		model = strings.TrimSpace(c.MasterModel)
	}
	if err != nil {
		log.WithError(err).WithField("fallback", model).
			Warn("orchestrator: routing failed, using fallback model")
	}
	return Decision{Model: model, Reason: "fallback: " + safeErr(err)}, err
}

// buildRouterPrompt constructs the JSON payload sent to the master model.
// It carries the user's last few messages plus the model menu.
func (r *Router) buildRouterPrompt(c config.Orchestrator, requestJSON []byte) ([]byte, error) {
	userTurns, err := extractUserMessages(requestJSON, 6) // last 6 turns
	if err != nil {
		return nil, err
	}
	userExcerpt := truncate(strings.Join(userTurns, "\n\n---\n\n"), 6000)

	menu := buildModelMenu(c.AllowedModels)

	system := strings.TrimSpace(c.SystemPromptOverride)
	if system == "" {
		system = defaultRouterSystemPrompt
	}

	body := map[string]any{
		"model": c.MasterModel,
		"messages": []map[string]any{
			{"role": "system", "content": system + "\n\nMenu of models you may pick from:\n" + menu},
			{"role": "user", "content": "USER REQUEST EXCERPT:\n" + userExcerpt + "\n\nReturn ONLY a JSON object: {\"model\":\"<one of the menu ids>\",\"reason\":\"<one short sentence>\"}"},
		},
		"temperature":     0,
		"max_tokens":      120,
		"response_format": map[string]string{"type": "json_object"},
		"stream":          false,
	}
	return json.Marshal(body)
}

// parseDecision pulls {model, reason} out of an OpenAI-format chat completion
// response. It tolerates a JSON object wrapped in prose by extracting the first
// {...} block via regex.
func (r *Router) parseDecision(c config.Orchestrator, payload []byte) (Decision, error) {
	var env struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return Decision{}, fmt.Errorf("envelope: %w", err)
	}
	if env.Error != nil {
		return Decision{}, fmt.Errorf("upstream error: %v", env.Error)
	}
	if len(env.Choices) == 0 {
		return Decision{}, fmt.Errorf("no choices in response")
	}
	content := strings.TrimSpace(env.Choices[0].Message.Content)
	if content == "" {
		return Decision{}, fmt.Errorf("empty content")
	}

	objBytes := extractJSONObject(content)
	if objBytes == nil {
		return Decision{}, fmt.Errorf("no JSON object found in content: %s", truncate(content, 200))
	}

	var pick struct {
		Model  string `json:"model"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(objBytes, &pick); err != nil {
		return Decision{}, fmt.Errorf("decision json: %w (raw=%s)", err, truncate(string(objBytes), 200))
	}
	pick.Model = strings.TrimSpace(pick.Model)
	if pick.Model == "" {
		return Decision{}, fmt.Errorf("decision missing model field")
	}
	if !r.isModelAllowed(c, pick.Model) {
		return Decision{}, fmt.Errorf("master picked disallowed model %q", pick.Model)
	}
	return Decision{Model: pick.Model, Reason: pick.Reason}, nil
}

func (r *Router) isModelAllowed(c config.Orchestrator, model string) bool {
	allowed := c.AllowedModels
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(a), model) {
			return true
		}
	}
	return false
}

// --- helpers ---

// jsonObjectRe captures the first balanced-looking JSON object from a string.
// We do a real brace walk below; this regex is only the fast first attempt.
var jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

// extractJSONObject finds the first balanced JSON object in s using a brace
// counter. Returns nil if no object is found.
func extractJSONObject(s string) []byte {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// skip
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1])
			}
		}
	}
	// regex fallback: tolerate cases where escaping confused the walker
	if m := jsonObjectRe.FindString(s); m != "" {
		return []byte(m)
	}
	return nil
}

// extractUserMessages pulls user-role message content from an OpenAI-format
// /v1/chat/completions request. It returns at most `keep` of the most recent
// user turns, oldest first. content may be either a string or an array of
// {type:text,text:...} parts.
func extractUserMessages(requestJSON []byte, keep int) ([]string, error) {
	if keep <= 0 {
		keep = 6
	}
	var env struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requestJSON, &env); err != nil {
		return nil, fmt.Errorf("unmarshal request: %w", err)
	}
	out := make([]string, 0, keep)
	for _, m := range env.Messages {
		if !strings.EqualFold(m.Role, "user") {
			continue
		}
		out = append(out, stringifyContent(m.Content))
	}
	if len(out) > keep {
		out = out[len(out)-keep:]
	}
	return out, nil
}

// stringifyContent renders OpenAI message content into a plain string. Handles
// both the string form and the array-of-parts form. Unknown shapes are
// rendered as their JSON.
func stringifyContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Plain string?
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Array of parts.
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(p.Text)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return string(raw)
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func requestHash(b []byte) string {
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:6])
}

func safeErr(err error) string {
	if err == nil {
		return ""
	}
	return truncate(err.Error(), 160)
}
