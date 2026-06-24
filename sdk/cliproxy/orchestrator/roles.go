package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// workerOutput captures the result of a Worker turn so the loop can
// either re-issue the call as a stream (Option A) or surface it as the
// final response (non-streaming endpoints).
type workerOutput struct {
	// RawPayload is the upstream response body, in the inbound schema
	// after translation.
	RawPayload []byte
	// Headers are upstream response headers captured during the call.
	Headers http.Header
	// RequestPayload is the request payload that produced RawPayload —
	// used by stream replay so the re-issued call sees the same input.
	RequestPayload []byte
	// Provider is the provider key the call landed on.
	Provider string
	// Model is the upstream model name used.
	Model string
	// Excerpt is a short summary of the assistant message, used in trace
	// records.
	Excerpt string
}

// runThinker runs a Thinker turn. Returns the planner's text summary so
// it can be folded into subsequent Worker turns.
func (o *Orchestrator) runThinker(ctx context.Context, req RunRequest, action Action, _ TurnState, rec *TraceRecord, turn int) (string, error) {
	payload, err := buildThinkerPayload(req.Payload)
	if err != nil {
		return "", err
	}

	start := time.Now()
	resp, err := o.execute(ctx, req, action, payload, false)
	latency := time.Since(start)

	summary := ""
	if err == nil {
		summary = extractAssistantText(resp.Payload)
	}

	rec.Turns = append(rec.Turns, TraceTurn{
		Index:     turn,
		Role:      action.Role.String(),
		Provider:  action.Provider,
		Model:     action.Model,
		Summary:   excerpt(summary, o.cfg.Trace.KeepWorkerExcerptsChars),
		LatencyMS: latency.Milliseconds(),
		Error:     errString(err),
	})
	if err != nil {
		return "", err
	}
	return summary, nil
}

// runWorker runs a Worker turn. The Worker uses the original user payload
// optionally augmented with prior Thinker plans as additional system
// context. The full assistant payload is captured so the loop can either
// replay it as a stream or return it directly.
func (o *Orchestrator) runWorker(ctx context.Context, req RunRequest, action Action, state TurnState, rec *TraceRecord, turn int) (*workerOutput, error) {
	plan := concatThinkerPlans(state.History)
	payload := req.Payload
	if plan != "" {
		augmented, err := appendSystemMessage(req.Payload, "Prior plan:\n"+plan)
		if err == nil {
			payload = augmented
		}
	}

	start := time.Now()
	resp, err := o.execute(ctx, req, action, payload, false)
	latency := time.Since(start)

	tokensIn, tokensOut := extractTokenUsage(resp.Payload)
	summary := ""
	if err == nil {
		summary = extractAssistantText(resp.Payload)
	}
	rec.Turns = append(rec.Turns, TraceTurn{
		Index:     turn,
		Role:      action.Role.String(),
		Provider:  action.Provider,
		Model:     action.Model,
		Summary:   excerpt(summary, o.cfg.Trace.KeepWorkerExcerptsChars),
		LatencyMS: latency.Milliseconds(),
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Error:     errString(err),
	})
	if err != nil {
		return nil, err
	}

	return &workerOutput{
		RawPayload:     resp.Payload,
		Headers:        cloneHTTPHeader(resp.Headers),
		RequestPayload: payload,
		Provider:       action.Provider,
		Model:          action.Model,
		Excerpt:        excerpt(summary, o.cfg.Trace.KeepWorkerExcerptsChars),
	}, nil
}

// runVerifier runs a Verifier turn. Returns the verdict ("ACCEPT" or
// "REVISE") and an optional diagnosis string.
func (o *Orchestrator) runVerifier(ctx context.Context, req RunRequest, action Action, _ TurnState, worker *workerOutput, rec *TraceRecord, turn int) (string, string, error) {
	if worker == nil {
		return "REVISE", "no worker output to verify", nil
	}

	candidate := extractAssistantText(worker.RawPayload)
	payload, err := buildVerifierPayload(req.Payload, candidate)
	if err != nil {
		return "REVISE", err.Error(), err
	}

	start := time.Now()
	resp, err := o.execute(ctx, req, action, payload, false)
	latency := time.Since(start)

	rawText := ""
	if err == nil {
		rawText = extractAssistantText(resp.Payload)
	}
	verdict, diag := parseVerifierVerdict(rawText)

	rec.Turns = append(rec.Turns, TraceTurn{
		Index:     turn,
		Role:      action.Role.String(),
		Provider:  action.Provider,
		Model:     action.Model,
		Summary:   excerpt(rawText, o.cfg.Trace.KeepWorkerExcerptsChars),
		Verdict:   verdict,
		Diagnosis: excerpt(diag, o.cfg.Trace.KeepWorkerExcerptsChars),
		LatencyMS: latency.Milliseconds(),
		Error:     errString(err),
	})
	if err != nil {
		return verdict, diag, err
	}
	return verdict, diag, nil
}

// execute submits a single non-streaming call via the auth manager.
func (o *Orchestrator) execute(ctx context.Context, req RunRequest, action Action, payload []byte, _ bool) (coreexecutor.Response, error) {
	execReq := coreexecutor.Request{
		Model:   action.Model,
		Payload: payload,
	}
	opts := coreexecutor.Options{
		Stream:          false,
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
	return o.mgr.Execute(ctx, []string{action.Provider}, execReq, opts)
}

// thinkerSystemPrompt is prepended to a request to elicit a short plan.
const thinkerSystemPrompt = `You are the planning role of a multi-agent system. ` +
	`Read the user's task and emit a short, numbered plan (3-7 steps) ` +
	`describing how to solve it. Do not solve the task itself; only ` +
	`output the plan.`

// verifierSystemPrompt asks the verifier to evaluate a candidate answer.
const verifierSystemPrompt = `You are the verification role of a multi-agent ` +
	`system. Read the user's task and a candidate answer. Reply with ` +
	`exactly "ACCEPT" or "REVISE" on the first line, then optionally one ` +
	`short line of diagnosis. Do not produce a new answer.`

// buildThinkerPayload constructs the Thinker request payload from the
// original user payload (OpenAI shape). The result keeps the user's
// messages but prepends a planner system message and removes any tools.
func buildThinkerPayload(orig []byte) ([]byte, error) {
	if len(orig) == 0 {
		return nil, fmt.Errorf("orchestrator: empty payload")
	}
	out := orig
	out, _ = sjson.DeleteBytes(out, "tools")
	out, _ = sjson.DeleteBytes(out, "functions")
	out, _ = sjson.DeleteBytes(out, "stream")
	out, err := sjson.SetBytes(out, "stream", false)
	if err != nil {
		return nil, err
	}

	systemMsg := map[string]any{"role": "system", "content": thinkerSystemPrompt}
	out, err = prependMessage(out, systemMsg)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// buildVerifierPayload constructs a Verifier prompt. We replace messages
// entirely with [system, user] where user carries the task and candidate
// to keep the prompt short.
func buildVerifierPayload(orig []byte, candidate string) ([]byte, error) {
	if len(orig) == 0 {
		return nil, fmt.Errorf("orchestrator: empty payload")
	}
	originalTask := extractFirstUserText(orig)
	user := fmt.Sprintf("Task:\n%s\n\nCandidate answer:\n%s", originalTask, candidate)

	messages := []map[string]any{
		{"role": "system", "content": verifierSystemPrompt},
		{"role": "user", "content": user},
	}

	out, err := sjson.SetBytes(orig, "messages", messages)
	if err != nil {
		return nil, err
	}
	out, _ = sjson.DeleteBytes(out, "tools")
	out, _ = sjson.DeleteBytes(out, "functions")
	out, err = sjson.SetBytes(out, "stream", false)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// prependMessage adds the supplied message at index 0 of the messages
// array. It tolerates a missing array by creating one.
func prependMessage(payload []byte, msg map[string]any) ([]byte, error) {
	existing := gjson.GetBytes(payload, "messages")
	var msgs []map[string]any
	msgs = append(msgs, msg)
	if existing.Exists() && existing.IsArray() {
		existing.ForEach(func(_, v gjson.Result) bool {
			var m map[string]any
			if err := json.Unmarshal([]byte(v.Raw), &m); err == nil {
				msgs = append(msgs, m)
			}
			return true
		})
	}
	return sjson.SetBytes(payload, "messages", msgs)
}

// appendSystemMessage adds a system message at the end of the messages
// array. Used to fold Thinker plans into Worker turns.
func appendSystemMessage(payload []byte, content string) ([]byte, error) {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload, fmt.Errorf("orchestrator: payload has no messages array")
	}
	idx := messages.Array()
	return sjson.SetBytes(payload, fmt.Sprintf("messages.%d", len(idx)), map[string]any{
		"role":    "system",
		"content": content,
	})
}

// extractAssistantText pulls the assistant message text from a typical
// OpenAI-shaped response payload. Returns empty string if the field
// isn't present (e.g., translator responded with a Claude shape).
func extractAssistantText(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if v := gjson.GetBytes(payload, "choices.0.message.content"); v.Exists() {
		// OpenAI shape — content may be a string or an array of parts.
		if v.IsArray() {
			var sb strings.Builder
			v.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text"); text.Exists() {
					sb.WriteString(text.String())
				}
				return true
			})
			return sb.String()
		}
		return v.String()
	}
	if v := gjson.GetBytes(payload, "content.0.text"); v.Exists() {
		// Claude shape.
		return v.String()
	}
	if v := gjson.GetBytes(payload, "candidates.0.content.parts.0.text"); v.Exists() {
		// Gemini shape.
		return v.String()
	}
	return ""
}

// extractFirstUserText returns the text of the first user-role message
// in an OpenAI-shaped payload.
func extractFirstUserText(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	first := ""
	gjson.GetBytes(payload, "messages").ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() != "user" {
			return true
		}
		c := m.Get("content")
		if c.IsArray() {
			c.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text"); text.Exists() {
					first = text.String()
					return false
				}
				return true
			})
		} else {
			first = c.String()
		}
		return false
	})
	return first
}

// concatThinkerPlans returns a newline-joined concatenation of Thinker
// turn summaries in chronological order.
func concatThinkerPlans(history []TurnHistory) string {
	var parts []string
	for _, t := range history {
		if t.Role == RoleThinker && strings.TrimSpace(t.Summary) != "" {
			parts = append(parts, t.Summary)
		}
	}
	return strings.Join(parts, "\n\n")
}

// extractTokenUsage pulls token counts from a response payload when
// available. Returns zero values when fields are missing.
func extractTokenUsage(payload []byte) (in int, out int) {
	if len(payload) == 0 {
		return 0, 0
	}
	in = int(gjson.GetBytes(payload, "usage.prompt_tokens").Int())
	if in == 0 {
		in = int(gjson.GetBytes(payload, "usage.input_tokens").Int())
	}
	if in == 0 {
		in = int(gjson.GetBytes(payload, "usageMetadata.promptTokenCount").Int())
	}
	out = int(gjson.GetBytes(payload, "usage.completion_tokens").Int())
	if out == 0 {
		out = int(gjson.GetBytes(payload, "usage.output_tokens").Int())
	}
	if out == 0 {
		out = int(gjson.GetBytes(payload, "usageMetadata.candidatesTokenCount").Int())
	}
	return in, out
}

// parseVerifierVerdict parses the verifier's text reply. The contract is
// "ACCEPT" or "REVISE" on the first line followed by an optional
// diagnosis on subsequent lines. We are lenient about whitespace.
func parseVerifierVerdict(text string) (verdict string, diagnosis string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "REVISE", "empty verifier response"
	}
	lines := strings.SplitN(trimmed, "\n", 2)
	head := strings.ToUpper(strings.TrimSpace(lines[0]))
	// Tolerate punctuation following the verdict word.
	head = strings.TrimFunc(head, func(r rune) bool {
		return r == '.' || r == ':' || r == ',' || r == '!' || r == ' '
	})
	verdict = "REVISE"
	if strings.HasPrefix(head, "ACCEPT") {
		verdict = "ACCEPT"
	} else if strings.HasPrefix(head, "REVISE") {
		verdict = "REVISE"
	}
	if len(lines) > 1 {
		diagnosis = strings.TrimSpace(lines[1])
	}
	return verdict, diagnosis
}

// excerpt truncates text to the requested character count. A non-positive
// limit returns the input unchanged.
func excerpt(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// errString safely renders an error for trace persistence.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// hashAPIKey returns a short hex SHA-256 prefix for the supplied API
// key, suitable for traces that should not store the key in plaintext.
func hashAPIKey(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:8])
}

// cloneHTTPHeader returns a defensive copy of an http.Header value. We
// keep this in the orchestrator package to avoid leaking workerOutput
// implementation details across package boundaries.
func cloneHTTPHeader(in http.Header) http.Header {
	if in == nil {
		return nil
	}
	out := make(http.Header, len(in))
	for k, v := range in {
		copied := make([]string, len(v))
		copy(copied, v)
		out[k] = copied
	}
	return out
}
