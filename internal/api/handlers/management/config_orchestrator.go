package management

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// GetOrchestrator returns the current orchestrator config block.
//
//	GET /v0/management/orchestrator
//	-> { "orchestrator": { ... } }
func (h *Handler) GetOrchestrator(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(500, gin.H{"error": "config unavailable"})
		return
	}
	c.JSON(200, gin.H{"orchestrator": h.cfg.Orchestrator})
}

// PutOrchestrator replaces the entire orchestrator config block.
//
//	PUT /v0/management/orchestrator
//	body: { ...full Orchestrator object... }  or  { "orchestrator": { ... } }
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
	h.cfg.Orchestrator = next
	h.reloadOrchestratorRouter()
	h.persist(c)
}

// PatchOrchestrator merges fields onto the current orchestrator config block.
// Unknown / zero-valued fields are left alone. Useful for one-knob toggles
// like `{ "enabled": true }` from the UI.
//
//	PATCH /v0/management/orchestrator
//	body: { ...partial Orchestrator object... }
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
	// Unmarshal into a pointer-map to detect which fields the client actually
	// supplied; missing fields are preserved.
	patched := h.cfg.Orchestrator
	overlay, err := unmarshalOrchestratorBody(raw)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	// JSON object with the user-supplied fields, used to detect presence.
	var present map[string]json.RawMessage
	body := raw
	// Unwrap one level of { "orchestrator": {...} } if present.
	var wrapper struct {
		Orchestrator json.RawMessage `json:"orchestrator"`
	}
	if json.Unmarshal(raw, &wrapper) == nil && len(wrapper.Orchestrator) > 0 {
		body = wrapper.Orchestrator
	}
	if json.Unmarshal(body, &present) != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	if _, ok := present["enabled"]; ok {
		patched.Enabled = overlay.Enabled
	}
	if _, ok := present["master-model"]; ok {
		patched.MasterModel = overlay.MasterModel
	}
	if _, ok := present["router-alias"]; ok {
		patched.RouterAlias = overlay.RouterAlias
	}
	if _, ok := present["allowed-models"]; ok {
		patched.AllowedModels = overlay.AllowedModels
	}
	if _, ok := present["fallback"]; ok {
		patched.Fallback = overlay.Fallback
	}
	if _, ok := present["timeout-ms"]; ok {
		patched.TimeoutMs = overlay.TimeoutMs
	}
	if _, ok := present["log-decisions"]; ok {
		patched.LogDecisions = overlay.LogDecisions
	}
	if _, ok := present["system-prompt-override"]; ok {
		patched.SystemPromptOverride = overlay.SystemPromptOverride
	}

	h.cfg.Orchestrator = patched
	h.reloadOrchestratorRouter()
	h.persist(c)
}

// DeleteOrchestrator resets the block to zero (off). The router stays wired
// but IsEnabled() will return false until re-configured.
//
//	DELETE /v0/management/orchestrator
func (h *Handler) DeleteOrchestrator(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(500, gin.H{"error": "config unavailable"})
		return
	}
	h.cfg.Orchestrator = config.Orchestrator{}
	h.reloadOrchestratorRouter()
	h.persist(c)
}

// reloadOrchestratorRouter pushes the current cfg.Orchestrator into the live
// router so the change takes effect immediately (no restart required).
func (h *Handler) reloadOrchestratorRouter() {
	if h == nil {
		return
	}
	h.mu.Lock()
	router := h.orchestratorRouter
	cfgCopy := h.cfg.Orchestrator
	h.mu.Unlock()
	if router != nil {
		router.Reload(&cfgCopy)
	}
}

// unmarshalOrchestratorBody accepts either { ...fields... } or
// { "orchestrator": { ...fields... } } and returns the parsed struct.
func unmarshalOrchestratorBody(body []byte) (config.Orchestrator, error) {
	var direct config.Orchestrator
	if err := json.Unmarshal(body, &direct); err == nil && !isZeroBody(body) {
		return direct, nil
	}
	var wrapper struct {
		Orchestrator config.Orchestrator `json:"orchestrator"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return config.Orchestrator{}, err
	}
	return wrapper.Orchestrator, nil
}

func isZeroBody(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' && c != '{' && c != '}' {
			return false
		}
	}
	return true
}
