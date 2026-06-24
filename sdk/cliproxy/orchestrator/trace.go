package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TraceRecorder persists per-request orchestrator decisions. The on-disk
// format is one JSON object per line; files roll over when they exceed
// the configured size.
//
// Recorder is the v0 reward-signal foundation. A future learned policy
// trainer consumes these files offline to fit a head against episode
// returns.
type TraceRecorder struct {
	cfg TraceConfig

	mu    sync.Mutex
	file  *os.File
	bytes int64
}

// NewTraceRecorder constructs a recorder. When Enabled is false the
// returned recorder is a no-op: all methods are safe to call but write
// nothing.
func NewTraceRecorder(cfg TraceConfig) *TraceRecorder {
	return &TraceRecorder{cfg: cfg}
}

// TraceRecord is the on-disk shape of a single orchestrated request.
type TraceRecord struct {
	DecisionID string       `json:"decision_id"`
	Time       time.Time    `json:"time"`
	APIKeyHash string       `json:"api_key_hash,omitempty"`
	Mode       string       `json:"mode"`
	Difficulty string       `json:"difficulty"`
	UserHint   string       `json:"user_hint"`
	Providers  []string     `json:"providers"`
	Turns      []TraceTurn  `json:"turns,omitempty"`
	Final      TraceFinal   `json:"final"`
	Reward     *TraceReward `json:"reward,omitempty"`
}

// TraceTurn captures the outcome of a single role turn.
type TraceTurn struct {
	Index      int    `json:"index"`
	Role       string `json:"role"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Summary    string `json:"summary,omitempty"`
	Verdict    string `json:"verdict,omitempty"`
	Diagnosis  string `json:"diagnosis,omitempty"`
	TokensIn   int    `json:"tokens_in,omitempty"`
	TokensOut  int    `json:"tokens_out,omitempty"`
	LatencyMS  int64  `json:"latency_ms,omitempty"`
	StreamMode bool   `json:"stream,omitempty"`
	Error      string `json:"error,omitempty"`
}

// TraceFinal records the loop's exit state.
type TraceFinal struct {
	TurnIndex   int    `json:"turn_index"`
	HaltedOn    string `json:"halted_on"`
	WorkerError string `json:"worker_error,omitempty"`
}

// TraceReward carries optional reward signals — populated by a future
// /v0/management/orchestrator/feedback endpoint or by an offline join.
type TraceReward struct {
	VerifierAccept bool     `json:"verifier_accept"`
	ClientHappy    *bool    `json:"client_happy,omitempty"`
	Labels         []string `json:"labels,omitempty"`
}

// Record appends a single record to the active trace file. Errors are
// dropped — telemetry must never fail a request.
func (r *TraceRecorder) Record(rec TraceRecord) {
	if r == nil || !r.cfg.Enabled {
		return
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return
	}
	payload = append(payload, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureFileLocked(); err != nil {
		return
	}
	n, err := r.file.Write(payload)
	if err != nil {
		return
	}
	r.bytes += int64(n)

	if rotateMB := int64(r.cfg.RotateMB); rotateMB > 0 && r.bytes >= rotateMB*1024*1024 {
		r.rotateLocked()
	}
}

// Close releases any open file. Safe to call multiple times.
func (r *TraceRecorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		r.bytes = 0
		return err
	}
	return nil
}

// ensureFileLocked opens the active trace file when not already open.
// Caller must hold r.mu.
func (r *TraceRecorder) ensureFileLocked() error {
	if r.file != nil {
		return nil
	}
	dir := r.cfg.Dir
	if dir == "" {
		// No directory configured; we still treat trace as a no-op
		// even when Enabled is true. The configuration loader is
		// expected to substitute a sensible default.
		return fmt.Errorf("trace: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("orchestrator-%s.jsonl",
		time.Now().UTC().Format("20060102-150405")))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	r.file = f
	r.bytes = 0
	// Capture existing file size for rotation accounting.
	if st, err := f.Stat(); err == nil {
		r.bytes = st.Size()
	}
	return nil
}

// rotateLocked closes the active file so the next write opens a new one.
// Caller must hold r.mu.
func (r *TraceRecorder) rotateLocked() {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
		r.bytes = 0
	}
}
