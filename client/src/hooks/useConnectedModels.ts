/**
 * useConnectedModels
 *
 * Aggregates every (provider, model) pair the proxy knows about into a
 * single flat list, suitable for driving a model picker dropdown in the
 * orchestrator visual editor. Sources, in priority order:
 *
 *   1. OAuth model-alias map (Record<channel, OAuthModelAliasEntry[]>)
 *      — these are the canonical model names a connected OAuth account
 *      can serve. Channel = provider key (claude | codex | gemini-cli |
 *      qwen | kimi | iflow | antigravity | aistudio | vertex).
 *
 *   2. API-key-based provider configs (claude / codex / gemini / vertex):
 *      each ProviderKeyConfig carries its own optional `models` alias
 *      list. We emit one (provider, model) pair per declared model.
 *
 *   3. OpenAI-compatibility providers: each provider has a custom name
 *      (e.g. "openrouter", "groq") and a `models` alias list. We emit
 *      one (provider=<provider.name>, model=<alias.name>) pair per
 *      declared model so the orchestrator catalog can target a specific
 *      OpenAI-compat upstream.
 *
 *   4. Auth-files list — for OAuth providers that have NO alias mapping
 *      configured yet, we still surface the channel itself so the user
 *      can free-type a model under it. We don't enumerate models from
 *      /model-definitions here because that fan-out is N requests per
 *      mount (one per channel), and the orchestrator picker only needs
 *      "the provider exists" + a free-text fallback.
 *
 * Calling code should treat this list as a *hint* — the picker always
 * accepts free text so an operator can route to a model the proxy
 * doesn't yet know about (e.g. before it's been added to an alias map).
 *
 * Caching: results are stored on a module-level cache with a short TTL
 * so successive mounts (CatalogEditor, RoleFamilyPinningEditor, ...) on
 * the same page don't refetch.
 */

import { useCallback, useEffect, useState } from 'react';
import { authFilesApi, providersApi } from '@/services/api';
import type {
  OpenAIProviderConfig,
  ProviderKeyConfig,
  GeminiKeyConfig,
  OAuthModelAliasEntry,
  AuthFileItem,
} from '@/types';

export type ConnectedModelSource =
  | 'oauth-alias'
  | 'oauth-auth-file'
  | 'api-key'
  | 'openai-compat';

export interface ConnectedModel {
  /** Provider key as the orchestrator sees it (e.g. "claude", "gemini-cli", "openrouter"). */
  provider: string;
  /** Upstream model name as it would be sent to the provider. */
  model: string;
  /** Optional alias the operator gave this model (display only). */
  alias?: string;
  /** Where this entry came from. Used for grouping/labels in the UI. */
  source: ConnectedModelSource;
  /** Optional human-readable provider label (e.g. "OpenAI compat: openrouter"). */
  providerLabel?: string;
  /** Stable id for React keys. */
  id: string;
}

export interface ConnectedProvider {
  /** Provider key (matches ConnectedModel.provider). */
  provider: string;
  /** Display label. */
  label: string;
  /** Where this entry came from. */
  source: ConnectedModelSource;
  /** Count of known models for this provider in the aggregated list. */
  modelCount: number;
}

interface ConnectedModelsCache {
  models: ConnectedModel[];
  providers: ConnectedProvider[];
  timestamp: number;
}

const CACHE_TTL_MS = 30 * 1000;
let memoryCache: ConnectedModelsCache | null = null;
let pendingFetch: Promise<ConnectedModelsCache> | null = null;

const dedupeKey = (provider: string, model: string) =>
  `${provider.toLowerCase()}::${model.toLowerCase()}`;

const pushUnique = (
  bucket: Map<string, ConnectedModel>,
  entry: ConnectedModel
): void => {
  const key = dedupeKey(entry.provider, entry.model);
  if (!entry.provider || !entry.model) return;
  if (bucket.has(key)) {
    // Upgrade an auth-file-only entry to a richer source if we now have
    // alias/provider config info for it.
    const existing = bucket.get(key)!;
    if (existing.source === 'oauth-auth-file' && entry.source !== 'oauth-auth-file') {
      bucket.set(key, { ...entry, id: existing.id });
    }
    return;
  }
  bucket.set(key, entry);
};

const collectFromAliasMap = (
  map: Record<string, OAuthModelAliasEntry[]>,
  bucket: Map<string, ConnectedModel>
): void => {
  Object.entries(map ?? {}).forEach(([rawChannel, entries]) => {
    const provider = rawChannel.trim().toLowerCase();
    if (!provider || !Array.isArray(entries)) return;
    entries.forEach((entry) => {
      const model = (entry?.name ?? '').trim();
      if (!model) return;
      pushUnique(bucket, {
        id: `oauth-alias:${provider}:${model}`,
        provider,
        model,
        alias: entry.alias && entry.alias !== model ? entry.alias : undefined,
        source: 'oauth-alias',
      });
    });
  });
};

const collectFromProviderKeyConfigs = (
  provider: string,
  configs: ProviderKeyConfig[] | GeminiKeyConfig[],
  bucket: Map<string, ConnectedModel>
): void => {
  configs.forEach((cfg, cfgIdx) => {
    (cfg.models ?? []).forEach((m, mIdx) => {
      const model = (m?.name ?? '').trim();
      if (!model) return;
      pushUnique(bucket, {
        id: `api-key:${provider}:${cfgIdx}:${mIdx}:${model}`,
        provider,
        model,
        alias: m.alias && m.alias !== model ? m.alias : undefined,
        source: 'api-key',
      });
    });
  });
};

const collectFromOpenAIProviders = (
  providers: OpenAIProviderConfig[],
  bucket: Map<string, ConnectedModel>
): void => {
  providers.forEach((p, pIdx) => {
    const provider = (p?.name ?? '').trim();
    if (!provider) return;
    const label = `OpenAI compat: ${provider}`;
    (p.models ?? []).forEach((m, mIdx) => {
      const model = (m?.name ?? '').trim();
      if (!model) return;
      pushUnique(bucket, {
        id: `openai:${provider}:${pIdx}:${mIdx}:${model}`,
        provider,
        model,
        alias: m.alias && m.alias !== model ? m.alias : undefined,
        source: 'openai-compat',
        providerLabel: label,
      });
    });
  });
};

const collectProvidersFromAuthFiles = (
  files: AuthFileItem[],
  bucket: Set<string>
): void => {
  files.forEach((file) => {
    const candidates = [file.type, file.provider]
      .filter((v): v is string => typeof v === 'string')
      .map((v) => v.trim().toLowerCase())
      .filter(Boolean);
    candidates.forEach((candidate) => {
      if (candidate === 'empty' || candidate === 'unknown') return;
      bucket.add(candidate);
    });
  });
};

const buildProviderSummary = (
  models: ConnectedModel[],
  authFileProviders: Set<string>,
  openaiProviders: OpenAIProviderConfig[]
): ConnectedProvider[] => {
  const summary = new Map<string, ConnectedProvider>();

  const ensure = (provider: string, source: ConnectedModelSource, label?: string) => {
    const key = provider.toLowerCase();
    if (!key) return;
    if (summary.has(key)) return;
    summary.set(key, {
      provider: key,
      label: label ?? provider,
      source,
      modelCount: 0,
    });
  };

  // Seed from OpenAI-compat providers so they show up even when no
  // models have been declared yet.
  openaiProviders.forEach((p) => {
    if (!p?.name) return;
    ensure(p.name, 'openai-compat', `OpenAI compat: ${p.name}`);
  });

  // Seed from auth files so OAuth channels with zero aliases still
  // appear in the dropdown.
  authFileProviders.forEach((provider) => {
    ensure(provider, 'oauth-auth-file');
  });

  // Final pass: count models and add any provider that only appeared
  // via the aggregated models list.
  models.forEach((m) => {
    const key = m.provider.toLowerCase();
    if (!key) return;
    if (!summary.has(key)) {
      summary.set(key, {
        provider: key,
        label: m.providerLabel ?? m.provider,
        source: m.source,
        modelCount: 0,
      });
    }
    summary.get(key)!.modelCount += 1;
  });

  return Array.from(summary.values()).sort((a, b) =>
    a.label.localeCompare(b.label, undefined, { sensitivity: 'accent' })
  );
};

/** Run all the network fetches in parallel and merge the results. Returns a fresh cache. */
async function fetchConnectedModels(): Promise<ConnectedModelsCache> {
  const settle = <T,>(p: Promise<T>, fallback: T): Promise<T> =>
    p.catch(() => fallback);

  const [aliasMap, claudeCfg, codexCfg, geminiCfg, vertexCfg, openaiCfg, authFiles] =
    await Promise.all([
      settle(authFilesApi.getOauthModelAlias(), {} as Record<string, OAuthModelAliasEntry[]>),
      settle(providersApi.getClaudeConfigs(), [] as ProviderKeyConfig[]),
      settle(providersApi.getCodexConfigs(), [] as ProviderKeyConfig[]),
      settle(providersApi.getGeminiKeys(), [] as GeminiKeyConfig[]),
      settle(providersApi.getVertexConfigs(), [] as ProviderKeyConfig[]),
      settle(providersApi.getOpenAIProviders(), [] as OpenAIProviderConfig[]),
      settle(authFilesApi.list(), { files: [] as AuthFileItem[] }),
    ]);

  const bucket = new Map<string, ConnectedModel>();
  collectFromAliasMap(aliasMap, bucket);
  collectFromProviderKeyConfigs('claude', claudeCfg, bucket);
  collectFromProviderKeyConfigs('codex', codexCfg, bucket);
  collectFromProviderKeyConfigs('gemini', geminiCfg, bucket);
  collectFromProviderKeyConfigs('vertex', vertexCfg, bucket);
  collectFromOpenAIProviders(openaiCfg, bucket);

  const models = Array.from(bucket.values()).sort((a, b) => {
    const providerCmp = a.provider.localeCompare(b.provider, undefined, {
      sensitivity: 'accent',
    });
    if (providerCmp !== 0) return providerCmp;
    return a.model.localeCompare(b.model, undefined, { sensitivity: 'accent' });
  });

  const authFileProviders = new Set<string>();
  collectProvidersFromAuthFiles(authFiles.files ?? [], authFileProviders);
  const providers = buildProviderSummary(models, authFileProviders, openaiCfg);

  return { models, providers, timestamp: Date.now() };
}

/** Force-invalidate the module-level cache (used by the manual refresh button). */
export function invalidateConnectedModelsCache(): void {
  memoryCache = null;
  pendingFetch = null;
}

export interface UseConnectedModelsResult {
  models: ConnectedModel[];
  providers: ConnectedProvider[];
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
}

export function useConnectedModels(): UseConnectedModelsResult {
  const [snapshot, setSnapshot] = useState<ConnectedModelsCache | null>(memoryCache);
  const [loading, setLoading] = useState<boolean>(
    !memoryCache || Date.now() - memoryCache.timestamp > CACHE_TTL_MS
  );
  const [error, setError] = useState<string | null>(null);

  const runFetch = useCallback(async (force: boolean) => {
    if (!force && memoryCache && Date.now() - memoryCache.timestamp < CACHE_TTL_MS) {
      setSnapshot(memoryCache);
      setLoading(false);
      return;
    }
    if (force) {
      memoryCache = null;
      pendingFetch = null;
    }
    setLoading(true);
    setError(null);
    try {
      if (!pendingFetch) {
        pendingFetch = fetchConnectedModels()
          .then((result) => {
            memoryCache = result;
            return result;
          })
          .finally(() => {
            pendingFetch = null;
          });
      }
      const result = await pendingFetch;
      setSnapshot(result);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load connected models');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void runFetch(false);
  }, [runFetch]);

  return {
    models: snapshot?.models ?? [],
    providers: snapshot?.providers ?? [],
    loading,
    error,
    refresh: () => runFetch(true),
  };
}
