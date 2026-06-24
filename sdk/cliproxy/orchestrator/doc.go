// Package orchestrator implements a Fugu-style multi-agent router that sits
// between the HTTP handler layer and the auth manager.
//
// The orchestrator decides, per inbound request, which provider family should
// serve the request and (optionally) runs a tri-role Thinker/Worker/Verifier
// loop on hard tasks. Account selection within the chosen provider family
// stays in the existing core auth Manager and Selector implementations —
// the orchestrator only narrows the candidate provider set.
//
// The design is documented in docs/fugu-orchestrator-design.md at the repo
// root. Key invariants:
//
//   - Off by default. When the configuration disables the orchestrator or
//     an API key is not whitelisted, callers must observe behavior identical
//     to today.
//   - No new HTTP API surface. Clients keep calling the existing endpoints.
//   - No buffering of streamed bytes. The final Worker turn calls
//     coreauth.Manager.ExecuteStream and bytes flow through unchanged.
//   - Failure-safe. If the orchestrator errors at any point it falls back to
//     today's single-shot dispatch with the original candidate provider list.
package orchestrator
