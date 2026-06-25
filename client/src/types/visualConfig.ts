export type PayloadParamValueType = 'string' | 'number' | 'boolean' | 'json';
export type PayloadParamValidationErrorCode =
  | 'payload_invalid_number'
  | 'payload_invalid_boolean'
  | 'payload_invalid_json';

export type VisualConfigFieldPath =
  | 'port'
  | 'logsMaxTotalSizeMb'
  | 'requestRetry'
  | 'maxRetryCredentials'
  | 'maxRetryInterval'
  | 'streaming.keepaliveSeconds'
  | 'streaming.bootstrapRetries'
  | 'streaming.nonstreamKeepaliveInterval';

export type VisualConfigValidationErrorCode = 'port_range' | 'non_negative_integer';

export type VisualConfigValidationErrors = Partial<
  Record<VisualConfigFieldPath, VisualConfigValidationErrorCode>
>;

export type PayloadParamEntry = {
  id: string;
  path: string;
  valueType: PayloadParamValueType;
  value: string;
};

export type PayloadModelEntry = {
  id: string;
  name: string;
  protocol?: string;
};

export type PayloadRule = {
  id: string;
  models: PayloadModelEntry[];
  params: PayloadParamEntry[];
};

export type PayloadFilterRule = {
  id: string;
  models: PayloadModelEntry[];
  params: string[];
};

export interface StreamingConfig {
  keepaliveSeconds: string;
  bootstrapRetries: string;
  nonstreamKeepaliveInterval: string;
}

export type OrchestratorMode = 'auto' | 'single-shot' | 'tri-role';
export type OrchestratorPolicyKind = 'rules' | 'learned';
export type OrchestratorLearnedFallback = 'rules' | 'fail';

/**
 * Orchestrator classifier strategy. Mirrors
 * OrchestratorClassifierConfig.Kind in internal/config/orchestrator.go.
 * Empty string means "unset" — let the orchestrator pick its default
 * (heuristic) and avoid writing an explicit key into YAML on save.
 */
export type OrchestratorClassifierKind =
  | ''
  | 'heuristic'
  | 'llm'
  | 'hybrid'
  | 'direct-model';

/**
 * Fallback strategy for the LLM classifier on error. Empty string means
 * "unset" — the orchestrator uses its built-in default (heuristic).
 */
export type OrchestratorClassifierFallback =
  | ''
  | 'heuristic'
  | 'default-category'
  | 'fail';

/**
 * CatalogEntryDraft is the editor-side shape of one
 * OrchestratorCatalogEntry from internal/config/orchestrator.go. The
 * tags/roles/supports slices are stored as plain string arrays (one
 * entry per chip in the UI). contextWindow is stored as a string so the
 * editor can hold partially-typed values; a numeric parse happens at
 * serialization time.
 */
export interface CatalogEntryDraft {
  /** Stable React key — never serialized. */
  id: string;
  entryId: string;
  provider: string;
  model: string;
  tags: string[];
  description: string;
  instructions: string;
  roles: string[];
  costTier: string;
  latencyTier: string;
  contextWindow: string;
  supports: string[];
}

/**
 * CategoryDraft is the editor-side shape of one OrchestratorCategory.
 * The Match.* fields are flattened onto this record so the form can
 * render them as siblings without nested object spreads. min/maxTokens
 * are strings to support partial input.
 */
export interface CategoryDraft {
  /** Stable React key — never serialized. */
  id: string;
  name: string;
  instructions: string;
  matchKeywords: string[];
  matchRegex: string[];
  matchRequireCodeBlock: boolean;
  matchMinTokens: string;
  matchMaxTokens: string;
  matchRequireTools: boolean;
  matchAnyOf: string[];
  matchNoneOf: string[];
  prefer: string[];
  rolePinThinker: string;
  rolePinWorker: string;
  rolePinVerifier: string;
}

// OrchestratorVisualConfig is the subset of orchestrator settings the
// visual editor can manage directly. Power users who need full control
// (per-budget fields, learned-policy tweaks beyond the basics) can drop
// to the source tab and edit YAML — the visual editor preserves keys it
// doesn't touch on save.
export interface OrchestratorVisualConfig {
  enabled: boolean;
  mode: OrchestratorMode;
  /**
   * One API key per line. The literal "*" allows every key. Empty
   * disables the orchestrator for all clients.
   */
  enabledForApiKeysText: string;
  respectRequestHeaders: boolean;

  policyKind: OrchestratorPolicyKind;
  /**
   * Plain-text representation of rules.defaults: one "key: provider1,
   * provider2" entry per line. Keys are role names (thinker | verifier)
   * or family names (code | math | recall | general | default).
   */
  rulesDefaultsText: string;
  /**
   * Plain-text representation of rules.models: one "key: model-name"
   * entry per line. Same keys as rulesDefaultsText.
   */
  rulesModelsText: string;
  verifierMustDiffer: boolean;

  learnedSocket: string;
  learnedTimeoutMs: string;
  learnedFallback: OrchestratorLearnedFallback;

  budgetMaxTurns: string;
  budgetWallBudgetMs: string;
  budgetMinVerifierTurns: string;

  difficultyEnabled: boolean;
  difficultyHardThreshold: string;
  difficultyMediumThreshold: string;

  traceEnabled: boolean;
  traceDir: string;

  /**
   * v2 catalog of upstream models the orchestrator may route to. Each
   * entry maps to one OrchestratorCatalogEntry in YAML.
   */
  catalog: CatalogEntryDraft[];

  /**
   * v2 dynamic task categories. Each entry maps to one
   * OrchestratorCategory in YAML.
   */
  categories: CategoryDraft[];

  /**
   * v2/v2.1 classifier — how requests are bucketed into a category, or
   * (in direct-model mode) mapped straight onto a catalog entry. Empty
   * strings indicate "leave the key out of YAML" so existing operator
   * configs aren't disturbed by unrelated saves.
   */
  classifierKind: OrchestratorClassifierKind;
  classifierFirstMatchWins: boolean;
  classifierLlmEnabled: boolean;
  classifierLlmProvider: string;
  classifierLlmModel: string;
  classifierLlmTimeoutMs: string;
  classifierLlmCacheTtlSeconds: string;
  classifierLlmMaxInputChars: string;
  classifierLlmPromptTemplate: string;
  classifierLlmFallbackOnError: OrchestratorClassifierFallback;
  classifierLlmDefaultCategory: string;
}

export type VisualConfigValues = {
  host: string;
  port: string;
  tlsEnable: boolean;
  tlsCert: string;
  tlsKey: string;
  rmAllowRemote: boolean;
  rmSecretKey: string;
  rmDisableControlPanel: boolean;
  rmPanelRepo: string;
  authDir: string;
  apiKeysText: string;
  debug: boolean;
  commercialMode: boolean;
  loggingToFile: boolean;
  logsMaxTotalSizeMb: string;
  usageStatisticsEnabled: boolean;
  proxyUrl: string;
  forceModelPrefix: boolean;
  requestRetry: string;
  maxRetryCredentials: string;
  maxRetryInterval: string;
  quotaSwitchProject: boolean;
  quotaSwitchPreviewModel: boolean;
  quotaAntigravityCredits: boolean;
  routingStrategy: 'round-robin' | 'fill-first';
  routingSessionAffinity: boolean;
  routingSessionAffinityTTL: string;
  wsAuth: boolean;
  payloadDefaultRules: PayloadRule[];
  payloadDefaultRawRules: PayloadRule[];
  payloadOverrideRules: PayloadRule[];
  payloadOverrideRawRules: PayloadRule[];
  payloadFilterRules: PayloadFilterRule[];
  streaming: StreamingConfig;
  orchestrator: OrchestratorVisualConfig;
};

/**
 * VisualConfigPatch is the shape consumers pass into setVisualValues /
 * VisualConfigEditor.onChange. It mirrors VisualConfigValues but lets
 * callers pass partial slices of the nested objects (StreamingConfig,
 * OrchestratorVisualConfig) without having to spread the current values
 * each time.
 */
export type VisualConfigPatch = Partial<
  Omit<VisualConfigValues, 'streaming' | 'orchestrator'>
> & {
  streaming?: Partial<StreamingConfig>;
  orchestrator?: Partial<OrchestratorVisualConfig>;
};

export const makeClientId = () => {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID();
  return `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;
};

export const DEFAULT_VISUAL_VALUES: VisualConfigValues = {
  host: '',
  port: '',
  tlsEnable: false,
  tlsCert: '',
  tlsKey: '',
  rmAllowRemote: false,
  rmSecretKey: '',
  rmDisableControlPanel: false,
  rmPanelRepo: '',
  authDir: '',
  apiKeysText: '',
  debug: false,
  commercialMode: false,
  loggingToFile: false,
  logsMaxTotalSizeMb: '',
  usageStatisticsEnabled: false,
  proxyUrl: '',
  forceModelPrefix: false,
  requestRetry: '',
  maxRetryCredentials: '',
  maxRetryInterval: '',
  quotaSwitchProject: true,
  quotaSwitchPreviewModel: true,
  quotaAntigravityCredits: false,
  routingStrategy: 'round-robin',
  routingSessionAffinity: false,
  routingSessionAffinityTTL: '',
  wsAuth: false,
  payloadDefaultRules: [],
  payloadDefaultRawRules: [],
  payloadOverrideRules: [],
  payloadOverrideRawRules: [],
  payloadFilterRules: [],
  streaming: {
    keepaliveSeconds: '',
    bootstrapRetries: '',
    nonstreamKeepaliveInterval: '',
  },
  orchestrator: {
    enabled: false,
    mode: 'auto',
    enabledForApiKeysText: '',
    respectRequestHeaders: true,
    policyKind: 'rules',
    rulesDefaultsText: '',
    rulesModelsText: '',
    verifierMustDiffer: true,
    learnedSocket: '',
    learnedTimeoutMs: '',
    learnedFallback: 'rules',
    budgetMaxTurns: '',
    budgetWallBudgetMs: '',
    budgetMinVerifierTurns: '',
    difficultyEnabled: true,
    difficultyHardThreshold: '',
    difficultyMediumThreshold: '',
    traceEnabled: true,
    traceDir: '',
    catalog: [],
    categories: [],
    classifierKind: '',
    classifierFirstMatchWins: true,
    classifierLlmEnabled: false,
    classifierLlmProvider: '',
    classifierLlmModel: '',
    classifierLlmTimeoutMs: '',
    classifierLlmCacheTtlSeconds: '',
    classifierLlmMaxInputChars: '',
    classifierLlmPromptTemplate: '',
    classifierLlmFallbackOnError: '',
    classifierLlmDefaultCategory: '',
  },
};

export const makeCatalogEntryDraft = (): CatalogEntryDraft => ({
  id: makeClientId(),
  entryId: '',
  provider: '',
  model: '',
  tags: [],
  description: '',
  instructions: '',
  roles: [],
  costTier: '',
  latencyTier: '',
  contextWindow: '',
  supports: [],
});

export const makeCategoryDraft = (): CategoryDraft => ({
  id: makeClientId(),
  name: '',
  instructions: '',
  matchKeywords: [],
  matchRegex: [],
  matchRequireCodeBlock: false,
  matchMinTokens: '',
  matchMaxTokens: '',
  matchRequireTools: false,
  matchAnyOf: [],
  matchNoneOf: [],
  prefer: [],
  rolePinThinker: '',
  rolePinWorker: '',
  rolePinVerifier: '',
});
