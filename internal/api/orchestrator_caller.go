package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/orchestrator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// newOrchestratorCaller returns the Caller closure used by the master-model
// orchestrator. It dispatches the master's classification request through the
// existing AuthManager so the master call reuses the proxy's OAuth pool,
// alias resolution, and retry/cooldown logic.
//
// The closure expects rawJSON to be an OpenAI-format /v1/chat/completions
// request body and returns the upstream OpenAI-format chat completion response.
func newOrchestratorCaller(authManager *coreauth.Manager) orchestrator.Caller {
	if authManager == nil {
		return func(context.Context, string, []byte) ([]byte, error) {
			return nil, errors.New("orchestrator caller: nil auth manager")
		}
	}
	return func(ctx context.Context, model string, rawJSON []byte) ([]byte, error) {
		model = strings.TrimSpace(model)
		if model == "" {
			return nil, errors.New("orchestrator caller: empty model")
		}
		providers := util.GetProviderName(model)
		if len(providers) == 0 {
			return nil, fmt.Errorf("orchestrator caller: unknown provider for master model %q", model)
		}
		req := coreexecutor.Request{
			Model:   model,
			Payload: rawJSON,
		}
		opts := coreexecutor.Options{
			Stream:          false,
			SourceFormat:    sdktranslator.FromString("openai"),
			OriginalRequest: rawJSON,
		}
		resp, err := authManager.Execute(ctx, providers, req, opts)
		if err != nil {
			return nil, err
		}
		return resp.Payload, nil
	}
}
