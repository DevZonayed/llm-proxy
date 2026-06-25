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

	if len(in.Catalog) > 0 {
		c.Catalog = cloneCatalog(in.Catalog)
	}
	if len(in.Categories) > 0 {
		c.Categories = cloneCategories(in.Categories)
	}

	// Classifier. We treat the section as "explicit" (caller takes
	// ownership of every field) only when Kind is set. Otherwise the
	// orchestrator's Default() ClassifierConfig wins — preventing the
	// common footgun where a YAML block that omits a bool flips it from
	// its documented default to Go's zero value.
	if in.Classifier.Kind != "" {
		c.Classifier.Kind = in.Classifier.Kind
		c.Classifier.Heuristic.FirstMatchWins = in.Classifier.Heuristic.FirstMatchWins
	}
	llm := in.Classifier.LLM
	c.Classifier.LLM.Enabled = llm.Enabled
	if llm.Provider != "" {
		c.Classifier.LLM.Provider = llm.Provider
	}
	if llm.Model != "" {
		c.Classifier.LLM.Model = llm.Model
	}
	if llm.TimeoutMS > 0 {
		c.Classifier.LLM.Timeout = time.Duration(llm.TimeoutMS) * time.Millisecond
	}
	if llm.CacheTTLSeconds > 0 {
		c.Classifier.LLM.CacheTTL = time.Duration(llm.CacheTTLSeconds) * time.Second
	}
	if llm.MaxInputChars > 0 {
		c.Classifier.LLM.MaxInputChars = llm.MaxInputChars
	}
	if llm.PromptTemplate != "" {
		c.Classifier.LLM.PromptTemplate = llm.PromptTemplate
	}
	if llm.FallbackOnError != "" {
		c.Classifier.LLM.FallbackOnError = llm.FallbackOnError
	}
	if llm.DefaultCategory != "" {
		c.Classifier.LLM.DefaultCategory = llm.DefaultCategory
	}

	return c
}

// cloneCatalog converts the SDK catalog form into the orchestrator
// package's CatalogEntry form. The conversion is field-by-field with
// defensive slice copies so the orchestrator never aliases the loader's
// memory.
func cloneCatalog(in []sdkconfig.OrchestratorCatalogEntry) []CatalogEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]CatalogEntry, 0, len(in))
	for _, e := range in {
		out = append(out, CatalogEntry{
			ID:            e.ID,
			Provider:      e.Provider,
			Model:         e.Model,
			Tags:          append([]string(nil), e.Tags...),
			Description:   e.Description,
			Instructions:  e.Instructions,
			Roles:         append([]string(nil), e.Roles...),
			CostTier:      e.CostTier,
			LatencyTier:   e.LatencyTier,
			ContextWindow: e.ContextWindow,
			Supports:      append([]string(nil), e.Supports...),
		})
	}
	return out
}

// cloneCategories converts the SDK category form into the orchestrator
// package's Category form.
func cloneCategories(in []sdkconfig.OrchestratorCategory) []Category {
	if len(in) == 0 {
		return nil
	}
	out := make([]Category, 0, len(in))
	for _, cat := range in {
		entry := Category{
			Name:         cat.Name,
			Instructions: cat.Instructions,
			Match: CategoryMatch{
				Keywords:         append([]string(nil), cat.Match.Keywords...),
				Regex:            append([]string(nil), cat.Match.Regex...),
				RequireCodeBlock: cat.Match.RequireCodeBlock,
				MinTokens:        cat.Match.MinTokens,
				MaxTokens:        cat.Match.MaxTokens,
				RequireTools:     cat.Match.RequireTools,
				AnyOf:            append([]string(nil), cat.Match.AnyOf...),
				NoneOf:           append([]string(nil), cat.Match.NoneOf...),
			},
			Prefer: append([]string(nil), cat.Prefer...),
		}
		if len(cat.RolePins) > 0 {
			entry.RolePins = make(map[string]string, len(cat.RolePins))
			for k, v := range cat.RolePins {
				entry.RolePins[k] = v
			}
		}
		out = append(out, entry)
	}
	return out
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
