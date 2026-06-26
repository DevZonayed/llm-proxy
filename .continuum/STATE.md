# Project State — CLIProxyAPI / llm-proxy

Last updated: 2026-06-26

## Production deployment (llm.nexalance.cloud)
- Live host: `https://llm.nexalance.cloud`. Panel: `/management.html`, OpenAI API: `/v1/*`.
- Auth: client API keys (`Authorization: Bearer <key>`) from `config.yaml.api-keys`.
  Two known keys: `jhvdsnvlenkjlvdfjkvbefkdnv`, `wkejfhcvnfkldencvwefcjkljn`.
- Management auth: `MANAGEMENT_PASSWORD` env var, used as
  `Authorization: Bearer <secret>` or `X-Management-Key: <secret>` against
  `/v0/management/*`. Secret hash recorded in user's secret store (do NOT commit).
- OAuth providers installed: antigravity (mdjonayedahamed165@gmail.com),
  claude (dev.nexalance@gmail.com), codex (islamafia330@gmail.com, pro plan).
- 29 upstream models exposed across GPT / Claude / Gemini / Other families.

## Task→Model routing (oauth-model-alias, configured 2026-06-26)

Wired via `PUT /v0/management/oauth-model-alias`. All entries use `fork: true`
so the original 29 model ids stay visible AND task aliases are added → 48 total
ids in `/v1/models`. Source of truth: the live management endpoint; mirror
below for documentation only.

| Task | Alias | Upstream | Channel | Rationale |
|---|---|---|---|---|
| Heavy reasoning | `task-reasoning` | claude-opus-4-8 | claude | Fable 5 is gated on this OAuth (returns "use Opus 4.8"); Opus 4.8 is Anthropic's recommended fallback. |
| Long agentic coding | `task-coding-large` | claude-opus-4-8 | claude | Same Opus 4.8; second slot below as fallback. |
| Coding fallback | `task-coding-large-fallback` | claude-opus-4-7 | claude | 87.6% SWE-V, top MCP-Atlas score. |
| Fast Claude | `task-claude-fast` | claude-haiku-4-5-20251001 | claude | Cheapest/fastest Anthropic; 73.3% SWE-V. |
| Claude code review | `task-code-review-claude` | claude-opus-4-5-20251101 | claude | Strong critic, cheaper than 4.7/4.8. |
| Balanced Claude | `task-balanced-claude` | claude-sonnet-4-5-20250929 | claude | OSWorld leader 61.4%. |
| Quick coding | `task-coding-quick` | gpt-5.3-codex-spark | codex | OpenAI's real-time codex tier. |
| Code review | `task-code-review` | codex-auto-review | codex | OpenAI's purpose-built reviewer model. |
| Image gen (best) | `task-image-gen` | gpt-image-2 | codex | #1 on Image Arena, ~99% text rendering. |
| General | `task-general` | gpt-5.5 | codex | All-purpose OpenAI flagship. |
| Fast OpenAI | `task-fast-openai` | gpt-5.4-mini | codex | Cheapest OpenAI. |
| Fast cheap | `task-fast-cheap` | gemini-3.5-flash-extra-low | antigravity | Google's cheapest Flash tier. |
| Bulk cost-sensitive | `task-cost-bulk` | gemini-3.5-flash-extra-low | antigravity | Same model, separate alias. |
| Long context | `task-long-context` | gemini-3.1-pro-low | antigravity | 1M-token Gemini Pro, cheap thinking tier. |
| Multilingual | `task-multilingual` | gemini-3.1-pro-low | antigravity | Tops Global-MMLU-Lite at 93.2%. |
| Vision | `task-vision` | gemini-3-flash | antigravity | Best multimodal of the available Gemini Flashes. |
| Browser agent | `task-browser-agent` | gemini-3-flash-agent | antigravity | Antigravity's agentic-harness build. |
| Image gen (cheap) | `task-image-gen-cheap` | gemini-3.1-flash-image | antigravity | Nano Banana 2; ~50% the cost of gpt-image-2. |
| Step-by-step thinking | `task-thinking` | claude-opus-4-6-thinking | antigravity | Adaptive-thinking Opus 4.6 (SOTA ARC-AGI-2). |

19 task aliases total (claude:6, codex:5, antigravity:8).

### Verified live (2026-06-26)
- `task-fast-cheap` → routed to Gemini, reply "PONG-fast"
- `task-claude-fast` → routed to claude-haiku-4-5-20251001, reply "PONG-claude"
- `task-coding-quick` → routed to gpt-5.3-codex-spark, reply "PONG-codex"
- `task-thinking` → routed to claude-opus-4-6-thinking, returned correct arithmetic
- `task-reasoning` → routed to claude-opus-4-8 after Fable-5 swap, reply "REASON-ok"
- Panel "OAuth Model Aliases" card shows: antigravity 8 / claude 6 / codex 5.

### How to undo / extend
- Add an alias: `PATCH /v0/management/oauth-model-alias` with
  `{"channel":"<claude|codex|antigravity>","aliases":[{name,alias,fork}]}`.
- Replace all: `PUT /v0/management/oauth-model-alias` with
  `{"<channel>":[{name,alias,fork},...]}` (no wrapper key).
- Drop a channel's aliases: `DELETE /v0/management/oauth-model-alias?channel=<name>`.
- Field semantics: `internal/config/config.go:244` (`type OAuthModelAlias`).
  `fork:true` keeps the original name AND exposes the alias; `fork:false`
  hides the original.
- Resolver path: `applyOAuthModelAlias` in `sdk/cliproxy/auth/conductor.go`
  (called from `executionModelCandidates`, ~line 525).

## What this project is
Go-based proxy (CLIProxyAPI fork) that exposes OpenAI/Gemini/Claude/Codex-compatible
endpoints over OAuth'd CLI subscriptions. Multi-account round-robin per provider,
static model aliasing, OpenAI-compatible upstreams, Amp CLI support, embeddable SDK.

## Master-model orchestrator (shipped 2026-06-26)

The user's actual ask, after the static-alias work above: a single configurable
"master" model that classifies each incoming request and routes it to one of
the 29 upstream models. Clients call `model: "<router-alias>"` and the proxy
substitutes the master's pick before dispatch. Streaming, auth pool, alias
resolution, account round-robin, and retry/cooldown stay exactly as before —
the orchestrator only rewrites the model name.

**Status:** Built in-repo, unit-tested, off by default. NOT yet deployed to
llm.nexalance.cloud — needs a Docker rebuild + Dokploy redeploy (PR pending).

### Files (all under `/Users/jonayedahamed/Desktop/Projects/Personal/llm-proxy/`)
- `sdk/cliproxy/orchestrator/doc.go` — package overview + ASCII flow.
- `sdk/cliproxy/orchestrator/router.go` — `Router`, `Caller`, `Decision`,
  `IsEnabled`, `ShouldRoute`, `Route`, `Reload`, JSON parser, fallback logic.
- `sdk/cliproxy/orchestrator/menu.go` — built-in 29-model catalog with one-line
  strength descriptions, plus `buildModelMenu(allowed)` helper.
- `sdk/cliproxy/orchestrator/router_test.go` — 14 tests cover happy path,
  whitelist enforcement, fallback on caller error, timeout, hot reload, prose-
  wrapped JSON tolerance, nil-safety.
- `internal/api/orchestrator_caller.go` — `newOrchestratorCaller(authManager)`
  returns the `Caller` closure that dispatches via the existing
  `AuthManager.Execute` path (so the master call reuses OAuth pool / aliasing
  / cooldown).
- `internal/api/handlers/management/config_orchestrator.go` —
  `Get/Put/Patch/DeleteOrchestrator` handlers + `reloadOrchestratorRouter`
  (live hot-reload, no restart).
- `internal/config/config.go` — `type Orchestrator struct` + `Config.Orchestrator`
  field, off by default.
- `internal/api/server.go` — constructs the Router after the management handler
  is initialized, calls `SetOrchestrator` on handlers and `SetOrchestratorRouter`
  on the mgmt handler; registers `/v0/management/orchestrator` routes.
- `internal/api/handlers/management/handler.go` — adds `orchestratorRouter`
  field + `SetOrchestratorRouter`.
- `sdk/api/handlers/handlers.go` — adds `BaseAPIHandler.Orch`,
  `SetOrchestrator`, `resolveOrchestratedModel`; calls
  `resolveOrchestratedModel` at the top of `ExecuteWithAuthManager`,
  `ExecuteCountWithAuthManager`, `ExecuteStreamWithAuthManager`.

### How a request flows when orchestrator is enabled
1. Client → `POST /v1/chat/completions { model: "master", messages: [...] }`.
2. Handler `ExecuteStreamWithAuthManager` first calls
   `resolveOrchestratedModel(ctx, "master", rawJSON)`.
3. `Orch.ShouldRoute("master")` returns true, so `Orch.Route(ctx, rawJSON)`:
   a. Extracts last 6 user turns (string or part-array content).
   b. Builds an OpenAI-format request to `cfg.Orchestrator.MasterModel`
      with `response_format: {"type":"json_object"}`, the built-in router
      system prompt, and a menu (catalog or `AllowedModels` whitelist).
   c. Calls `newOrchestratorCaller(authManager)` → `AuthManager.Execute`.
   d. Parses `{model, reason}` (tolerates prose-wrapped JSON via a brace-walk).
   e. Validates against `AllowedModels`; on any failure returns
      `Decision{Model: cfg.Fallback or cfg.MasterModel, Reason: "fallback: ..."}`.
4. Handler rewrites `modelName` and proceeds with existing dispatch path —
   streaming bytes flow back from the picked model unchanged.

### Management API (live hot-reload, no restart)
```bash
# Read
curl -H "Authorization: Bearer $MANAGEMENT_PASSWORD" \
  https://llm.nexalance.cloud/v0/management/orchestrator

# Enable with recommended defaults
curl -X PATCH -H "Authorization: Bearer $MANAGEMENT_PASSWORD" \
  -H "Content-Type: application/json" \
  -d '{"enabled":true,"master-model":"gemini-3.5-flash-extra-low","router-alias":"master","fallback":"gpt-5.4-mini","timeout-ms":8000,"log-decisions":true}' \
  https://llm.nexalance.cloud/v0/management/orchestrator

# Restrict the menu (master may only pick from these)
curl -X PATCH -H "Authorization: Bearer $MANAGEMENT_PASSWORD" \
  -H "Content-Type: application/json" \
  -d '{"allowed-models":["claude-opus-4-8","gpt-5.3-codex-spark","gemini-3-flash","gemini-3.1-pro-low","claude-haiku-4-5-20251001","gpt-image-2"]}' \
  https://llm.nexalance.cloud/v0/management/orchestrator

# Disable
curl -X DELETE -H "Authorization: Bearer $MANAGEMENT_PASSWORD" \
  https://llm.nexalance.cloud/v0/management/orchestrator

# Use it
curl -H "Authorization: Bearer $CLI_PROXY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"master","messages":[{"role":"user","content":"fix this Go bug: ..."}]}' \
  https://llm.nexalance.cloud/v1/chat/completions
```

### Safety / invariants preserved
- Off by default: `Config.Orchestrator.Enabled == false` makes
  `resolveOrchestratedModel` a no-op. `go build ./...` and existing tests pass
  with zero behavior change.
- Never fails a request because of routing — every error path returns the
  configured Fallback (or MasterModel itself when Fallback is empty).
- Per-classification timeout (default 8s); master call is bounded.
- Hot reload via management API; no server restart needed.
- Built-in 29-model catalog matches /v1/models exactly (kept in sync via
  `defaultModelCatalog` in `sdk/cliproxy/orchestrator/menu.go`).

### Tests
- `go test ./sdk/cliproxy/orchestrator/...` — 14 unit tests, all pass.
- `go test ./sdk/api/handlers/... ./internal/api/... ./internal/config/...`
  — all pre-existing tests still pass (no regressions).

### What is NOT in v0 (deferred / out of scope)
- Tri-role Thinker/Worker/Verifier loop (the §5.2 "hard" path in the design
  doc). v0 ships only the single-shot path with an LLM-based router — that's
  what the user explicitly asked for.
- Trace recorder writing JSONL files for offline training. v0 emits an
  info-level log line per routed request when `log-decisions: true`.
- Difficulty classifier; the master itself is the only intelligence.
- Per-API-key gating; v0 is global (any client sending the alias gets routed).

### Deferred for follow-up (after smoke-tested in prod)
- UI panel section in the management SPA for editing `Orchestrator` config
  visually. v0 is API-only.
- Trace persistence (file/SQLite/Redis stream) per §8 of design doc.
- Tri-role loop (§5.2) when the master picks `mode=hard`.

## Active design thread: Fugu-style orchestrator
- Source: Sakana_Fugu_Report.docx at repo root (June 23, 2026 research report).
- Goal: add a TRINITY-style coordinator layer that picks WHICH provider family
  serves each request, with optional tri-role (Thinker/Worker/Verifier) loop on
  hard tasks. Account selection within a family stays unchanged.
- Design doc: `docs/fugu-orchestrator-design.md` (v0 spec, no code yet).
- Scope: v0 = single-shot + tri-role with rules-based policy + trace recorder
  + sidecar stub for learned policy. v1 = Conductor/DAG workflows + trained head.
- Status: design spec written; awaiting user review.
- Open threads (from §14 of the design doc):
  1. Streaming-after-ACCEPT strategy: (A) re-issue Worker as stream
     [default] vs. (B) buffer-and-flush.
  2. Gating granularity: per-API-key (planned) vs. also per-route.
  3. Trace sink: rotating JSON files (planned) vs. SQLite / Postgres / Redis stream.
  4. Report was truncated mid-section (sep-CMA-ES table); §10 (Conductor) and
     possibly §7 (budgets) will need amendment once the rest is supplied.

## Key files / surfaces (verified)
| Concern | File:line | Function |
|---|---|---|
| HTTP entry (OpenAI) | sdk/api/handlers/openai/openai_handlers.go:98 | ChatCompletions |
| Dispatch | sdk/api/handlers/handlers.go:536, :633 | ExecuteWithAuthManager, ExecuteStreamWithAuthManager |
| Model resolution | sdk/api/handlers/handlers.go:849 | getRequestDetails |
| Alias / candidate models | sdk/cliproxy/auth/conductor.go:523 | executionModelCandidates |
| Pick auth + executor | sdk/cliproxy/auth/conductor.go:2888 | pickNextMixed |
| Executor iface | sdk/cliproxy/auth/conductor.go:28 | ProviderExecutor |
| Selector iface (account) | sdk/cliproxy/auth/conductor.go:109 | Selector.Pick |
| Hook iface (reward) | sdk/cliproxy/auth/conductor.go:120 | Hook.OnResult |
| Translator iface | sdk/translator/types.go:6 | RequestTransform, ResponseTransform |
| Round-robin | sdk/cliproxy/auth/selector.go:26 | RoundRobinSelector |
| Config | internal/config/config.go:28 | Config struct |
| Usage telemetry | internal/usage/logger_plugin.go:27 | LoggerPlugin |

## Conventions
- Skill registry rule for this project: search_skills before substantive work;
  install best match via add_skill_to_project. Currently installed:
  `qodex-ai/ai-agent-skills/multi-agent-orchestration`.
- New code for the orchestrator goes under `sdk/cliproxy/orchestrator/` — peer
  of Manager, Selector, ProviderExecutor (not an HTTP-layer concern).
- Orchestrator must be OFF by default — `go build ./...` and existing behavior
  must be identical when `orchestrator.enabled: false`.
- Account selection within a chosen provider family stays where it is. Do not
  rewrite Selector for the orchestrator.

## Not yet decided / not yet built
- Implementation (this iteration was design-only by user choice).
- Training pipeline for the learned head (sep-CMA-ES). v0 ships trace format
  and sidecar wire protocol; training is a separate Python workstream.
- Per-tenant policies (one global policy in v0).
