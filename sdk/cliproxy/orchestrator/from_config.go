package orchestrator

import (
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// FromConfig converts an SDK-shaped orchestrator configuration into the
// orchestrator package's Config form, layering documented defaults on
// top of any explicit fields the caller provided.
//
// FromConfig never returns an error; missing values are filled with the
// Default() Config. The orchestrator's own Validate is the right place
// to surface invalid input.
func FromConfig(in sdkconfig.OrchestratorConfig) Config {
	c := Default()

	c.Enabled = in.Enabled
	if in.Mode != "" {
		c.Mode = in.Mode
	}
	if in.EnabledForAPIKeys != nil {
		c.EnabledForAPIKeys = append([]string(nil), in.EnabledForAPIKeys...)
	}
	c.RespectRequestHeaders = in.RespectRequestHeaders

	if in.Policy.Kind != "" {
		c.Policy.Kind = in.Policy.Kind
	}
	if in.Policy.Rules.Defaults != nil {
		c.Policy.Rules.Defaults = cloneDefaults(in.Policy.Rules.Defaults)
	}
	if in.Policy.Rules.Models != nil {
		c.Policy.Rules.Models = cloneModels(in.Policy.Rules.Models)
	}
	c.Policy.Rules.VerifierMustDiffer = in.Policy.Rules.VerifierMustDiffer

	if in.Policy.Learned.Socket != "" {
		c.Policy.Learned.Socket = in.Policy.Learned.Socket
	}
	if in.Policy.Learned.TimeoutMS > 0 {
		c.Policy.Learned.Timeout = time.Duration(in.Policy.Learned.TimeoutMS) * time.Millisecond
	}
	if in.Policy.Learned.FallbackOnError != "" {
		c.Policy.Learned.FallbackOnError = in.Policy.Learned.FallbackOnError
	}

	if in.Budgets.MaxTurns > 0 {
		c.Budgets.MaxTurns = in.Budgets.MaxTurns
	}
	if in.Budgets.WallBudgetMS > 0 {
		c.Budgets.WallBudget = time.Duration(in.Budgets.WallBudgetMS) * time.Millisecond
	}
	if in.Budgets.MinVerifierTurns > 0 {
		c.Budgets.MinVerifierTurns = in.Budgets.MinVerifierTurns
	}
	if in.Budgets.EscalateOnVerifierRevise > 0 {
		c.Budgets.EscalateOnVerifierRevise = in.Budgets.EscalateOnVerifierRevise
	}
	if in.Budgets.SingleShotFallbackMS > 0 {
		c.Budgets.SingleShotFallback = time.Duration(in.Budgets.SingleShotFallbackMS) * time.Millisecond
	}

	c.Difficulty.Enabled = in.Difficulty.Enabled
	if in.Difficulty.HardTokenThreshold > 0 {
		c.Difficulty.HardTokenThreshold = in.Difficulty.HardTokenThreshold
	}
	if in.Difficulty.MediumTokenThreshold > 0 {
		c.Difficulty.MediumTokenThreshold = in.Difficulty.MediumTokenThreshold
	}
	c.Difficulty.PromoteOnTools = in.Difficulty.PromoteOnTools

	c.Trace.Enabled = in.Trace.Enabled
	if in.Trace.Dir != "" {
		c.Trace.Dir = in.Trace.Dir
	}
	if in.Trace.KeepWorkerExcerptsChars > 0 {
		c.Trace.KeepWorkerExcerptsChars = in.Trace.KeepWorkerExcerptsChars
	}
	c.Trace.KeepFullWorkerPayloads = in.Trace.KeepFullWorkerPayloads
	if in.Trace.RotateMB > 0 {
		c.Trace.RotateMB = in.Trace.RotateMB
	}

	return c
}

func cloneDefaults(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func cloneModels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
