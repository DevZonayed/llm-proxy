package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// LearnedPolicy is a stub for a future learned coordinator served by an
// out-of-process sidecar over a Unix socket. v0 wires the dial loop, the
// length-prefixed JSON frame protocol, and the fallback policy; training
// is intentionally out of scope.
//
// Wire protocol (each direction is one frame per Decide call):
//   - 4-byte big-endian length, followed by that many bytes of JSON.
//   - Request: {decision_id, turn, providers, difficulty, history, user_hint}
//   - Response: {provider, model, role, halt}
type LearnedPolicy struct {
	cfg      LearnedConfig
	fallback Policy
}

// NewLearnedPolicy constructs a learned policy. The fallback policy is
// consulted whenever the sidecar is unavailable and LearnedConfig
// requests fallback behavior; pass NewRulesPolicy(...) for the default.
func NewLearnedPolicy(cfg LearnedConfig, fallback Policy) *LearnedPolicy {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 50 * time.Millisecond
	}
	if strings.TrimSpace(cfg.FallbackOnError) == "" {
		cfg.FallbackOnError = "rules"
	}
	return &LearnedPolicy{
		cfg:      cfg,
		fallback: fallback,
	}
}

// learnedRequest is the wire representation of a TurnState the sidecar
// receives. It is intentionally minimal — only fields the policy is
// expected to need are sent.
type learnedRequest struct {
	Turn       int           `json:"turn"`
	Providers  []string      `json:"providers"`
	Difficulty string        `json:"difficulty"`
	History    []historyWire `json:"history,omitempty"`
	UserHint   string        `json:"user_hint,omitempty"`
}

type historyWire struct {
	Index    int    `json:"index"`
	Role     string `json:"role"`
	Provider string `json:"provider"`
	Verdict  string `json:"verdict,omitempty"`
}

// learnedResponse is the wire representation of an Action.
type learnedResponse struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Role     string `json:"role"`
	Halt     bool   `json:"halt"`
}

// Decide implements Policy.
func (p *LearnedPolicy) Decide(ctx context.Context, state TurnState) (Action, error) {
	dialCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	action, err := p.decideViaSocket(dialCtx, state)
	if err == nil {
		return action, nil
	}

	if strings.EqualFold(strings.TrimSpace(p.cfg.FallbackOnError), "fail") {
		return Action{}, fmt.Errorf("orchestrator: learned policy unavailable: %w", err)
	}
	if p.fallback == nil {
		return Action{}, fmt.Errorf("orchestrator: learned policy unavailable and no fallback configured: %w", err)
	}
	return p.fallback.Decide(ctx, state)
}

// decideViaSocket performs one round-trip with the sidecar.
func (p *LearnedPolicy) decideViaSocket(ctx context.Context, state TurnState) (Action, error) {
	if strings.TrimSpace(p.cfg.Socket) == "" {
		return Action{}, errors.New("orchestrator: learned policy: socket path not configured")
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", p.cfg.Socket)
	if err != nil {
		return Action{}, fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	req := learnedRequest{
		Turn:       state.Turn,
		Providers:  state.Providers,
		Difficulty: state.Difficulty.String(),
		UserHint:   state.UserModelHint,
	}
	for _, h := range state.History {
		req.History = append(req.History, historyWire{
			Index:    h.Index,
			Role:     h.Role.String(),
			Provider: h.Provider,
			Verdict:  h.Verdict,
		})
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return Action{}, fmt.Errorf("encode: %w", err)
	}
	if err := writeFrame(conn, payload); err != nil {
		return Action{}, fmt.Errorf("write frame: %w", err)
	}

	respBytes, err := readFrame(conn)
	if err != nil {
		return Action{}, fmt.Errorf("read frame: %w", err)
	}

	var resp learnedResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return Action{}, fmt.Errorf("decode: %w", err)
	}

	role, err := parseRole(resp.Role)
	if err != nil {
		return Action{}, err
	}
	return Action{
		Provider: resp.Provider,
		Model:    resp.Model,
		Role:     role,
		Halt:     resp.Halt,
	}, nil
}

func parseRole(s string) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "worker", "w":
		return RoleWorker, nil
	case "thinker", "t":
		return RoleThinker, nil
	case "verifier", "v":
		return RoleVerifier, nil
	}
	return RoleWorker, fmt.Errorf("orchestrator: learned policy returned unknown role %q", s)
}

// writeFrame writes a length-prefixed payload. Big-endian 32-bit length.
func writeFrame(w io.Writer, payload []byte) error {
	const maxLen = 4 << 20 // 4 MiB safety cap
	if len(payload) > maxLen {
		return fmt.Errorf("frame too large: %d bytes", len(payload))
	}
	hdr := []byte{
		byte(len(payload) >> 24),
		byte(len(payload) >> 16),
		byte(len(payload) >> 8),
		byte(len(payload)),
	}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return nil
}

// readFrame reads a single length-prefixed payload. Mirrors writeFrame.
func readFrame(r io.Reader) ([]byte, error) {
	const maxLen = 4 << 20
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	length := (int(hdr[0]) << 24) | (int(hdr[1]) << 16) | (int(hdr[2]) << 8) | int(hdr[3])
	if length < 0 || length > maxLen {
		return nil, fmt.Errorf("invalid frame length: %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
