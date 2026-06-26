// Package orchestrator implements the master-model request router.
//
// At a glance:
//
//	client                             proxy                                upstream
//	──────                             ─────                                ────────
//	POST /v1/chat/completions
//	{ model: "master",                 1. handler sees model == RouterAlias
//	  messages: [...] }     ────────►  2. orchestrator.Route():
//	                                      a. build router prompt with the
//	                                         catalog (incoming messages +
//	                                         29-model menu w/ one-line each)
//	                                      b. POST to internal AuthManager
//	                                         with model=MasterModel,
//	                                         response_format=json_object
//	                                      c. parse {"model","reason"}
//	                                      d. validate against AllowedModels
//	                                   3. handler rewrites modelName = picked
//	                                   4. existing dispatch path runs
//	                                      unchanged, streams back
//	                                                                       ─────►
//	bytes streamed back   ◄────────────────────────────────────────────────
//
// The Router is constructed at server startup with a Caller closure that
// closes over the AuthManager + the existing handler's getRequestDetails
// helper (so the master call benefits from the same OAuth/account pool and
// alias resolution everything else uses).
//
// The orchestrator is OFF unless Orchestrator.Enabled = true in config; when
// disabled, Route() is never invoked and the proxy's existing model
// resolution is bit-for-bit identical to before this package existed.
package orchestrator
