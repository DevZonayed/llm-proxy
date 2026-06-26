package management

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/orchestrator"
	log "github.com/sirupsen/logrus"
)

// Management endpoints for the Fugu-style orchestrator (see
// docs/fugu-orchestrator-design.md + sdk/cliproxy/orchestrator).
//
//	GET    /v0/management/orchestrator   -> { "orchestrator": { ... } }
//	PUT    /v0/management/orchestrator   <- full OrchestratorConfig (replace)
//	PATCH  /v0/management/orchestrator   <- partial OrchestratorConfig (merge)
//	DELETE /v0/management/orchestrator   -> reset to zero (Enabled=false)
//
// PUT/PATCH/DELETE persist the YAML config and then hot-reload the live
// orchestrator on BaseAPIHandler so the change takes effect without a
// server restart. PUT and PATCH accept either { ...fields... } directly
// or { "orchestrator": { ...fields... } } (mirrors the oauth-model-alias
// endpoint shape so the UI can use one helper).

// GetOrchestrator returns the current orchestrator config block.
func (h *Handler) GetOrchestrator(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(500, gin.H{"error": "config unavailable"})
		return
	}
	c.JSON(200, gin.H{"orchestrator": h.cfg.Orchestrator})
}

// PutOrchestrator replaces the entire orchestrator config block.
func (h *Handler) PutOrchestrator(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(500, gin.H{"error": "config unavailable"})
		return
	}
	raw, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	next, err := unmarshalOrchestratorBody(raw)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := validateOrchestrator(next); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	h.cfg.Orchestrator = next
	if err := h.reloadOrchestratorRouter(); err != nil {
		c.JSON(400, gin.H{"error": "config saved but orchestrator failed to rebuild: " + err.Error()})
		return
	}
	h.persist(c)
}

// PatchOrchestrator merges fields onto the current orchestrator config.
// Only fields present in the body are touched; everything else is preserved.
func (h *Handler) PatchOrchestrator(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(500, gin.H{"error": "config unavailable"})
		return
	}
	raw, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	// Detect which top-level fields were sent.
	body := unwrapOrchestratorWrapper(raw)
	var present map[string]json.RawMessage
	if err := json.Unmarshal(body, &present); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	overlay, err := unmarshalOrchestratorBody(raw)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	patched := h.cfg.Orchestrator
	if _, ok := present["enabled"]; ok {
		patched.Enabled = overlay.Enabled
	}
	if _, ok := present["mode"]; ok {
		patched.Mode = overlay.Mode
	}
	if _, ok := present["enabled-for-api-keys"]; ok {
		patched.EnabledForAPIKeys = overlay.EnabledForAPIKeys
	}
	if _, ok := present["respect-request-headers"]; ok {
		patched.RespectRequestHeaders = overlay.RespectRequestHeaders
	}
	if _, ok := present["policy"]; ok {
		patched.Policy = overlay.Policy
	}
	if _, ok := present["budgets"]; ok {
		patched.Budgets = overlay.Budgets
	}
	if _, ok := present["difficulty"]; ok {
		patched.Difficulty = overlay.Difficulty
	}
	if _, ok := present["trace"]; ok {
		patched.Trace = overlay.Trace
	}
	if _, ok := present["catalog"]; ok {
		patched.Catalog = overlay.Catalog
	}
	if _, ok := present["categories"]; ok {
		patched.Categories = overlay.Categories
	}
	if _, ok := present["classifier"]; ok {
		patched.Classifier = overlay.Classifier
	}

	if err := validateOrchestrator(patched); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	h.cfg.Orchestrator = patched
	if err := h.reloadOrchestratorRouter(); err != nil {
		c.JSON(400, gin.H{"error": "config saved but orchestrator failed to rebuild: " + err.Error()})
		return
	}
	h.persist(c)
}

// DeleteOrchestrator resets the orchestrator config block to zero (off).
func (h *Handler) DeleteOrchestrator(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(500, gin.H{"error": "config unavailable"})
		return
	}
	h.cfg.Orchestrator = config.OrchestratorConfig{}
	_ = h.reloadOrchestratorRouter() // rebuild is a no-op when disabled
	h.persist(c)
}

// reloadOrchestratorRouter rebuilds the orchestrator from the current
// cfg.Orchestrator and swaps it on the live BaseAPIHandler. Safe to call
// when the orchestrator is disabled (nil swap clears the existing one).
func (h *Handler) reloadOrchestratorRouter() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	bh := h.baseHandler
	am := h.authManager
	cfgCopy := h.cfg.Orchestrator
	h.mu.Unlock()

	if bh == nil {
		// No request-time handler wired (e.g. construction order or tests).
		// Persisted config takes effect on next restart.
		return nil
	}
	// Disabled — drop any live orchestrator.
	if !cfgCopy.Enabled {
		bh.SetOrchestrator(nil)
		return nil
	}
	if am == nil {
		return fmt.Errorf("auth manager unavailable")
	}
	o, err := orchestrator.New(orchestrator.FromConfig(cfgCopy), am)
	if err != nil {
		log.WithError(err).Warn("orchestrator: rebuild failed; keeping previous instance")
		return err
	}
	bh.SetOrchestrator(o) // o may be nil when Enabled flipped off mid-flight
	return nil
}

// unmarshalOrchestratorBody accepts either the raw config object or
// { "orchestrator": { ... } } and returns the parsed struct.
func unmarshalOrchestratorBody(body []byte) (config.OrchestratorConfig, error) {
	body = unwrapOrchestratorWrapper(body)
	var out config.OrchestratorConfig
	if err := json.Unmarshal(body, &out); err != nil {
		return config.OrchestratorConfig{}, fmt.Errorf("invalid body: %w", err)
	}
	return out, nil
}

// unwrapOrchestratorWrapper returns body[orchestrator] if the top-level
// object contains an "orchestrator" key, else returns body unchanged.
func unwrapOrchestratorWrapper(body []byte) []byte {
	var wrapper struct {
		Orchestrator json.RawMessage `json:"orchestrator"`
	}
	if json.Unmarshal(body, &wrapper) == nil && len(wrapper.Orchestrator) > 0 {
		return wrapper.Orchestrator
	}
	return body
}

// validateOrchestrator runs the orchestrator's own Config.Validate via the
// FromConfig adapter so callers learn about bad configs at PATCH time
// rather than at first request.
func validateOrchestrator(c config.OrchestratorConfig) error {
	return orchestrator.FromConfig(c).Validate()
}
