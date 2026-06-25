# Project State — CLIProxyAPI / llm-proxy

Last updated: 2026-06-25 (post-PR #2 + visual editor surfaces for v2/v2.1)

## What this project is
Go-based proxy (CLIProxyAPI fork) that exposes OpenAI/Gemini/Claude/Codex-compatible
endpoints over OAuth'd CLI subscriptions. Multi-account round-robin per provider,
static model aliasing, OpenAI-compatible upstreams, Amp CLI support, embeddable SDK.

## Active design thread: Fugu-style orchestrator
- Source: Sakana_Fugu_Report.docx at repo root (June 23, 2026 research report).
- Goal: TRINITY-style coordinator that picks WHICH provider AND which upstream
  MODEL serves each request, with optional tri-role
  (Thinker/Worker/Verifier) loop on hard tasks. Account selection within a
  family stays unchanged.
- Design doc: `docs/fugu-orchestrator-design.md`.
- Implementation: `sdk/cliproxy/orchestrator/` (shipped on PR #1).
- Scope: v0 = single-shot + tri-role with rules-based policy + trace recorder
  + sidecar stub for learned policy + per-role/family model pinning. v1 =
  Conductor/DAG workflows + trained head.
- Status: v0 on branch `mochi/nara/https-github-com-berriai-litellm`. Reviewer
  must run `go build ./...` and `go test ./sdk/cliproxy/orchestrator/...`
  (Go was not installed on the authoring machine).

## Per-task / per-role model pinning (how to configure)
- "Master" config surface is the YAML at the proxy root
  (`config.yaml` / `config.example.yaml`). The watcher hot-reloads it; there
  is no live Management API endpoint to edit orchestrator config in v0.
- **Provider** preference per role/family:
  `orchestrator.policy.rules.defaults` — map of family/role key →
  ordered provider list. Familiar form.
- **Model** pinning per role/family:
  `orchestrator.policy.rules.models` — map of family/role key →
  upstream model string. Resolution order: role-key
  (thinker|verifier) → family-key (code|math|recall|general) →
  "default" → the user's requested model. Blank entries are ignored.
- Worked example "gemini-flash plans, claude-opus codes, gpt-5 verifies"
  lives in `config.example.yaml` (commented block) and in §7 of the
  design doc.

## v2 routing policy: catalog + categories + LLM classifier (this branch)
The 3-line `defaults: {code, math, recall}` table didn't scale past a
handful of providers. v2 adds, additively (legacy still works):
- `orchestrator.catalog`: list of `{id, provider, model, tags,
  description, cost-tier, latency-tier, context-window, supports}`
  entries. The knowledge base of upstream models.
- `orchestrator.categories`: dynamic, user-defined task buckets with
  `{name, instructions, match{keywords|regex|require-code-block|
  min/max-tokens|require-tools|any-of|none-of}, prefer[catalog ids],
  role-pins{thinker|worker|verifier}}`.
- `orchestrator.classifier`: `kind: heuristic|llm|hybrid`. When
  `llm`/`hybrid` and `llm.enabled`, the orchestrator calls
  `llm.provider`/`llm.model` (must be in the candidate set) via
  `AuthManager.Execute` with a short routing prompt, caches by hash,
  falls back to heuristic / `default-category` / fail per
  `fallback-on-error`.
- Resolution order: category Prefer/RolePins → legacy
  `defaults`/`models` → first candidate. Account selection inside the
  chosen provider is still `Selector.Pick`.
- New files: `sdk/cliproxy/orchestrator/{categories.go,
  category_input.go, classifier_llm.go, categories_test.go}`.
- Modified: `internal/config/orchestrator.go`, `sdk/config/config.go`,
  `sdk/cliproxy/orchestrator/{config.go, from_config.go, policy.go,
  policy_rules.go, orchestrator.go, trace.go}`,
  `sdk/api/handlers/handlers.go`, `config.example.yaml`,
  `docs/fugu-orchestrator-design.md` (new §15).
- `Decision` now carries `CategoryHint` + `NormalizedModel`; handler
  forwards both into `RunRequest`. The single-shot path honors
  `Decision.NormalizedModel` so per-category model pins reach the
  executor without further plumbing.
- Visual config editor for v2 fields is NOT yet built — catalog and
  categories are YAML-only this iteration. Adding a Catalog + Categories
  section to the React `VisualConfigEditor.tsx` is a follow-up PR.
  **Shipped in PR #3** (this branch): `CatalogEditor`,
  `CategoriesEditor`, `ClassifierEditor` in
  `client/src/components/config/VisualConfigEditorBlocks.tsx`; new draft
  types `CatalogEntryDraft`, `CategoryDraft`, and flattened classifier
  fields in `client/src/types/visualConfig.ts`; round-trip
  parse/serialize/dirty-tracking in
  `client/src/hooks/useVisualConfig.ts`; three new SectionSubsections
  inside the Orchestrator section of `VisualConfigEditor.tsx`. The
  surface is additive — saves preserve YAML keys the user didn't touch,
  empty rows are dropped at serialization time, and the section stays
  invisible until the operator opts in.

## v2.1 direct-model routing (no categories required)
The simpler mental model the user asked for: describe each model in a
paragraph, let the LLM pick the model directly. Additive on top of v2.
- `CatalogEntry.Instructions` (paragraph) — what this entry is best
  at. The direct-model classifier sees it.
- `CatalogEntry.Roles` (free-form list — `[thinker]`, `[worker]`,
  `[verifier]`, `[classifier]`, etc.) — soft role hints for tri-role.
  Empty = eligible for any role.
- `classifier.kind: direct-model` — new mode. LLM picks a CATALOG ID,
  not a category name. Validation requires non-empty `catalog`.
- New `sdk/cliproxy/orchestrator/classifier_direct.go` +
  `classifier_direct_test.go`. Reuses the existing TTL cache,
  provider-must-be-in-candidate-set guard, and reply normalization
  patterns from the category classifier.
- `Decision.ModelCatalogID` and `RunRequest.ModelCatalogID` plumbed
  through; handler.go forwards both stream and non-stream paths.
- `TurnState.ModelCatalogID` is the highest-priority signal in
  `RulesPolicy.Decide` (priority 1, Worker only). Priority 2 is the
  Roles-tagged catalog walk for Thinker/Verifier. Priority 3 is the
  v2 category path. Priority 4 is legacy code/math/recall.
- `TraceRecord.ModelCatalogID` joins `category` in the trace JSON.
- Worked example (`claude-opus-4.8` SWE / `gpt-5-thinking` math /
  `haiku-fast` thinker) in `config.example.yaml` and §16 of the
  design doc.

## Open questions for the user (v2.1)
- Should the visual config editor learn the catalog/categories/direct-
  model surfaces, or are operators content with YAML for this layer?
- Worth adding a 3-call-per-request "per-role direct-model" mode (ask
  the classifier separately which Thinker, Worker, Verifier to use)?
  Triples classifier cost; not yet implemented.
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
- Training pipeline for the learned head (sep-CMA-ES). v0 ships trace format
  and sidecar wire protocol; training is a separate Python workstream.
- Per-tenant policies (one global policy in v0).
- Live Management API endpoint for orchestrator config (currently YAML-only).
- Validation that a pinned model is actually servable by at least one
  provider in the matching `defaults` list — today this is enforced by the
  dispatch layer at request time (request will error if unservable).
- Cost-aware optimization (catalog has `cost-tier`/`latency-tier` but
  they aren't weighted in selection yet).
- Auto-discovery of catalog entries from the existing `AI Providers →
  Models` panel (operators write the catalog by hand today).
- Visual config editor surface for `catalog` / `categories` /
  `classifier` blocks.
