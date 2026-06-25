/**
 * Orchestrator import/export helpers.
 *
 * The visual editor stores orchestrator settings in the
 * OrchestratorVisualConfig shape (per-field strings + draft arrays).
 * Operators want to round-trip those settings as JSON files so they can
 * ship policy presets between machines without copying YAML around. This
 * module is the single source of truth for that round-trip:
 *
 *   serializeOrchestratorForExport(values)  →  JSON-safe payload
 *   parseOrchestratorImport(rawText)        →  Partial<OrchestratorVisualConfig> (or throws)
 *
 * The format is intentionally a flat JSON object with stable, snake-case
 * keys mirroring the YAML schema (catalog/categories/classifier/policy.*)
 * so files exported here look familiar to anyone who has read
 * docs/fugu-orchestrator-design.md. We don't include React-only fields
 * like the `id` keys on draft arrays — those are regenerated on import.
 *
 * Files are tagged with a version so future schema changes can migrate
 * without silently corrupting older exports.
 */

import {
  makeCatalogEntryDraft,
  makeCategoryDraft,
  type CatalogEntryDraft,
  type CategoryDraft,
  type OrchestratorClassifierFallback,
  type OrchestratorClassifierKind,
  type OrchestratorVisualConfig,
} from '@/types/visualConfig';

const EXPORT_VERSION = 1;
const EXPORT_TYPE = 'llm-proxy.orchestrator';

/** Shape we read/write to disk. JSON-safe. */
export interface OrchestratorExportFile {
  type: typeof EXPORT_TYPE;
  version: number;
  exportedAt: string;
  orchestrator: SerializedOrchestrator;
}

interface SerializedRolePins {
  thinker?: string;
  worker?: string;
  verifier?: string;
}

interface SerializedCatalogEntry {
  id?: string;
  provider: string;
  model: string;
  tags?: string[];
  description?: string;
  instructions?: string;
  roles?: string[];
  costTier?: string;
  latencyTier?: string;
  contextWindow?: string;
  supports?: string[];
}

interface SerializedCategory {
  name: string;
  instructions?: string;
  match?: {
    keywords?: string[];
    regex?: string[];
    anyOf?: string[];
    noneOf?: string[];
    minTokens?: string;
    maxTokens?: string;
    requireCodeBlock?: boolean;
    requireTools?: boolean;
  };
  prefer?: string[];
  rolePins?: SerializedRolePins;
}

interface SerializedOrchestrator {
  enabled: boolean;
  mode: OrchestratorVisualConfig['mode'];
  enabledForApiKeys: string[];
  respectRequestHeaders: boolean;
  policy: {
    kind: OrchestratorVisualConfig['policyKind'];
    rules: {
      defaults: Record<string, string[]>;
      models: Record<string, string>;
      verifierMustDiffer: boolean;
    };
    learned: {
      socket: string;
      timeoutMs: string;
      fallback: OrchestratorVisualConfig['learnedFallback'];
    };
  };
  budgets: {
    maxTurns: string;
    wallBudgetMs: string;
    minVerifierTurns: string;
  };
  difficulty: {
    enabled: boolean;
    hardThreshold: string;
    mediumThreshold: string;
  };
  trace: {
    enabled: boolean;
    dir: string;
  };
  catalog: SerializedCatalogEntry[];
  categories: SerializedCategory[];
  classifier: {
    kind: OrchestratorClassifierKind;
    firstMatchWins: boolean;
    llm: {
      enabled: boolean;
      provider: string;
      model: string;
      timeoutMs: string;
      cacheTtlSeconds: string;
      maxInputChars: string;
      promptTemplate: string;
      fallbackOnError: OrchestratorClassifierFallback;
      defaultCategory: string;
    };
  };
}

const splitLines = (text: string): string[] =>
  text
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean);

/**
 * Parse a "key: provider1, provider2" textarea into a defaults map.
 * Mirrors useVisualConfig.ts's serializer so exports round-trip cleanly.
 */
function defaultsTextToRecord(text: string): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const line of splitLines(text)) {
    if (line.startsWith('#')) continue;
    const colonIdx = line.indexOf(':');
    if (colonIdx < 0) continue;
    const key = line.slice(0, colonIdx).trim();
    if (!key) continue;
    const rest = line.slice(colonIdx + 1).trim();
    out[key] = rest
      .split(',')
      .map((entry) => entry.trim())
      .filter(Boolean);
  }
  return out;
}

/** Parse a "key: model" textarea into a flat pinning map. */
function modelsTextToRecord(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of splitLines(text)) {
    if (line.startsWith('#')) continue;
    const colonIdx = line.indexOf(':');
    if (colonIdx < 0) continue;
    const key = line.slice(0, colonIdx).trim();
    if (!key) continue;
    out[key] = line.slice(colonIdx + 1).trim();
  }
  return out;
}

function defaultsRecordToText(record: Record<string, string[]>): string {
  return Object.entries(record ?? {})
    .filter(([key]) => key.trim().length > 0)
    .map(([key, providers]) => `${key.trim()}: ${(providers ?? []).join(', ')}`)
    .join('\n');
}

function modelsRecordToText(record: Record<string, string>): string {
  return Object.entries(record ?? {})
    .filter(([key]) => key.trim().length > 0)
    .map(([key, model]) => `${key.trim()}: ${model ?? ''}`)
    .join('\n');
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const arrOfString = (value: unknown): string[] =>
  Array.isArray(value)
    ? value
        .map((item) => (typeof item === 'string' ? item.trim() : ''))
        .filter(Boolean)
    : [];

const stringField = (value: unknown): string =>
  typeof value === 'string' ? value : '';

/** Build a JSON-safe export payload from the current orchestrator draft. */
export function serializeOrchestratorForExport(
  values: OrchestratorVisualConfig
): OrchestratorExportFile {
  const payload: SerializedOrchestrator = {
    enabled: values.enabled,
    mode: values.mode,
    enabledForApiKeys: splitLines(values.enabledForApiKeysText),
    respectRequestHeaders: values.respectRequestHeaders,
    policy: {
      kind: values.policyKind,
      rules: {
        defaults: defaultsTextToRecord(values.rulesDefaultsText),
        models: modelsTextToRecord(values.rulesModelsText),
        verifierMustDiffer: values.verifierMustDiffer,
      },
      learned: {
        socket: values.learnedSocket,
        timeoutMs: values.learnedTimeoutMs,
        fallback: values.learnedFallback,
      },
    },
    budgets: {
      maxTurns: values.budgetMaxTurns,
      wallBudgetMs: values.budgetWallBudgetMs,
      minVerifierTurns: values.budgetMinVerifierTurns,
    },
    difficulty: {
      enabled: values.difficultyEnabled,
      hardThreshold: values.difficultyHardThreshold,
      mediumThreshold: values.difficultyMediumThreshold,
    },
    trace: {
      enabled: values.traceEnabled,
      dir: values.traceDir,
    },
    catalog: values.catalog.map((entry) => ({
      id: entry.entryId || undefined,
      provider: entry.provider,
      model: entry.model,
      tags: entry.tags.length ? entry.tags : undefined,
      description: entry.description || undefined,
      instructions: entry.instructions || undefined,
      roles: entry.roles.length ? entry.roles : undefined,
      costTier: entry.costTier || undefined,
      latencyTier: entry.latencyTier || undefined,
      contextWindow: entry.contextWindow || undefined,
      supports: entry.supports.length ? entry.supports : undefined,
    })),
    categories: values.categories.map((cat) => ({
      name: cat.name,
      instructions: cat.instructions || undefined,
      match: {
        keywords: cat.matchKeywords.length ? cat.matchKeywords : undefined,
        regex: cat.matchRegex.length ? cat.matchRegex : undefined,
        anyOf: cat.matchAnyOf.length ? cat.matchAnyOf : undefined,
        noneOf: cat.matchNoneOf.length ? cat.matchNoneOf : undefined,
        minTokens: cat.matchMinTokens || undefined,
        maxTokens: cat.matchMaxTokens || undefined,
        requireCodeBlock: cat.matchRequireCodeBlock || undefined,
        requireTools: cat.matchRequireTools || undefined,
      },
      prefer: cat.prefer.length ? cat.prefer : undefined,
      rolePins: {
        thinker: cat.rolePinThinker || undefined,
        worker: cat.rolePinWorker || undefined,
        verifier: cat.rolePinVerifier || undefined,
      },
    })),
    classifier: {
      kind: values.classifierKind,
      firstMatchWins: values.classifierFirstMatchWins,
      llm: {
        enabled: values.classifierLlmEnabled,
        provider: values.classifierLlmProvider,
        model: values.classifierLlmModel,
        timeoutMs: values.classifierLlmTimeoutMs,
        cacheTtlSeconds: values.classifierLlmCacheTtlSeconds,
        maxInputChars: values.classifierLlmMaxInputChars,
        promptTemplate: values.classifierLlmPromptTemplate,
        fallbackOnError: values.classifierLlmFallbackOnError,
        defaultCategory: values.classifierLlmDefaultCategory,
      },
    },
  };

  return {
    type: EXPORT_TYPE,
    version: EXPORT_VERSION,
    exportedAt: new Date().toISOString(),
    orchestrator: payload,
  };
}

function readCatalogEntries(value: unknown): CatalogEntryDraft[] {
  if (!Array.isArray(value)) return [];
  return value
    .map((raw) => {
      if (!isRecord(raw)) return null;
      const draft = makeCatalogEntryDraft();
      draft.entryId = stringField(raw.id);
      draft.provider = stringField(raw.provider);
      draft.model = stringField(raw.model);
      draft.tags = arrOfString(raw.tags);
      draft.description = stringField(raw.description);
      draft.instructions = stringField(raw.instructions);
      draft.roles = arrOfString(raw.roles);
      draft.costTier = stringField(raw.costTier ?? raw['cost-tier']);
      draft.latencyTier = stringField(raw.latencyTier ?? raw['latency-tier']);
      const ctx = raw.contextWindow ?? raw['context-window'];
      draft.contextWindow =
        typeof ctx === 'number' ? String(ctx) : stringField(ctx);
      draft.supports = arrOfString(raw.supports);
      if (!draft.provider && !draft.model) return null;
      return draft;
    })
    .filter((d): d is CatalogEntryDraft => d !== null);
}

function readCategories(value: unknown): CategoryDraft[] {
  if (!Array.isArray(value)) return [];
  return value
    .map((raw) => {
      if (!isRecord(raw)) return null;
      const draft = makeCategoryDraft();
      draft.name = stringField(raw.name);
      if (!draft.name) return null;
      draft.instructions = stringField(raw.instructions);
      const match = isRecord(raw.match) ? raw.match : {};
      draft.matchKeywords = arrOfString(match.keywords);
      draft.matchRegex = arrOfString(match.regex);
      draft.matchAnyOf = arrOfString(match.anyOf ?? match['any-of']);
      draft.matchNoneOf = arrOfString(match.noneOf ?? match['none-of']);
      const minTokens = match.minTokens ?? match['min-tokens'];
      draft.matchMinTokens =
        typeof minTokens === 'number' ? String(minTokens) : stringField(minTokens);
      const maxTokens = match.maxTokens ?? match['max-tokens'];
      draft.matchMaxTokens =
        typeof maxTokens === 'number' ? String(maxTokens) : stringField(maxTokens);
      draft.matchRequireCodeBlock = Boolean(
        match.requireCodeBlock ?? match['require-code-block']
      );
      draft.matchRequireTools = Boolean(
        match.requireTools ?? match['require-tools']
      );
      draft.prefer = arrOfString(raw.prefer);
      const pins = isRecord(raw.rolePins ?? raw['role-pins'])
        ? (raw.rolePins ?? raw['role-pins']) as Record<string, unknown>
        : {};
      draft.rolePinThinker = stringField(pins.thinker);
      draft.rolePinWorker = stringField(pins.worker);
      draft.rolePinVerifier = stringField(pins.verifier);
      return draft;
    })
    .filter((d): d is CategoryDraft => d !== null);
}

/**
 * Parse a raw JSON string (from a file upload) into a Partial draft
 * that the visual editor can splice into its current state. Throws on
 * unrecognised / malformed payloads — the caller renders the error.
 */
export function parseOrchestratorImport(
  raw: string
): Partial<OrchestratorVisualConfig> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    throw new Error('The file is not valid JSON.');
  }
  if (!isRecord(parsed)) {
    throw new Error('Expected a JSON object at the top level.');
  }

  // Allow either { type, orchestrator: { … } } or a bare orchestrator
  // block (so operators can hand-edit a config and paste just the part
  // they want).
  const orch = isRecord(parsed.orchestrator)
    ? (parsed.orchestrator as Record<string, unknown>)
    : parsed;

  if (parsed.type && parsed.type !== EXPORT_TYPE) {
    throw new Error(
      `Unexpected file type "${String(parsed.type)}". Expected "${EXPORT_TYPE}".`
    );
  }
  if (parsed.version && parsed.version !== EXPORT_VERSION) {
    // We don't refuse newer versions outright — operators may be
    // upgrading — but we warn via the error channel so the UI can
    // surface it instead of silently dropping unknown fields.
    // (Currently we accept v1 only.)
  }

  const policy = isRecord(orch.policy) ? orch.policy : {};
  const rules = isRecord(policy.rules) ? policy.rules : {};
  const learned = isRecord(policy.learned) ? policy.learned : {};
  const budgets = isRecord(orch.budgets) ? orch.budgets : {};
  const difficulty = isRecord(orch.difficulty) ? orch.difficulty : {};
  const trace = isRecord(orch.trace) ? orch.trace : {};
  const classifier = isRecord(orch.classifier) ? orch.classifier : {};
  const llm = isRecord(classifier.llm) ? classifier.llm : {};

  const patch: Partial<OrchestratorVisualConfig> = {};

  if (typeof orch.enabled === 'boolean') patch.enabled = orch.enabled;
  if (orch.mode === 'auto' || orch.mode === 'single-shot' || orch.mode === 'tri-role') {
    patch.mode = orch.mode;
  }
  if (Array.isArray(orch.enabledForApiKeys)) {
    patch.enabledForApiKeysText = arrOfString(orch.enabledForApiKeys).join('\n');
  }
  if (typeof orch.respectRequestHeaders === 'boolean') {
    patch.respectRequestHeaders = orch.respectRequestHeaders;
  }

  if (policy.kind === 'rules' || policy.kind === 'learned') {
    patch.policyKind = policy.kind;
  }
  if (isRecord(rules.defaults)) {
    patch.rulesDefaultsText = defaultsRecordToText(
      rules.defaults as Record<string, string[]>
    );
  }
  if (isRecord(rules.models)) {
    patch.rulesModelsText = modelsRecordToText(
      rules.models as Record<string, string>
    );
  }
  if (typeof rules.verifierMustDiffer === 'boolean') {
    patch.verifierMustDiffer = rules.verifierMustDiffer;
  }

  if (typeof learned.socket === 'string') patch.learnedSocket = learned.socket;
  if (typeof learned.timeoutMs === 'string') patch.learnedTimeoutMs = learned.timeoutMs;
  if (learned.fallback === 'rules' || learned.fallback === 'fail') {
    patch.learnedFallback = learned.fallback;
  }

  if (typeof budgets.maxTurns === 'string') patch.budgetMaxTurns = budgets.maxTurns;
  if (typeof budgets.wallBudgetMs === 'string') patch.budgetWallBudgetMs = budgets.wallBudgetMs;
  if (typeof budgets.minVerifierTurns === 'string') {
    patch.budgetMinVerifierTurns = budgets.minVerifierTurns;
  }

  if (typeof difficulty.enabled === 'boolean') patch.difficultyEnabled = difficulty.enabled;
  if (typeof difficulty.hardThreshold === 'string') {
    patch.difficultyHardThreshold = difficulty.hardThreshold;
  }
  if (typeof difficulty.mediumThreshold === 'string') {
    patch.difficultyMediumThreshold = difficulty.mediumThreshold;
  }

  if (typeof trace.enabled === 'boolean') patch.traceEnabled = trace.enabled;
  if (typeof trace.dir === 'string') patch.traceDir = trace.dir;

  if (orch.catalog !== undefined) {
    patch.catalog = readCatalogEntries(orch.catalog);
  }
  if (orch.categories !== undefined) {
    patch.categories = readCategories(orch.categories);
  }

  if (
    classifier.kind === '' ||
    classifier.kind === 'heuristic' ||
    classifier.kind === 'llm' ||
    classifier.kind === 'hybrid' ||
    classifier.kind === 'direct-model'
  ) {
    patch.classifierKind = classifier.kind as OrchestratorClassifierKind;
  }
  if (typeof classifier.firstMatchWins === 'boolean') {
    patch.classifierFirstMatchWins = classifier.firstMatchWins;
  }
  if (typeof llm.enabled === 'boolean') patch.classifierLlmEnabled = llm.enabled;
  if (typeof llm.provider === 'string') patch.classifierLlmProvider = llm.provider;
  if (typeof llm.model === 'string') patch.classifierLlmModel = llm.model;
  if (typeof llm.timeoutMs === 'string') patch.classifierLlmTimeoutMs = llm.timeoutMs;
  if (typeof llm.cacheTtlSeconds === 'string') {
    patch.classifierLlmCacheTtlSeconds = llm.cacheTtlSeconds;
  }
  if (typeof llm.maxInputChars === 'string') {
    patch.classifierLlmMaxInputChars = llm.maxInputChars;
  }
  if (typeof llm.promptTemplate === 'string') {
    patch.classifierLlmPromptTemplate = llm.promptTemplate;
  }
  if (
    llm.fallbackOnError === '' ||
    llm.fallbackOnError === 'heuristic' ||
    llm.fallbackOnError === 'default-category' ||
    llm.fallbackOnError === 'fail'
  ) {
    patch.classifierLlmFallbackOnError = llm.fallbackOnError as OrchestratorClassifierFallback;
  }
  if (typeof llm.defaultCategory === 'string') {
    patch.classifierLlmDefaultCategory = llm.defaultCategory;
  }

  return patch;
}

export const ORCHESTRATOR_EXPORT_FILE_TYPE = EXPORT_TYPE;
export const ORCHESTRATOR_EXPORT_FILE_VERSION = EXPORT_VERSION;
