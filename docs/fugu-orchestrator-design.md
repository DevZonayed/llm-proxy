# Fugu-Style Orchestrator on CLIProxyAPI — Design Spec

Status: **Draft** · Author: design pass from the Sakana Fugu June 23, 2026 research report
Scope: design only — no code in this doc.

---

## 1. Context

CLIProxyAPI is today a *transport-and-account* layer. It exposes
OpenAI/Gemini/Claude/Codex-compatible endpoints over OAuth'd CLI subscriptions
and does **multi-account round-robin within a provider**, plus **static model
aliasing** (`claude-opus-4.5 → claude-sonnet-4`). What it does not do is decide,
per request, **which frontier family is the right one** — that decision is left
to the client or to a coarse alias table.

Sakana Fugu (June 22, 2026) is a *small coordinator over frontier models*:

- **TRINITY** (~0.6B backbone + ~10–20K head, trained with sep-CMA-ES) emits one
  choice per turn — pick a worker model and assign it a role (Thinker / Worker
  / Verifier). Penultimate-token decision, no generation in the coordinator.
- **Conductor / Fugu Ultra** (~7B, GRPO-trained) emits a whole workflow DAG.

Their thesis: GPT leads on math/planning, Opus on software/security, Gemini on
science/recall, and a tiny coordinator that learns each model's strengths can
beat any individual model. CLIProxyAPI already terminates calls to all three of
those families and authenticates against them, so it is the natural substrate
for self-hosting an equivalent.

**This spec covers Fugu v0** — the TRINITY-equivalent low-latency variant.
Fugu Ultra / Conductor (DAG workflows) is sketched as a v1 extension in §10
and intentionally out of scope here.

**Intended outcome.** A new `orchestrator` package that sits *between* HTTP
handler model resolution and `Manager.Execute*`, picks which provider family
serves the request, optionally runs a tri-role Thinker/Worker/Verifier loop on
hard tasks, falls back cleanly to today's round-robin, and emits per-decision
records that can later train a learned head. The default behavior must remain
identical to today when the orchestrator is off.

---

## 2. Non-goals

- **Training sep-CMA-ES from scratch in v0.** v0 ships with a rule-based policy
  and a stub for a learned head. Training infrastructure is sketched but not
  built.
- **A new HTTP API surface.** Clients keep calling the existing OpenAI / Claude
  / Gemini endpoints. The orchestrator is invisible to them.
- **Re-streaming or buffering frontier responses.** Once the Worker call goes
  to `ExecuteStream`, bytes go end-to-end. (Thinker/Verifier turns are
  non-streaming by design — see §5.4.)
- **Replacing per-provider account round-robin.** Account selection within a
  family stays exactly where it is (`Selector.Pick`, `scheduler.pickMixed`).
- **Workflow DAGs.** Reserved for the Conductor / Ultra variant (§10).

---

## 3. Mental model

```
        ┌──────────────────────────────────────────────────────────────┐
        │ HTTP handler (sdk/api/handlers/openai/openai_handlers.go)    │
        │   ChatCompletions → handleStreaming/handleNonStreamingResp.  │
        └──────────────────────────────┬───────────────────────────────┘
                                       │ rawJSON, modelName
                                       ▼
        ┌──────────────────────────────────────────────────────────────┐
        │ BaseAPIHandler.ExecuteWithAuthManager / ...Stream...         │
        │   sdk/api/handlers/handlers.go:536, :633                     │
        │   getRequestDetails → providers[], normalizedModel           │
        └──────────────────────────────┬───────────────────────────────┘
                                       │
                                       ▼
                ┌────────────── NEW: orchestrator.Decide ──────────────┐
                │  in:  Request{messages, model_hint, opts, providers} │
                │  out: Plan{role, provider, model, turn_budget,       │
                │             stream_ok, decision_id}                  │
                └──────────────────────────────┬───────────────────────┘
                                               │
              ┌────────────────────────────────┼────────────────────────────────┐
              │                                │                                │
              ▼                                ▼                                ▼
       single-shot                 tri-role loop (≤K turns)             passthrough
       (default, fast)             T → W → V → ACCEPT|REVISE            (orch disabled
        Worker only                 Worker turn = ExecuteStream          or model pinned)
              │                                │                                │
              └──────────────────┬─────────────┴────────────────────────────────┘
                                 ▼
                  ┌────────────────────────────────────┐
                  │ Manager.Execute / ExecuteStream    │
                  │ (sdk/cliproxy/auth/conductor.go)   │
                  │ unchanged — account round-robin    │
                  │ + cooldown + executor dispatch     │
                  └────────────────────────────────────┘
                                 │
                                 ▼
                      OnResult hook → orchestrator
                                       reward log
```

Key invariant: **the orchestrator chooses the provider family and the worker
model; `Manager` still chooses the account.** This keeps cooldowns, quota
fallbacks, and multi-account fan-out intact.

---

## 4. Insertion points in the existing codebase

All file paths are absolute under `/Users/jonayedahamed/Desktop/Projects/Personal/llm-proxy/`.

| Concern | Existing file:line | Existing function | What the orchestrator does here |
|---|---|---|---|
| Dispatch entry | `sdk/api/handlers/handlers.go:536` | `BaseAPIHandler.ExecuteWithAuthManager` | **Call `orchestrator.Decide()` before `AuthManager.Execute`** when orchestration is enabled for the request. Pass the chosen provider list + model to the existing dispatch. |
| Streaming dispatch entry | `sdk/api/handlers/handlers.go:633` | `BaseAPIHandler.ExecuteStreamWithAuthManager` | Same, except the final Worker turn calls `ExecuteStream` so SSE works end-to-end. |
| Model resolution | `sdk/api/handlers/handlers.go:849` | `BaseAPIHandler.getRequestDetails` | Unchanged. Orchestrator runs *after* this — `providers[]` is its candidate set. |
| Closest existing concept | `sdk/cliproxy/auth/conductor.go:523` | `Manager.executionModelCandidates` | Static alias table. The orchestrator is the dynamic generalization of this. Insertion is *one level above*, not here. |
| Account selection | `sdk/cliproxy/auth/selector.go:26` (`RoundRobinSelector`) and `sdk/cliproxy/auth/conductor.go:2938` (`scheduler.pickMixed`) | Round-robin / fill-first per provider | **Unchanged.** Orchestrator outputs `provider`; `Selector` picks the auth within that provider. |
| Executor contract | `sdk/cliproxy/auth/conductor.go:28-44` | `ProviderExecutor` | Unchanged. Orchestrator only chooses which executor's `Identifier()` to target. |
| Translator contract | `sdk/translator/types.go:6-34` | `RequestTransform`, `ResponseTransform` | Unchanged for end-user requests. The orchestrator's Thinker/Verifier internal prompts are constructed *post-translation* using a small JSON envelope (see §6.3). |
| Reward signal | `sdk/cliproxy/auth/conductor.go:1343` | `Manager.MarkResult` (called inside `executeMixedOnce`) | **New `Hook` implementation** (`Hook.OnResult`, conductor.go:127) consumes `Result` records, joins them to `decision_id` via context, and persists for training. |
| Config | `internal/config/config.go:28` | `Config` struct | **New top-level section** `orchestrator:` (see §7). Off by default; gated per-API-key and per-request. |
| Usage telemetry | `internal/usage/logger_plugin.go:27` | `LoggerPlugin.HandleUsage` | Extended `Record` to carry orchestrator decision metadata (provider chosen, role, turn index, decision_id). |

**No fork is required.** Every seam already exists: `Selector`, `Hook`,
`RegisterExecutor`, and the public `Manager.Execute/ExecuteStream` calls. The
orchestrator is a new package consumed by the handler layer.

---

## 5. The Fugu v0 design

### 5.1 Two modes per request

| Mode | When | Behavior |
|---|---|---|
| **Single-shot** (default) | Request flagged "easy" or orchestrator disabled | Decide once → execute on chosen provider → stream back. Latency ≈ today. |
| **Tri-role loop** | Request flagged "hard" *or* client requested `mode=ultra` *or* policy escalates | Up to `K` turns of Thinker / Worker / Verifier. Loop halts on first `ACCEPT` from a Verifier or when `K` is exhausted. Worker's final turn streams. |

A request is flagged "hard" by a cheap classifier (see §6.2). Default `K=4`.
Single-shot is the path that must never regress on latency.

### 5.2 The tri-role protocol (TRINITY-style)

Direct adaptation of the report's tri-role multi-turn loop:

- **Thinker (T)** — high-level plan, decomposition, critique of partial answer.
  Output is appended to the working transcript. Cheap model preferred.
- **Worker (W)** — produces concrete content. The Worker turn is the only one
  that streams to the client; everything before it is non-stream and discarded
  after extracting "key output."
- **Verifier (V)** — emits `ACCEPT` or `REVISE` + optional diagnosis. Halts the
  loop on `ACCEPT`.

The decision unit per turn is `(provider, role)`. The same provider may take
different roles across turns. Per the paper, **removing role selection costs
~6 pts on MATH500 and ~4.6 on RLPR** — roles are load-bearing, not cosmetic.

**Halting rule.** First `ACCEPT` wins. If `K` is exhausted without an
`ACCEPT`, the last Worker output is returned and a `partial_timeout` flag is
attached to the decision record.

### 5.3 The policy (the "coordinator")

v0 ships **two interchangeable policies** behind one interface:

```text
type Policy interface {
    Decide(ctx, state TurnState) Action
}
type Action struct {
    Provider string         // "anthropic" | "openai" | "google" | ...
    Model    string         // resolved upstream model
    Role     Role           // Thinker | Worker | Verifier
    Halt     bool           // V's ACCEPT — return Worker's last output
}
type TurnState struct {
    UserModelHint string    // what the client asked for
    Providers     []string  // candidates from existing model resolution
    Difficulty    Bucket    // easy | medium | hard, set by §6.2
    Turn          int       // 0-indexed
    History       []Turn    // role + provider + key output (short)
    Budget        int       // K
}
```

Two implementations:

1. **`rules.Policy`** — hand-written rules (the v0 default):
   - Domain heuristic from message content (regex on code blocks, math
     tokens, science vocabulary) routes to a default family:
     `code → anthropic`, `math/planning → openai`, `recall/long-context → google`.
   - For Thinker turns, prefer the cheapest provider in the candidate set.
   - For Verifier turns, prefer a *different* family from the last Worker to
     reduce same-model bias (the report's "diversity matters" finding).
   - On Verifier `ACCEPT`, halt.
   This is enough to demonstrate the architecture end-to-end and is what
   gets shipped in v0.
2. **`learned.Policy`** — a stubbed implementation backed by a Python sidecar
   over a Unix socket. v0 ships the **stub plus the wire format** so the team
   can drop in a trained head later without touching Go. See §8.

The Selector (`sdk/cliproxy/auth/selector.go`) is **not** changed — it still
picks an account within a chosen provider. The orchestrator only writes
`provider` and `model` into the request.

### 5.4 Streaming contract

- Thinker and Verifier turns are **non-streaming**: they use `Manager.Execute`,
  not `ExecuteStream`. Their outputs are summarized in-memory and appended to
  the transcript. The client never sees them.
- The **final** Worker turn (single-shot or post-`ACCEPT`) uses
  `Manager.ExecuteStream`. SSE bytes flow through unchanged — no buffering, no
  re-encoding.
- If the loop times out at `K`, the last Worker turn is **replayed** as a
  streaming call so the user gets streamed bytes. (Trade-off: cost. Mitigated
  by clamping `K` and gating loop mode behind difficulty.)

### 5.5 Budgets and stop rules

| Knob | Default | Purpose |
|---|---|---|
| `K` (max turns) | 4 | Bound worst-case cost and latency. |
| `wall_budget_ms` | 60_000 | Hard wall-clock kill switch for the loop. |
| `min_verifier_turns` | 1 | Always run at least one Verifier before halting on Worker output. |
| `escalate_on_verifier_revise` | 1 | Number of consecutive `REVISE` verdicts that escalates the next Worker to a stronger family. |
| `single_shot_fallback_ms` | 10_000 | If loop mode can't produce its first Worker turn in this window, drop to single-shot. |

These are all in config (§7). The point is that **the loop is bounded in
every dimension** — turns, wall-clock, and "if anything looks wrong, single-shot."

### 5.6 What v0 does NOT do (deferred to v1)

- Conductor / Ultra workflows (DAGs of subtasks with explicit access lists).
- A learned head trained with sep-CMA-ES on production traces. v0 ships the
  *trace recorder*; training is a separate workstream.
- Per-tenant policies. v0 has one policy per CLIProxyAPI instance, configured
  globally.
- Cost-aware decisioning (token-weighted). v0 logs cost; it doesn't optimize
  against it. v1 turns that into a reward weight.

---

## 6. Internals

### 6.1 Package layout

New code under `sdk/cliproxy/orchestrator/`:

```
sdk/cliproxy/orchestrator/
├── orchestrator.go          # public API: Orchestrator, New, Decide, Run
├── policy/
│   ├── policy.go            # Policy interface + Action/TurnState types
│   ├── rules.go             # rules-based default
│   └── learned.go           # stub for sidecar/learned head
├── difficulty/
│   └── classifier.go        # cheap difficulty bucketing (§6.2)
├── roles/
│   ├── thinker.go           # transcript-shaping helpers
│   ├── worker.go            # final-turn streaming wrapper
│   └── verifier.go          # ACCEPT/REVISE parsing
├── trace/
│   ├── recorder.go          # decision records (§8)
│   └── reward.go            # join Result → decision_id
└── config.go                # orchestrator-specific config struct
```

Why under `sdk/cliproxy/`: that is where `Manager`, `Selector`, `Hook`, and
`ProviderExecutor` already live, and the orchestrator is a peer of those —
not an HTTP concern. The handler layer just *calls* it.

### 6.2 Difficulty classifier (cheap, deterministic, no LLM)

A small heuristic that runs in microseconds per request:

| Signal | Easy | Medium | Hard |
|---|---|---|---|
| Total user-message tokens (estimated) | <500 | 500–2_500 | >2_500 |
| Number of user turns | 1 | 2–3 | ≥4 |
| Code blocks present | optional | yes | multi-file |
| Math markers (`\\(`, `$$`, `prove`, `\\sum`) | none | one | multiple |
| Explicit `tools` array | empty | small | non-trivial |

Promote one bucket up if the client sends header `X-Orchestrator-Mode: ultra`.
Demote to single-shot for `X-Orchestrator-Mode: fast`. Default mapping:

- `easy` → single-shot
- `medium` → single-shot with Verifier "shadow turn" (Verifier runs but the
  loop never re-issues the Worker — used only as a quality signal in the
  trace recorder, never gates the response)
- `hard` → full tri-role loop

### 6.3 The transcript format (internal, never leaves the proxy)

A compact JSON object kept in memory across turns:

```json
{
  "decision_id": "01J...ULID",
  "request": { "messages": [...], "model_hint": "auto", "providers": ["anthropic","openai","google"] },
  "turns": [
    { "i": 0, "role": "T", "provider": "google", "model": "gemini-3.1-pro",
      "summary": "Plan: 1) ... 2) ...", "tokens_in": 412, "tokens_out": 88, "ms": 740 },
    { "i": 1, "role": "W", "provider": "anthropic", "model": "claude-opus-4.8",
      "summary": "<256-char excerpt>", "tokens_in": 1100, "tokens_out": 540, "ms": 4200 },
    { "i": 2, "role": "V", "provider": "openai", "model": "gpt-5.5",
      "verdict": "REVISE", "diagnosis": "Step 3 is incorrect because...", "ms": 610 }
  ],
  "final": { "turn_index": 3, "halted_on": "verifier_accept" }
}
```

This is the artifact that the trace recorder writes (§8). Worker payloads
themselves are **not** stored verbatim by default — only an excerpt — to keep
the trace store small and avoid hoarding user content.

### 6.4 How a single request flows (single-shot path)

1. Handler `ChatCompletions` runs, gets `providers[]` and `normalizedModel`.
2. Handler asks `orchestrator.IsEnabled(apiKey, requestHeaders)`. If `false`,
   call existing `ExecuteWithAuthManager` / stream path **unchanged**. Done.
3. Handler calls `orchestrator.Decide(ctx, Request{...})`. Orchestrator:
   - Runs difficulty classifier → `easy`.
   - Asks `Policy.Decide` with `Turn=0, Role=Worker, Difficulty=easy`.
   - Gets back `Action{provider="anthropic", model="claude-opus-4.8", Role=Worker}`.
   - Allocates `decision_id`, opens a trace record, attaches `decision_id` to ctx.
4. Handler calls existing `ExecuteStreamWithAuthManager` with the new
   `providers=["anthropic"]` and `model="claude-opus-4.8"`.
5. `Manager` selects the account via `Selector.Pick`, executor streams bytes.
6. `Manager.MarkResult` fires; orchestrator's `Hook.OnResult` joins to
   `decision_id` and appends turn outcome (success, tokens, latency).
7. Trace record is sealed.

### 6.5 How a single request flows (tri-role path)

1. Steps 1–2 above. Difficulty = `hard`.
2. `orchestrator.Run(ctx, Request{...}, K=4)` takes over the request.
3. **Turn 0 (Thinker):** non-stream Execute. Provider per policy. Summary
   appended to transcript.
4. **Turn 1 (Worker):** non-stream Execute. Provider per policy. Output kept
   in full in memory.
5. **Turn 2 (Verifier):** non-stream Execute. Returns `ACCEPT` or `REVISE`.
   - `ACCEPT` → break and **replay** the Worker output as a streaming response
     to the client (small re-encoding cost; one-time per request).
   - `REVISE` → next turn is another Worker (or another Thinker if policy
     prefers) and goes around.
6. On `K` exhausted: stream back the last Worker output regardless.
7. `Hook.OnResult` fires per executed turn; trace is sealed at end-of-request.

Note: the **replay-the-Worker-as-stream** step (5/6) is the one part of v0
that does an LLM call we'd ideally avoid. Two options exist and either is
fine for v0:
  - **(A) Re-issue** the Worker call once `ACCEPT` returns, this time as
    `ExecuteStream`, paying ~+1 inference. Simple, low risk.
  - **(B)** Issue the Worker as `ExecuteStream` initially, buffer chunks into
    memory, then if `ACCEPT` flush them downstream, else discard. No extra
    inference, but client sees a delay before first byte.
  Default v0 is **(A)** because it preserves the "no buffering" invariant of
  CLIProxyAPI's existing streaming path. (B) is a known optimization for v1.

---

## 7. Config

New top-level section in `internal/config/config.go`. **Off by default.**

```yaml
orchestrator:
  enabled: false                 # master switch
  mode: "tri-role"               # "single-shot" | "tri-role" | "auto"
                                 # "auto" = difficulty-driven (recommended)

  # Which API keys may use the orchestrator. Empty = none. "*" = all.
  enabled-for-api-keys: []

  # Per-request override headers (read by handler):
  #   X-Orchestrator: on|off
  #   X-Orchestrator-Mode: fast|ultra
  respect-request-headers: true

  policy:
    kind: "rules"                # "rules" | "learned"
    rules:
      # Provider preferences per role/family.
      defaults:
        code: ["claude"]
        math: ["codex"]
        recall: ["gemini-cli"]
        thinker: ["gemini-cli"]
        verifier: ["codex"]
      # Per-role/family upstream MODEL pinning. Resolution order at
      # runtime: role-key (thinker|verifier) → family-key (code|math|
      # recall|general) → "default" → the user's requested model.
      models:
        thinker: "gemini-2.5-flash"
        verifier: "gpt-5"
        code: "claude-opus-4-5-20251101"
        math: "gpt-5"
        default: "gemini-2.5-pro"
      verifier-must-differ: true
    learned:                     # only consulted when kind: "learned"
      socket: "/var/run/llm-proxy/fugu.sock"
      timeout-ms: 50
      fallback-on-error: "rules" # if sidecar dies, use rules

  budgets:
    max-turns: 4
    wall-budget-ms: 60000
    min-verifier-turns: 1
    escalate-on-verifier-revise: 1
    single-shot-fallback-ms: 10000

  difficulty:
    enabled: true
    hard-token-threshold: 2500
    medium-token-threshold: 500
    promote-on-tools: false

  trace:
    enabled: true
    dir: "~/.cli-proxy-api/orchestrator-traces"
    keep-worker-excerpts-chars: 256
    keep-full-worker-payloads: false   # privacy-sensitive — opt-in
    rotate-mb: 64
```

The new fields are additive. Existing `routing:` strategy keeps working as
the **inner** layer (account choice within a chosen provider).

---

## 8. Trace recorder and the path to a learned head

The recorder writes one JSON line per request to a rolling file under
`orchestrator.trace.dir`. Schema follows §6.3 plus aggregate counters.

This is what makes the design Fugu-shaped, not just a router:

- **Each record is one episode.** A learned policy can be trained against
  these episodes offline.
- **Reward signal.** v0 ships two trivially-derived rewards: `verifier_accept`
  (1 if any Verifier in the loop ACCEPTed; else 0) and a downstream
  `client_satisfied` field optionally set by the API key holder via a follow-up
  call to a new `POST /v0/management/orchestrator/feedback/{decision_id}`
  endpoint (out of scope to ship in v0; the field exists in the schema for
  forward-compat).
- **Why this maps to sep-CMA-ES.** The report's reward is binary (`R(τ) ∈
  {0,1}`) and per-parameter gradients are low-SNR. A learned head reading the
  same context vector our handler already produces (existing token-level
  inputs + difficulty + provider candidates) and outputting `L+3` logits
  (one provider + one role per turn) is a near-direct port. We don't ship the
  trainer; we ship the trace format and the sidecar wire protocol so
  training can be a separate Python project.

**Sidecar wire protocol (learned policy).** Unix socket, length-prefixed JSON
frames. One request per turn:

```json
{ "decision_id": "...", "turn": 0, "providers": ["anthropic","openai","google"],
  "difficulty": "hard", "history": [...], "user_hint": "auto" }
```

Response:

```json
{ "provider": "openai", "model": "gpt-5.5", "role": "T", "halt": false }
```

50ms timeout, fallback to `rules.Policy`. The Go side never knows what
framework the trainer used.

---

## 9. Failure modes and fallbacks

| Failure | Behavior |
|---|---|
| Policy returns a provider not in candidate set | Treat as policy error → fall back to single-shot using `providers[0]` (today's first-match behavior). Record `policy_invalid`. |
| Thinker / Verifier turn times out | Skip that turn, keep going. If Worker has produced output, halt and stream it. Record `turn_timeout`. |
| All Worker attempts fail (within `Manager`'s own retry/cooldown) | Surface the same error code the proxy returns today. Orchestrator transparency: no new client-visible error classes in v0. |
| Wall budget hit | Stream last Worker output. Record `wall_budget_exhausted`. |
| Trace store full / unreachable | Drop trace, continue serving the request. **Never** fail a request because of telemetry. |
| Sidecar (learned policy) down | Per config: either fall back to rules or fail closed. Default = rules. |
| Streaming mid-flight error after Worker started streaming | Existing Manager behavior. Orchestrator does not interpose on bytes once streaming has started. |

---

## 10. v1 / Conductor (Fugu Ultra) — sketched, not designed here

The same architecture extends to Conductor (the workflow-emitting variant)
by replacing the per-turn `Action` with a `Workflow` plan:

```text
type Workflow struct {
    Subtasks []Subtask
    Edges    []Edge      // DAG dependencies
}
type Subtask struct {
    ID          string
    Provider    string
    Model       string
    AccessList  []string // which prior subtasks' outputs this task may read
    Role        Role
}
```

The runner becomes a topological executor with bounded concurrency. The
streaming-aware turn is whichever subtask is marked `final: true`. Everything
else in §4–§9 still applies. **Out of scope for v0.**

---

## 11. Files that will change in v0

Net-new (all in `sdk/cliproxy/orchestrator/...`) — see §6.1 for the tree.

Modified (small, surgical):

- `sdk/api/handlers/handlers.go` — wrap `ExecuteWithAuthManager` /
  `ExecuteStreamWithAuthManager` with the `orchestrator.IsEnabled` /
  `Decide` / `Run` gate. ~80 lines of glue.
- `sdk/cliproxy/auth/conductor.go` — no interface changes. The orchestrator
  registers a `Hook` and reads `Result` from `OnResult` (already exists at
  line 127). Zero source edits expected.
- `internal/config/config.go` — add the `Orchestrator` field (§7). Wire to
  the loader. ~40 lines.
- `internal/usage/logger_plugin.go` — accept `decision_id` and `role` on
  `Record` (optional fields). ~10 lines.
- `config.example.yaml` — append the §7 block, commented out.

No changes required to:

- `sdk/translator/*` — orchestrator works on already-normalized requests.
- `internal/runtime/executor/*` — executors don't know orchestration exists.
- `sdk/cliproxy/auth/selector.go` — account selection is unchanged.

---

## 12. Reusing what's already there

The design deliberately leans on existing CLIProxyAPI primitives instead of
inventing parallel machinery:

- **`ProviderExecutor` interface** (conductor.go:28-44) — what the orchestrator
  ultimately delegates each turn to. No new executor type.
- **`Selector` interface** (conductor.go:108-111) and `RoundRobinSelector`
  (selector.go:26) — account choice stays here. Orchestrator never sees auths.
- **`Hook.OnResult`** (conductor.go:127) — the reward-signal entry point. The
  orchestrator implements `Hook` and is registered alongside any existing hooks.
- **Model alias resolution** (`Manager.executionModelCandidates`,
  conductor.go:523) — runs *after* the orchestrator's decision; the
  orchestrator outputs a normalized model and the alias layer expands it to
  upstream candidates as today.
- **`LoggerPlugin`** (usage/logger_plugin.go:27) — existing telemetry; extended
  to attach `decision_id` instead of building a parallel store.
- **Streaming `StreamResult`** (cliproxy/executor/types.go:71) — used as-is
  for the final Worker turn.
- **Custom-provider extension model** (`examples/custom-provider`) — proves
  that adding the orchestrator without forking core is consistent with the
  project's stated extension story.

---

## 13. Verification plan

Build-time:

- `go build ./...` from repo root must succeed with `orchestrator.enabled: false`.
- `go test ./sdk/cliproxy/orchestrator/...` covers policy, difficulty, trace
  recorder, and the tri-role loop with a fake `ProviderExecutor` registered
  via `Manager.RegisterExecutor`.

End-to-end (manual, on a dev box with at least two real OAuth'd accounts):

1. **Default-off parity.** With `orchestrator.enabled: false`, replay a
   recorded request and assert byte-identical response vs. baseline.
2. **Single-shot.** Set `mode: single-shot`, `policy.kind: rules`, with rules
   forcing `code → anthropic`. Send a code question; confirm Anthropic served
   it (existing usage log will show this) and that a trace file appears.
3. **Tri-role accept.** Set `mode: tri-role`, send a multi-step math problem.
   Watch trace: expect `[T, W, V]` and `final.halted_on: verifier_accept`.
4. **Tri-role revise→accept.** Inject a deliberately broken first Worker
   (via a stub executor in a dev build) and confirm the Verifier returns
   `REVISE`, the loop re-issues Worker, and eventually `ACCEPT`s.
5. **Budget exhaustion.** `K=1`, `min-verifier-turns: 1` — Verifier always
   `REVISE`s. Confirm last Worker output is streamed back and
   `final.halted_on: wall_budget_exhausted` (or `turns_exhausted`).
6. **Streaming continuity.** Compare SSE event sequence for the same prompt
   under (a) orchestrator off (b) orchestrator on with single-shot — clients
   should not be able to tell the difference.
7. **Hook reward.** After 100 requests, inspect the trace dir for one JSON
   line per request, each with a non-empty `final` block.

Negative tests:

- Kill the learned-policy sidecar; with `fallback-on-error: rules` the
  request must still succeed (config §7).
- Disable trace store; requests must still succeed.

---

## 14. Open questions for the user

1. **(A) re-issue Worker vs. (B) buffer-and-flush** for streaming after a
   Verifier `ACCEPT` (see §6.5). v0 default is (A). Confirm or override.
2. Is **per-API-key gating** (`enabled-for-api-keys`) enough, or do you want
   per-route gating too (e.g. only `/v1/chat/completions`, not Claude
   `/v1/messages`)? v0 plans per-API-key only.
3. **Trace store retention.** Default in §7 is rotating files in
   `~/.cli-proxy-api/orchestrator-traces/`. Want a different sink (SQLite,
   Postgres, Redis stream)? CLIProxyAPI already has `internal/redisqueue` and
   `internal/store` if either is preferred.
4. The Fugu report (section 4+) is **truncated** in the source provided. Once
   the Conductor / self-hosting comparison sections are available, expect
   amendments to §10 and possibly §7 (Conductor-specific budgets).

---

## 15. Routing policy v2 — catalog, dynamic categories, optional LLM classifier

§7's `policy.rules.defaults` / `policy.rules.models` table is enough when you
have ~3 provider families and a coarse code/math/recall split. The moment
you bring in **many models across providers** (different versions, vision
vs. text, fast vs. strong, cheap classifiers vs. expensive reasoners), the
3-line table runs out of room. v2 extends the orchestrator with three
peer concepts:

1. **Catalog** — a list of `(provider, model)` entries enriched with tags,
   a one-line description, and tier hints. This is the knowledge base.
2. **Categories** — dynamic, user-defined task buckets. Each category has
   a natural-language instruction, optional deterministic match
   predicates (keywords, regex, token range, code-block-present,
   tools-present), an ordered `prefer` list of catalog ids, and
   per-role pins (Thinker / Worker / Verifier).
3. **Classifier** — picks one category per request. Modes:
   `heuristic` (predicates only), `llm` (cheap upstream model picks
   from the category names + instructions), or `hybrid` (heuristic
   first; LLM only when nothing matches).

The legacy `defaults`/`models` table is **kept as the fallback**. If a
request matches no category — or the catalog can't satisfy the matched
category from the current candidate set — the policy falls back to the
old code/math/recall routing exactly as today.

### 15.1 Configuration schema (additive to §7)

```yaml
orchestrator:
  enabled: true
  mode: auto

  # ... existing fields from §7 ...

  catalog:
    - id: claude-opus-4.8
      provider: claude
      model: claude-opus-4-5-20251101
      tags: [code, reasoning, long-context]
      description: "Best for software engineering, refactors, debugging."
      cost-tier: high
    - id: claude-haiku-4.5
      provider: claude
      model: claude-haiku-4-5-20251001
      tags: [cheap, fast, summarization]
      cost-tier: cheap
    - id: gpt-5-thinking
      provider: codex
      model: gpt-5-thinking
      tags: [math, planning, reasoning]
    - id: gpt-5-mini
      provider: openai-compatibility
      model: gpt-5-mini
      tags: [cheap, fast, classification]
    - id: gemini-3-pro
      provider: gemini-cli
      model: gemini-3.1-pro
      tags: [recall, long-context, multimodal]

  categories:
    - name: code-review
      instructions: "User asks to review code, find bugs, suggest refactors."
      match: { keywords: [review, refactor, bug], require-code-block: true }
      prefer: [claude-opus-4.8, gpt-5-thinking]
      role-pins:
        thinker:  claude-haiku-4.5
        worker:   claude-opus-4.8
        verifier: gpt-5-thinking

    - name: math-proof
      instructions: "User asks to prove a theorem, solve a problem, check a derivation."
      match: { keywords: [prove, theorem, derivative], regex: ["\\$\\$"] }
      prefer: [gpt-5-thinking, claude-opus-4.8]

    - name: long-context-rag
      instructions: "Long document attached, asking questions about it."
      match: { min-tokens: 30000 }
      prefer: [gemini-3-pro, claude-opus-4.8]

    - name: default
      instructions: "Everything else."
      prefer: [claude-haiku-4.5, gpt-5-mini]

  classifier:
    kind: hybrid                      # heuristic | llm | hybrid
    heuristic: { first-match-wins: true }
    llm:
      enabled: true
      provider: openai-compatibility
      model: gpt-5-mini               # cheap, fast
      timeout-ms: 800
      cache-ttl-seconds: 300          # cache by hash of user message
      max-input-chars: 4000
      fallback-on-error: heuristic    # heuristic | default-category | fail
      default-category: default
```

### 15.2 Resolution order (per turn)

1. The orchestrator pre-classifies the request **once** at request
   start and stuffs the picked category name into `TurnState.CategoryHint`.
   The same hint is reused across every turn of the tri-role loop —
   re-classifying mid-loop costs latency without providing signal.
2. `RulesPolicy.Decide` first consults `categories`:
   - Look up the category struct by name (case-insensitive).
   - For the current role, try `role-pins[role]` first; if absent or
     the pinned catalog id's provider isn't in the candidate set, walk
     `prefer` in order and pick the first id whose provider is.
   - On hit, emit `Action{Provider, Model}` from the catalog entry.
3. On miss, fall back to the legacy `classifyDomain` + `defaults` +
   `models` path (the v0 behavior, untouched).
4. Account selection inside the chosen provider still goes through the
   existing `Selector.Pick` — unchanged.

### 15.3 The LLM classifier

When `classifier.kind` is `llm` or `hybrid` and `classifier.llm.enabled`
is true, the orchestrator builds a short prompt of the form:

```
You are a routing classifier for an LLM proxy. Given a user request and
a list of category names with descriptions, pick the single best-fitting
category. Reply with ONLY the chosen category name.

Categories:
- code-review: User asks to review code, find bugs, suggest refactors.
- math-proof: User asks to prove a theorem, solve a problem, check a derivation.
- long-context-rag: Long document attached, asking questions about it.
- default: Everything else.

User request (lowercased excerpt):
<first 4000 chars of the user message>
```

That prompt is sent **through the proxy's own AuthManager** to
`classifier.llm.provider` / `classifier.llm.model` as an OpenAI-shaped
chat-completions call. The reply is normalized (trimmed, lowercased,
"category: foo" prefixes stripped, partial-substring fallback) and
matched against the configured category names.

- `timeout-ms` bounds the classifier call (default 800 ms).
- `cache-ttl-seconds` caches per `(model, user-text)` hash so repeated
  similar requests don't pay for re-classification (default 300 s).
- `fallback-on-error` selects what to do when the classifier fails,
  times out, or returns an unknown reply: `heuristic` (default — run
  the deterministic matcher), `default-category` (use
  `default-category` verbatim), or `fail` (return an empty hint and
  let the legacy domain heuristic take over).

The classifier provider **must already be in the request's candidate
provider set** — otherwise we'd ship the classifier call somewhere it
doesn't belong. When it isn't, the classifier is skipped silently and
the fallback rule fires.

### 15.4 What gets recorded

Each trace record now carries a `category` field (the picked category
name, or `""`). Combined with the existing per-turn `provider` and
`model` fields, this gives the trace store enough signal to train a
learned policy that supersedes the rules-and-LLM stack entirely (still
the v1 workstream from §8).

### 15.5 Files added/changed in v2

Net-new in `sdk/cliproxy/orchestrator/`:

- `categories.go` — catalog index + heuristic category matcher.
- `category_input.go` — payload extraction (user text, tools,
  approx-token count) shared by both classifiers.
- `classifier_llm.go` — LLM-based classifier, cache, normalization.
- `categories_test.go` — unit tests for catalog/category/classifier.

Modified (small):

- `internal/config/orchestrator.go` — `OrchestratorCatalogEntry`,
  `OrchestratorCategory`, `OrchestratorClassifierConfig` types added
  to `OrchestratorConfig`. Existing fields untouched.
- `sdk/config/config.go` — re-exports the new types.
- `sdk/cliproxy/orchestrator/config.go` — `Catalog`, `Categories`,
  `Classifier` fields on the package's `Config`; new `Default()`
  values; `Validate()` checks catalog id uniqueness and that every
  category Prefer/RolePin references an existing catalog id.
- `sdk/cliproxy/orchestrator/from_config.go` — clones the new fields
  off the YAML config.
- `sdk/cliproxy/orchestrator/policy.go` — `TurnState` gains
  `CategoryHint string`.
- `sdk/cliproxy/orchestrator/policy_rules.go` — variadic
  `NewRulesPolicy` constructor + `WithCatalog`/`WithCategories`
  functional options + category-first branch in `Decide`.
- `sdk/cliproxy/orchestrator/orchestrator.go` — `Orchestrator` holds
  `catClass` + `llmClass`; `Decide` runs `classifyCategory` once and
  ships the hint via `Decision.CategoryHint`; `runLoop` forwards the
  hint into every turn's `TurnState`.
- `sdk/cliproxy/orchestrator/trace.go` — `TraceRecord.Category`.
- `sdk/api/handlers/handlers.go` — forwards `Decision.CategoryHint`
  into `RunRequest` for both the stream and non-stream paths, and
  honors `Decision.NormalizedModel` for single-shot dispatch.
- `config.example.yaml` — full worked example in the orchestrator
  block.

### 15.6 What v2 still does NOT do

- **Per-tenant policies.** One catalog + one category list per
  CLIProxyAPI instance. v1 of multi-tenant routing is unchanged.
- **Cost-aware optimization.** `cost-tier` is recorded but does not
  yet weight selection. v1 workstream.
- **Auto-discovery of catalog entries.** Operators write the catalog
  by hand. A future enhancement could seed it from
  `AI Providers → Models` already exposed in the UI.
- **Visual config editor for v2 fields.** The existing
  `VisualConfigEditor` Orchestrator section covers v0 fields only;
  catalog/categories editing is YAML-only for now. Adding a visual
  surface is a follow-up PR.

---

## 16. Direct-model routing (v2.1)

§15's category-first scheme works, but it forces operators to write
*two* layers: a list of categories and a catalog of models. For an
operator with 50 models, hand-curating both is busywork. The simpler
mental model is: **describe each model in a paragraph and let the LLM
pick the model directly.** v2.1 adds that path without removing
anything from v2.

### 16.1 Schema additions

`OrchestratorCatalogEntry` gains two fields:

| Field          | Type     | Purpose                                                                 |
|----------------|----------|-------------------------------------------------------------------------|
| `instructions` | string   | Paragraph-level briefing the direct-model classifier reads. Falls back to `description` when blank. |
| `roles`        | []string | Soft hint for tri-role: `["thinker"]`, `["worker"]`, `["verifier"]`. Empty means "any role". |

`OrchestratorClassifierConfig.Kind` gains a new value:

- `direct-model` — LLM picks a **catalog id**, not a category name.

`Validate()` requires a non-empty `catalog` when `kind: direct-model`.

### 16.2 Wire shape

The orchestrator now ships two parallel LLM classifiers in
`sdk/cliproxy/orchestrator/`:

- `classifier_llm.go` — picks a **category** name from `categories[]`.
- `classifier_direct.go` — picks a **catalog id** from `catalog[]`.

Both reuse the same `llmCache`, `MaxInputChars`, `Timeout`, and
provider-must-be-in-candidate-set guard. The selection between them is
purely `classifier.kind`. v2.1 deliberately does NOT mix them in one
request — that keeps prompt shape, cost surface, and trace fields
predictable.

The direct-model prompt looks like:

```
[system] You are a routing classifier for an LLM proxy. Given a user
request and a catalog of available models (each with a paragraph
describing what it is best at), pick the single best model id. Reply
with ONLY the chosen id wrapped in nothing.

[user] Available models:

[claude-opus-4.8]
Claude Opus 4.8 is the best model for complex software engineering:
multi-file refactors, security audits, deep code review, debugging
tricky production issues, writing new code in any language. Prefer
over GPT for SWE.

[gpt-5-thinking]
GPT-5 Thinking is the strongest model for multi-step math and formal
reasoning: proofs, derivations, planning problems. Pick over Claude
for math.

[haiku-fast]
Fast, cheap model for summarization and the Thinker role.

User request (lowercased excerpt):
refactor this whole package and add error handling everywhere

Reply with exactly one model id from the [bracketed] headers above.
```

Reply normalization is lenient: strip brackets / quotes / `model:`
prefixes, accept any line of the reply, fall back to a substring scan
preferring the longest match. Catalog entries whose `provider` is not
in the request's candidate set are excluded from the prompt entirely.

### 16.3 Resolution order in RulesPolicy.Decide

```
priority    role                source                  result
1           RoleWorker          state.ModelCatalogID    catalog[id] -> Action
2           RoleThinker/        Roles-tagged catalog     first entry with role
            RoleVerifier        (declared order)         hint + provider OK
3           any                 state.CategoryHint       category Prefer/RolePin
4           any                 cfg.Defaults / cfg.Models legacy code/math/recall
5           any                 providers[0]             fallback
```

VerifierMustDiffer still applies at step 2 — the catalog walk excludes
the last Worker provider when possible.

### 16.4 Tri-role behavior

- **Worker** turns honor the direct-model pick (priority 1).
- **Thinker** turns walk the catalog in declared order, picking the
  first entry whose `roles` slice includes `"thinker"` (or is empty)
  and whose provider is in the candidate set. Operators put
  `roles: [thinker, classifier]` on cheap models for this purpose.
- **Verifier** turns do the same with `"verifier"`. With
  `verifier-must-differ: true`, the candidate set is pre-pruned to
  exclude the last Worker provider, then the catalog walk runs.

This means a fully direct-model config doesn't need a `categories:`
block at all — the catalog plus role hints plus the LLM classifier is
self-sufficient.

### 16.5 Trace record changes

`TraceRecord.ModelCatalogID` is new. Each request now carries either
`category` (v2 path) or `model_catalog_id` (v2.1 path) — sometimes
both when the operator runs hybrid mode.

### 16.6 Files added/changed in v2.1

Net-new:

- `sdk/cliproxy/orchestrator/classifier_direct.go` — direct-model
  classifier, prompt renderer, reply normalizer.
- `sdk/cliproxy/orchestrator/classifier_direct_test.go` — tests for
  prompt rendering, normalization, policy integration, validation.

Modified:

- `internal/config/orchestrator.go` — `OrchestratorCatalogEntry.Instructions` and `Roles` fields, `direct-model` kind documented.
- `sdk/cliproxy/orchestrator/config.go` — mirrors above, `EffectiveInstructions()` / `HasRole()` helpers, `Validate()` accepts `direct-model` and requires a catalog when used.
- `sdk/cliproxy/orchestrator/from_config.go` — clones the new fields.
- `sdk/cliproxy/orchestrator/policy.go` — `TurnState.ModelCatalogID`.
- `sdk/cliproxy/orchestrator/policy_rules.go` — new priority-1 branch
  for `ModelCatalogID`, new priority-2 branch for Roles-tagged
  Thinker/Verifier; stores a deterministic copy of the catalog slice
  for ordered iteration.
- `sdk/cliproxy/orchestrator/orchestrator.go` — adds `directClass`
  field, `classifierUsesDirectModel`, `runClassifiers` that dispatches
  to the right classifier per kind, propagates `ModelCatalogID` via
  `Decision`, `RunRequest`, `TurnState`, and the trace record.
- `sdk/cliproxy/orchestrator/trace.go` — `TraceRecord.ModelCatalogID`.
- `sdk/api/handlers/handlers.go` — forwards `Decision.ModelCatalogID`
  into both the stream and non-stream `RunRequest`s.
- `config.example.yaml` — full worked direct-model example.

### 16.7 What v2.1 still does NOT do

- **Per-role direct-model classification.** The LLM classifier only
  picks the Worker model; Thinker / Verifier rely on Roles hints. A
  three-call-per-request "Thinker model? Worker model? Verifier
  model?" mode would triple classifier cost — out of scope.
- **Mixed direct-model + category in one request.** `hybrid` still
  means "heuristic category → LLM category." We did not add a
  "heuristic category → LLM direct-model" hybrid: it's more code for
  no demonstrated need, and operators who want direct-model can put
  it behind a per-API-key gate.
- **Persisted classifier learning.** The LLM call is stateless except
  for the TTL cache. Closing the loop on trace records to fine-tune
  the classifier itself is the same v1 workstream from §8.
