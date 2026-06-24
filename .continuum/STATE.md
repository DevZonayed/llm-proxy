# Project State — CLIProxyAPI / llm-proxy

Last updated: 2026-06-24

## What this project is
Go-based proxy (CLIProxyAPI fork) that exposes OpenAI/Gemini/Claude/Codex-compatible
endpoints over OAuth'd CLI subscriptions. Multi-account round-robin per provider,
static model aliasing, OpenAI-compatible upstreams, Amp CLI support, embeddable SDK.

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
