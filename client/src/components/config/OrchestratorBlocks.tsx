/**
 * OrchestratorBlocks
 *
 * The user-friendly v2.2 orchestrator surface. Replaces the original
 * VisualConfigEditorBlocks orchestrator widgets (free-text textareas
 * for `rules.defaults` / `rules.models`, bare provider+model inputs in
 * the catalog) with components that:
 *
 *   • Discover connected models from the proxy itself (OAuth aliases,
 *     API-key providers, OpenAI-compat providers, auth files) via
 *     useConnectedModels, and surface them in a searchable dropdown so
 *     the operator picks instead of types.
 *   • Edit role / family pinning as structured rows (key + ordered
 *     provider chips + pinned model) instead of two parallel textareas.
 *   • Edit the catalog & categories as cards with the same searchable
 *     model picker driving the provider/model fields.
 *   • Round-trip the entire orchestrator block as a JSON file
 *     (Import / Export) so policy presets are portable.
 *
 * The serializer used by useVisualConfig.ts (writeOrchestratorBlock) is
 * unchanged — these components still produce `rulesDefaultsText` and
 * `rulesModelsText` in the format the existing YAML pipeline expects,
 * so the orchestrator's Go side keeps working without a backend change.
 */

import {
  memo,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import {
  IconChevronDown,
  IconDownload,
  IconPlus,
  IconRefreshCw,
  IconTrash2,
  IconUpload,
  IconX,
} from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import { downloadBlob } from '@/utils/download';
import {
  invalidateConnectedModelsCache,
  useConnectedModels,
  type ConnectedModel,
  type ConnectedProvider,
} from '@/hooks/useConnectedModels';
import {
  parseOrchestratorImport,
  serializeOrchestratorForExport,
} from '@/utils/orchestratorIO';
import type {
  CatalogEntryDraft,
  CategoryDraft,
  OrchestratorVisualConfig,
} from '@/types/visualConfig';
import {
  makeCatalogEntryDraft,
  makeCategoryDraft,
} from '@/types/visualConfig';
import styles from './VisualConfigEditor.module.scss';

/* ------------------------------------------------------------ */
/* Pinning text <-> structured row representation               */
/* ------------------------------------------------------------ */

/**
 * The two textareas the original UI exposed are the source of truth
 * for `rules.defaults` (a map of key -> ordered provider list) and
 * `rules.models` (a map of key -> single model name).
 *
 * Internally we represent each row as one PinningRow so a card can
 * edit them together — the key, its provider candidates, and the
 * pinned model. parsePinningRows() / serializePinningRows() are the
 * round-trip helpers, called whenever the user mutates a row.
 */
export interface PinningRow {
  /** Stable React key — never serialized. */
  uid: string;
  /** Pinning key (role / family name). */
  key: string;
  /** Ordered provider keys allowed for this role/family. */
  providers: string[];
  /** Pinned upstream model name (single). Empty means "no pin". */
  model: string;
}

const PINNING_ROLE_KEYS = ['thinker', 'worker', 'verifier'] as const;
const PINNING_FAMILY_KEYS = ['code', 'math', 'recall', 'general', 'default'] as const;

export type PinningKeyKind = 'role' | 'family' | 'custom';

export const pinningKeyKind = (key: string): PinningKeyKind => {
  const k = key.trim().toLowerCase();
  if ((PINNING_ROLE_KEYS as readonly string[]).includes(k)) return 'role';
  if ((PINNING_FAMILY_KEYS as readonly string[]).includes(k)) return 'family';
  return 'custom';
};

const makePinningUid = (): string => {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return globalThis.crypto.randomUUID();
  }
  return `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;
};

const parseProviderList = (rest: string): string[] =>
  rest
    .split(',')
    .map((entry) => entry.trim())
    .filter(Boolean);

/**
 * Merge a defaults text + models text into one ordered PinningRow list.
 * Row order: every key that appeared in defaults first (in order), then
 * any model-only keys after. Round-trip-safe — toPinningTexts on the
 * result returns the same two strings (minus trivia like trailing
 * whitespace).
 */
export function parsePinningRows(defaultsText: string, modelsText: string): PinningRow[] {
  const providersByKey = new Map<string, string[]>();
  const orderedKeys: string[] = [];

  for (const rawLine of defaultsText.split('\n')) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;
    const colonIdx = line.indexOf(':');
    if (colonIdx < 0) continue;
    const key = line.slice(0, colonIdx).trim();
    if (!key) continue;
    const list = parseProviderList(line.slice(colonIdx + 1));
    if (!providersByKey.has(key)) {
      orderedKeys.push(key);
    }
    providersByKey.set(key, list);
  }

  const modelByKey = new Map<string, string>();
  for (const rawLine of modelsText.split('\n')) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;
    const colonIdx = line.indexOf(':');
    if (colonIdx < 0) continue;
    const key = line.slice(0, colonIdx).trim();
    if (!key) continue;
    modelByKey.set(key, line.slice(colonIdx + 1).trim());
    if (!providersByKey.has(key)) {
      orderedKeys.push(key);
      providersByKey.set(key, []);
    }
  }

  return orderedKeys.map((key) => ({
    uid: makePinningUid(),
    key,
    providers: providersByKey.get(key) ?? [],
    model: modelByKey.get(key) ?? '',
  }));
}

/** Reverse of parsePinningRows. Skips rows with no key. */
export function toPinningTexts(rows: PinningRow[]): {
  rulesDefaultsText: string;
  rulesModelsText: string;
} {
  const defaultsLines: string[] = [];
  const modelLines: string[] = [];
  for (const row of rows) {
    const key = row.key.trim();
    if (!key) continue;
    // Always emit a defaults line — even when no providers — so the YAML
    // serializer still records the key intent. An empty list serializes
    // as an empty array; the policy treats it as "no constraint".
    defaultsLines.push(`${key}: ${row.providers.filter(Boolean).join(', ')}`);
    const model = row.model.trim();
    if (model) {
      modelLines.push(`${key}: ${model}`);
    }
  }
  return {
    rulesDefaultsText: defaultsLines.join('\n'),
    rulesModelsText: modelLines.join('\n'),
  };
}

/* ------------------------------------------------------------ */
/* ConnectedModelPicker — searchable combobox for (provider+model). */
/* ------------------------------------------------------------ */

interface ConnectedModelPickerValue {
  provider: string;
  model: string;
}

interface ConnectedModelPickerProps {
  value: ConnectedModelPickerValue;
  models: ConnectedModel[];
  /** Optional filter — limit dropdown options to this provider key. */
  providerFilter?: string;
  /** Optional list of provider keys the operator might want to add free-text models under. */
  knownProviders?: ConnectedProvider[];
  placeholder?: string;
  ariaLabel?: string;
  disabled?: boolean;
  /** Show only model names; useful when provider is fixed (e.g. role-pinning row). */
  modelOnly?: boolean;
  onChange: (next: ConnectedModelPickerValue) => void;
}

interface NormalizedOption {
  provider: string;
  model: string;
  /** What we display as the option's primary text. */
  label: string;
  /** A free-form sub-line (e.g. provider / source / alias). */
  meta?: string;
  /** Lower-case search text. */
  searchKey: string;
}

const buildOptions = (
  models: ConnectedModel[],
  providerFilter?: string,
  modelOnly?: boolean
): NormalizedOption[] => {
  const out: NormalizedOption[] = [];
  for (const entry of models) {
    if (providerFilter && entry.provider !== providerFilter) continue;
    const provider = entry.provider;
    const model = entry.model;
    const label = modelOnly ? model : `${provider} · ${model}`;
    const aliasNote = entry.alias ? `alias: ${entry.alias}` : undefined;
    const sourceNote =
      entry.source === 'openai-compat'
        ? 'OpenAI-compat'
        : entry.source === 'api-key'
          ? 'API key'
          : entry.source === 'oauth-alias'
            ? 'OAuth alias'
            : 'OAuth';
    const meta = modelOnly
      ? [aliasNote, sourceNote].filter(Boolean).join(' · ')
      : [aliasNote, sourceNote].filter(Boolean).join(' · ');
    out.push({
      provider,
      model,
      label,
      meta: meta || undefined,
      searchKey: `${provider} ${model} ${entry.alias ?? ''}`.toLowerCase(),
    });
  }
  return out;
};

export const ConnectedModelPicker = memo(function ConnectedModelPicker({
  value,
  models,
  providerFilter,
  knownProviders,
  placeholder,
  ariaLabel,
  disabled,
  modelOnly,
  onChange,
}: ConnectedModelPickerProps) {
  const { t } = useTranslation();
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const dropdownRef = useRef<HTMLDivElement | null>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [highlightedIndex, setHighlightedIndex] = useState(-1);
  const [dropdownStyle, setDropdownStyle] = useState<React.CSSProperties | null>(null);

  const displayValue = useMemo(() => {
    if (!value.model && !value.provider) return '';
    if (modelOnly) return value.model;
    if (!value.model) return value.provider;
    if (!value.provider) return value.model;
    return `${value.provider} · ${value.model}`;
  }, [value, modelOnly]);

  // While the dropdown is closed, show the committed value as input
  // text. When open, the operator types to filter — we show the query
  // verbatim and reset on close.
  const effectiveQuery = open ? query : displayValue;

  const options = useMemo(
    () => buildOptions(models, providerFilter, modelOnly),
    [models, providerFilter, modelOnly]
  );

  const filtered = useMemo(() => {
    const q = (open ? query : '').trim().toLowerCase();
    if (!q) return options;
    return options.filter((opt) => opt.searchKey.includes(q));
  }, [options, query, open]);

  const grouped = useMemo(() => {
    if (modelOnly) {
      return [{ provider: providerFilter ?? '', options: filtered }];
    }
    const map = new Map<string, NormalizedOption[]>();
    for (const opt of filtered) {
      if (!map.has(opt.provider)) map.set(opt.provider, []);
      map.get(opt.provider)!.push(opt);
    }
    return Array.from(map.entries()).map(([provider, opts]) => ({ provider, options: opts }));
  }, [filtered, modelOnly, providerFilter]);

  const flat = useMemo(() => grouped.flatMap((g) => g.options), [grouped]);

  const updateDropdownStyle = useCallback(() => {
    if (!wrapRef.current) return;
    const rect = wrapRef.current.getBoundingClientRect();
    const viewportHeight = window.innerHeight;
    const spaceBelow = viewportHeight - rect.bottom - 12;
    const direction = spaceBelow > 220 ? 'down' : 'up';
    const maxHeight = Math.max(160, Math.min(360, direction === 'down' ? spaceBelow : rect.top - 12));
    setDropdownStyle(
      direction === 'down'
        ? {
            top: rect.bottom + 6,
            left: rect.left,
            width: rect.width,
            maxHeight,
          }
        : {
            top: 'auto',
            bottom: viewportHeight - rect.top + 6,
            left: rect.left,
            width: rect.width,
            maxHeight,
          }
    );
  }, []);

  // Position the portalled dropdown against the trigger before paint to
  // avoid a one-frame flicker, then keep it in sync with viewport/scroll.
  useLayoutEffect(() => {
    if (!open) return undefined;
    updateDropdownStyle();
    const handle = () => updateDropdownStyle();
    window.addEventListener('resize', handle);
    window.addEventListener('scroll', handle, true);
    return () => {
      window.removeEventListener('resize', handle);
      window.removeEventListener('scroll', handle, true);
    };
  }, [open, updateDropdownStyle]);

  useEffect(() => {
    if (!open) return undefined;
    const handleClick = (event: MouseEvent) => {
      const target = event.target as Node;
      if (wrapRef.current?.contains(target)) return;
      if (dropdownRef.current?.contains(target)) return;
      setOpen(false);
    };
    document.addEventListener('mousedown', handleClick);
    return () => document.removeEventListener('mousedown', handleClick);
  }, [open]);

  const commit = useCallback(
    (next: ConnectedModelPickerValue) => {
      onChange(next);
      setOpen(false);
      setQuery('');
      setHighlightedIndex(-1);
    },
    [onChange]
  );

  const handleSelectOption = useCallback(
    (opt: NormalizedOption) => {
      commit({ provider: opt.provider, model: opt.model });
    },
    [commit]
  );

  const handleQueryChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    const nextQuery = event.target.value;
    setQuery(nextQuery);
    setHighlightedIndex(-1);
    setOpen(true);
  };

  const handleKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (disabled) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setOpen(true);
      setHighlightedIndex((prev) => Math.min(prev + 1, flat.length - 1));
      return;
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault();
      setHighlightedIndex((prev) => Math.max(prev - 1, 0));
      return;
    }
    if (event.key === 'Enter') {
      if (open && highlightedIndex >= 0 && highlightedIndex < flat.length) {
        event.preventDefault();
        handleSelectOption(flat[highlightedIndex]);
        return;
      }
      // Commit free-typed text — useful when the model is not yet in
      // the dropdown (e.g. just-added catalog entry).
      const trimmed = query.trim();
      if (trimmed) {
        event.preventDefault();
        // If there's a single matching option, pick it. Otherwise treat
        // it as free text and split on the first "·" or "/" delimiter.
        const splitMatch = trimmed.split(/\s*[·/]\s*/);
        if (splitMatch.length >= 2) {
          commit({ provider: splitMatch[0], model: splitMatch.slice(1).join(' / ') });
        } else if (modelOnly && providerFilter) {
          commit({ provider: providerFilter, model: trimmed });
        } else {
          commit({ provider: value.provider, model: trimmed });
        }
      } else {
        setOpen(false);
      }
      return;
    }
    if (event.key === 'Escape') {
      setOpen(false);
      setQuery('');
    }
  };

  const isUnknownEntry = useMemo(() => {
    if (!value.model) return false;
    return !models.some(
      (m) =>
        m.provider.toLowerCase() === value.provider.toLowerCase() &&
        m.model.toLowerCase() === value.model.toLowerCase()
    );
  }, [models, value]);

  const placeholderLabel = placeholder
    ?? t('config_management.visual.sections.orchestrator.picker_placeholder', {
      defaultValue: 'Pick a connected model or type a new one',
    });

  const dropdown =
    open && dropdownStyle ? (
      <div
        ref={dropdownRef}
        className={styles.modelPickerDropdown}
        style={dropdownStyle}
        role="listbox"
        aria-label={ariaLabel ?? placeholderLabel}
      >
        {flat.length === 0 ? (
          <div className={styles.modelPickerEmpty}>
            {t('config_management.visual.sections.orchestrator.picker_no_results', {
              defaultValue: 'No matching connected models. Press Enter to use the typed value.',
            })}
          </div>
        ) : (
          grouped.map((group) => (
            <div key={group.provider || 'all'}>
              {!modelOnly && group.provider ? (
                <div className={styles.modelPickerGroupLabel}>{group.provider}</div>
              ) : null}
              {group.options.map((opt) => {
                const globalIndex = flat.indexOf(opt);
                const active = opt.provider === value.provider && opt.model === value.model;
                const highlighted = globalIndex === highlightedIndex;
                return (
                  <button
                    key={`${opt.provider}::${opt.model}`}
                    type="button"
                    role="option"
                    aria-selected={active}
                    className={[
                      styles.modelPickerOption,
                      highlighted ? styles.modelPickerOptionHighlighted : '',
                      active ? styles.modelPickerOptionActive : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                    onMouseEnter={() => setHighlightedIndex(globalIndex)}
                    onMouseDown={(e) => {
                      // Prevent input blur before click registers.
                      e.preventDefault();
                    }}
                    onClick={() => handleSelectOption(opt)}
                  >
                    <span className={styles.modelPickerOptionLabel}>{opt.label}</span>
                    {opt.meta ? (
                      <span className={styles.modelPickerOptionMeta}>{opt.meta}</span>
                    ) : null}
                  </button>
                );
              })}
            </div>
          ))
        )}
        {knownProviders && knownProviders.length > 0 && !modelOnly ? (
          <div className={styles.modelPickerCustom}>
            {t('config_management.visual.sections.orchestrator.picker_hint', {
              defaultValue:
                'Tip: type "provider · model" to add a model the proxy hasn\'t indexed yet.',
            })}
          </div>
        ) : null}
      </div>
    ) : null;

  return (
    <div className={styles.modelPicker} ref={wrapRef}>
      <input
        ref={inputRef}
        type="text"
        className={`input ${styles.modelPickerInput}`}
        placeholder={placeholderLabel}
        aria-label={ariaLabel ?? placeholderLabel}
        value={effectiveQuery}
        disabled={disabled}
        onFocus={() => {
          setQuery('');
          setOpen(true);
        }}
        onChange={handleQueryChange}
        onKeyDown={handleKeyDown}
      />
      {isUnknownEntry ? (
        <div className={`${styles.modelPickerMeta} ${styles.modelPickerWarn}`}>
          {t('config_management.visual.sections.orchestrator.picker_unverified', {
            defaultValue:
              'This model is not currently connected. The orchestrator will still route to it if a provider serves it at runtime.',
          })}
        </div>
      ) : null}
      {dropdown && typeof document !== 'undefined'
        ? createPortal(dropdown, document.body)
        : null}
    </div>
  );
});

/* ------------------------------------------------------------ */
/* ProviderChipPicker — multi-select for the provider list per row. */
/* ------------------------------------------------------------ */

interface ProviderChipPickerProps {
  value: string[];
  providers: ConnectedProvider[];
  disabled?: boolean;
  onChange: (next: string[]) => void;
  ariaLabel?: string;
}

const ProviderChipPicker = memo(function ProviderChipPicker({
  value,
  providers,
  disabled,
  onChange,
  ariaLabel,
}: ProviderChipPickerProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState('');
  const inputId = useId();

  const addProvider = useCallback(
    (provider: string) => {
      const next = provider.trim().toLowerCase();
      if (!next) return;
      if (value.some((existing) => existing.toLowerCase() === next)) return;
      onChange([...value, next]);
      setDraft('');
    },
    [onChange, value]
  );

  const removeProvider = useCallback(
    (provider: string) => {
      onChange(value.filter((existing) => existing.toLowerCase() !== provider.toLowerCase()));
    },
    [onChange, value]
  );

  const datalistId = `${inputId}-providers`;

  return (
    <div className={styles.pinningProviderChips}>
      {value.map((provider) => (
        <span key={provider} className={styles.pinningProviderChip}>
          {provider}
          {!disabled ? (
            <button
              type="button"
              className={styles.pinningProviderChipRemove}
              aria-label={t('config_management.visual.sections.orchestrator.remove_provider', {
                defaultValue: 'Remove provider',
              })}
              onClick={() => removeProvider(provider)}
            >
              <IconX size={12} />
            </button>
          ) : null}
        </span>
      ))}
      <input
        type="text"
        className="input"
        list={datalistId}
        value={draft}
        disabled={disabled}
        placeholder={
          value.length === 0
            ? t('config_management.visual.sections.orchestrator.add_provider_placeholder', {
                defaultValue: 'add a provider (claude, codex, openrouter, ...)',
              })
            : t('config_management.visual.sections.orchestrator.add_more_provider', {
                defaultValue: '+ provider',
              })
        }
        aria-label={ariaLabel}
        style={{ flex: '1 1 140px', minWidth: 100 }}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ',') {
            e.preventDefault();
            addProvider(draft);
          } else if (e.key === 'Backspace' && !draft && value.length) {
            e.preventDefault();
            onChange(value.slice(0, -1));
          }
        }}
        onBlur={() => {
          if (draft.trim()) addProvider(draft);
        }}
      />
      <datalist id={datalistId}>
        {providers.map((p) => (
          <option key={p.provider} value={p.provider}>
            {p.label}
          </option>
        ))}
      </datalist>
    </div>
  );
});

/* ------------------------------------------------------------ */
/* RoleFamilyPinningEditor — the structured replacement for the */
/* original `Providers per role/family` + `Model pinning`       */
/* textareas in the orchestrator section.                       */
/* ------------------------------------------------------------ */

interface RoleFamilyPinningEditorProps {
  values: OrchestratorVisualConfig;
  models: ConnectedModel[];
  providers: ConnectedProvider[];
  disabled?: boolean;
  onChange: (patch: {
    rulesDefaultsText: string;
    rulesModelsText: string;
  }) => void;
}

export function RoleFamilyPinningEditor({
  values,
  models,
  providers,
  disabled,
  onChange,
}: RoleFamilyPinningEditorProps) {
  const { t } = useTranslation();
  const rows = useMemo(
    () => parsePinningRows(values.rulesDefaultsText, values.rulesModelsText),
    [values.rulesDefaultsText, values.rulesModelsText]
  );

  const emit = useCallback(
    (nextRows: PinningRow[]) => {
      const { rulesDefaultsText, rulesModelsText } = toPinningTexts(nextRows);
      onChange({ rulesDefaultsText, rulesModelsText });
    },
    [onChange]
  );

  const updateRow = (uid: string, patch: Partial<PinningRow>) => {
    emit(rows.map((row) => (row.uid === uid ? { ...row, ...patch } : row)));
  };

  const removeRow = (uid: string) => {
    emit(rows.filter((row) => row.uid !== uid));
  };

  const addRowWithKey = (key: string) => {
    if (!key.trim()) return;
    if (rows.some((row) => row.key.trim().toLowerCase() === key.trim().toLowerCase())) return;
    emit([
      ...rows,
      {
        uid: makePinningUid(),
        key: key.trim(),
        providers: [],
        model: '',
      },
    ]);
  };

  const addCustomRow = () => {
    emit([
      ...rows,
      {
        uid: makePinningUid(),
        key: '',
        providers: [],
        model: '',
      },
    ]);
  };

  const presetKeys = useMemo(() => {
    const taken = new Set(rows.map((r) => r.key.trim().toLowerCase()));
    return {
      roles: PINNING_ROLE_KEYS.filter((k) => !taken.has(k)),
      families: PINNING_FAMILY_KEYS.filter((k) => !taken.has(k)),
    };
  }, [rows]);

  return (
    <div className={styles.blockStack}>
      <div className={styles.orchPresetGroup}>
        {presetKeys.roles.map((key) => (
          <button
            key={key}
            type="button"
            className={styles.orchPresetButton}
            disabled={disabled}
            onClick={() => addRowWithKey(key)}
          >
            <IconPlus size={12} /> {key}
          </button>
        ))}
        {presetKeys.families.map((key) => (
          <button
            key={key}
            type="button"
            className={styles.orchPresetButton}
            disabled={disabled}
            onClick={() => addRowWithKey(key)}
          >
            <IconPlus size={12} /> {key}
          </button>
        ))}
        <button
          type="button"
          className={styles.orchPresetButton}
          disabled={disabled}
          onClick={addCustomRow}
        >
          <IconPlus size={12} />{' '}
          {t('config_management.visual.sections.orchestrator.add_custom_pin', {
            defaultValue: 'custom key',
          })}
        </button>
      </div>

      {rows.length === 0 ? (
        <div className={styles.emptyState}>
          {t('config_management.visual.sections.orchestrator.pinning_empty', {
            defaultValue:
              'No pins yet. Tap a preset chip above (thinker / verifier / code / math / recall / default) or add a custom key. Each row sets which providers may serve that role/family and pins one upstream model.',
          })}
        </div>
      ) : null}

      {rows.map((row) => {
        const kind = pinningKeyKind(row.key);
        return (
          <div key={row.uid} className={styles.pinningRow}>
            <div className={styles.blockStack}>
              <div className={styles.pinningRowHeader}>
                <span
                  className={[
                    styles.pinningKeyKind,
                    kind === 'family' ? styles.pinningKeyKindFamily : '',
                    kind === 'custom' ? styles.pinningKeyKindCustom : '',
                  ]
                    .filter(Boolean)
                    .join(' ')}
                >
                  {kind}
                </span>
              </div>
              <input
                type="text"
                className="input"
                value={row.key}
                placeholder={t(
                  'config_management.visual.sections.orchestrator.pinning_key_placeholder',
                  {
                    defaultValue: 'role or family key',
                  }
                )}
                aria-label={t('config_management.visual.sections.orchestrator.pinning_key_label', {
                  defaultValue: 'Pinning key',
                })}
                disabled={disabled}
                onChange={(e) => updateRow(row.uid, { key: e.target.value })}
              />
            </div>

            <div>
              <div className={styles.blockLabel}>
                {t('config_management.visual.sections.orchestrator.pinning_providers_label', {
                  defaultValue: 'Allowed providers (ordered)',
                })}
              </div>
              <ProviderChipPicker
                value={row.providers}
                providers={providers}
                disabled={disabled}
                onChange={(nextProviders) => updateRow(row.uid, { providers: nextProviders })}
                ariaLabel={t('config_management.visual.sections.orchestrator.pinning_providers_aria', {
                  defaultValue: 'Providers allowed to serve this key',
                })}
              />
            </div>

            <div>
              <div className={styles.blockLabel}>
                {t('config_management.visual.sections.orchestrator.pinning_model_label', {
                  defaultValue: 'Pinned model (single)',
                })}
              </div>
              <ConnectedModelPicker
                value={{ provider: row.providers[0] ?? '', model: row.model }}
                models={models}
                disabled={disabled}
                modelOnly
                providerFilter={row.providers[0]}
                onChange={(next) => updateRow(row.uid, { model: next.model })}
                placeholder={t(
                  'config_management.visual.sections.orchestrator.pinning_model_placeholder',
                  { defaultValue: 'pick a model (optional)' }
                )}
              />
            </div>

            <div>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => removeRow(row.uid)}
                disabled={disabled}
                aria-label={t('config_management.visual.common.delete')}
              >
                <IconTrash2 size={14} />
              </Button>
            </div>
          </div>
        );
      })}
    </div>
  );
}

/* ------------------------------------------------------------ */
/* OrchestratorImportExport — file-based round-trip of the      */
/* orchestrator block as JSON.                                  */
/* ------------------------------------------------------------ */

interface OrchestratorImportExportProps {
  values: OrchestratorVisualConfig;
  disabled?: boolean;
  refreshing?: boolean;
  onImport: (patch: Partial<OrchestratorVisualConfig>) => void;
  onRefresh?: () => void;
}

export function OrchestratorImportExport({
  values,
  disabled,
  refreshing,
  onImport,
  onRefresh,
}: OrchestratorImportExportProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  const handleExport = useCallback(() => {
    const payload = serializeOrchestratorForExport(values);
    const json = JSON.stringify(payload, null, 2);
    const blob = new Blob([json], { type: 'application/json' });
    const ts = new Date().toISOString().replace(/[:.]/g, '-');
    downloadBlob({
      filename: `orchestrator-${ts}.json`,
      blob,
    });
    showNotification(
      t('config_management.visual.sections.orchestrator.export_success', {
        defaultValue: 'Orchestrator settings exported.',
      }),
      'success'
    );
  }, [values, showNotification, t]);

  const handleFileSelected = useCallback(
    async (file: File) => {
      try {
        const text = await file.text();
        const patch = parseOrchestratorImport(text);
        onImport(patch);
        showNotification(
          t('config_management.visual.sections.orchestrator.import_success', {
            defaultValue: 'Orchestrator settings imported. Review and save to apply.',
          }),
          'success'
        );
      } catch (err) {
        const message =
          err instanceof Error ? err.message : 'Failed to import orchestrator settings.';
        showNotification(
          `${t('config_management.visual.sections.orchestrator.import_failed', {
            defaultValue: 'Import failed',
          })}: ${message}`,
          'error'
        );
      } finally {
        if (fileInputRef.current) {
          fileInputRef.current.value = '';
        }
      }
    },
    [onImport, showNotification, t]
  );

  return (
    <div className={styles.orchToolbar}>
      <div className={styles.orchToolbarHint}>
        {t('config_management.visual.sections.orchestrator.toolbar_hint', {
          defaultValue:
            'Export the orchestrator block as JSON to share a preset, or import one to overwrite the current draft. Refresh re-reads the list of connected models from the proxy.',
        })}
      </div>
      {onRefresh ? (
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            invalidateConnectedModelsCache();
            onRefresh();
          }}
          disabled={disabled || refreshing}
        >
          <IconRefreshCw size={14} />{' '}
          {t('config_management.visual.sections.orchestrator.refresh_models', {
            defaultValue: 'Refresh models',
          })}
        </Button>
      ) : null}
      <Button variant="ghost" size="sm" onClick={handleExport} disabled={disabled}>
        <IconDownload size={14} />{' '}
        {t('config_management.visual.sections.orchestrator.export_button', {
          defaultValue: 'Export',
        })}
      </Button>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => fileInputRef.current?.click()}
        disabled={disabled}
      >
        <IconUpload size={14} />{' '}
        {t('config_management.visual.sections.orchestrator.import_button', {
          defaultValue: 'Import',
        })}
      </Button>
      <input
        ref={fileInputRef}
        type="file"
        accept="application/json,.json"
        style={{ display: 'none' }}
        onChange={(event) => {
          const file = event.target.files?.[0];
          if (file) void handleFileSelected(file);
        }}
      />
    </div>
  );
}

/* ------------------------------------------------------------ */
/* CatalogEditorV2 — upgraded catalog cards with a model picker. */
/* ------------------------------------------------------------ */

const ChipsField = memo(function ChipsField({
  value,
  placeholder,
  ariaLabel,
  disabled,
  onChange,
}: {
  value: string[];
  placeholder?: string;
  ariaLabel?: string;
  disabled?: boolean;
  onChange: (next: string[]) => void;
}) {
  const joined = useMemo(() => value.join(', '), [value]);
  const [buffer, setBuffer] = useState<string | null>(null);
  const display = buffer ?? joined;
  const commit = useCallback(
    (raw: string) => {
      const next = raw
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
      onChange(next);
    },
    [onChange]
  );
  return (
    <input
      className="input"
      placeholder={placeholder}
      aria-label={ariaLabel ?? placeholder}
      value={display}
      disabled={disabled}
      onFocus={() => setBuffer(joined)}
      onChange={(e) => setBuffer(e.target.value)}
      onBlur={(e) => {
        commit(e.target.value);
        setBuffer(null);
      }}
    />
  );
});

interface CatalogEditorV2Props {
  value: CatalogEntryDraft[];
  models: ConnectedModel[];
  providers: ConnectedProvider[];
  disabled?: boolean;
  onChange: (next: CatalogEntryDraft[]) => void;
}

export const CatalogEditorV2 = memo(function CatalogEditorV2({
  value,
  models,
  providers: _providers,
  disabled,
  onChange,
}: CatalogEditorV2Props) {
  const { t } = useTranslation();
  const entries = value;

  const update = (index: number, patch: Partial<CatalogEntryDraft>) =>
    onChange(entries.map((entry, i) => (i === index ? { ...entry, ...patch } : entry)));
  const remove = (index: number) => onChange(entries.filter((_, i) => i !== index));
  const add = () => onChange([...entries, makeCatalogEntryDraft()]);

  return (
    <div className={styles.blockStack}>
      {entries.map((entry, index) => {
        const autoId = entry.entryId || entry.model || `entry-${index + 1}`;
        return (
          <div key={entry.id} className={styles.ruleCard}>
            <div className={styles.ruleCardHeader}>
              <div className={styles.ruleCardTitle}>
                {t('config_management.visual.sections.orchestrator.catalog_entry', {
                  defaultValue: 'Catalog entry {{n}}',
                  n: index + 1,
                })}
              </div>
              <Button variant="ghost" size="sm" onClick={() => remove(index)} disabled={disabled}>
                {t('config_management.visual.common.delete')}
              </Button>
            </div>

            <div className={styles.blockStack}>
              <div>
                <div className={styles.blockLabel}>
                  {t('config_management.visual.sections.orchestrator.catalog_picker_label', {
                    defaultValue: 'Connected model',
                  })}
                </div>
                <ConnectedModelPicker
                  value={{ provider: entry.provider, model: entry.model }}
                  models={models}
                  disabled={disabled}
                  onChange={(next) =>
                    update(index, { provider: next.provider, model: next.model })
                  }
                />
              </div>

              <div className={styles.stringListRow}>
                <input
                  className="input"
                  placeholder={t(
                    'config_management.visual.sections.orchestrator.catalog_id_ph',
                    { defaultValue: 'id (defaults to model name)' }
                  )}
                  aria-label={t('config_management.visual.sections.orchestrator.catalog_id', {
                    defaultValue: 'Catalog id',
                  })}
                  value={entry.entryId}
                  disabled={disabled}
                  onChange={(e) => update(index, { entryId: e.target.value })}
                  style={{ flex: '1 1 200px' }}
                />
                <div className={styles.orchSummaryChip}>id: {autoId}</div>
              </div>

              <input
                className="input"
                placeholder={t('config_management.visual.sections.orchestrator.catalog_desc_ph', {
                  defaultValue: 'one-line description / specialty',
                })}
                aria-label={t('config_management.visual.sections.orchestrator.catalog_desc', {
                  defaultValue: 'Description',
                })}
                value={entry.description}
                disabled={disabled}
                onChange={(e) => update(index, { description: e.target.value })}
              />

              <textarea
                className="input"
                rows={3}
                placeholder={t(
                  'config_management.visual.sections.orchestrator.catalog_instructions_ph',
                  {
                    defaultValue:
                      "Direct-model routing brief: what is this model uniquely good at? When should the router prefer it? When should it avoid it?",
                  }
                )}
                aria-label={t(
                  'config_management.visual.sections.orchestrator.catalog_instructions',
                  { defaultValue: 'Instructions' }
                )}
                value={entry.instructions}
                disabled={disabled}
                onChange={(e) => update(index, { instructions: e.target.value })}
              />

              <ChipsField
                value={entry.tags}
                placeholder={t('config_management.visual.sections.orchestrator.catalog_tags_ph', {
                  defaultValue: 'tags (comma-separated): code, vision, math, long-context, ...',
                })}
                ariaLabel="Tags"
                disabled={disabled}
                onChange={(tags) => update(index, { tags })}
              />

              <ChipsField
                value={entry.roles}
                placeholder={t('config_management.visual.sections.orchestrator.catalog_roles_ph', {
                  defaultValue: 'roles (comma-separated): thinker, worker, verifier, classifier',
                })}
                ariaLabel="Roles"
                disabled={disabled}
                onChange={(roles) => update(index, { roles })}
              />

              <ChipsField
                value={entry.supports}
                placeholder={t(
                  'config_management.visual.sections.orchestrator.catalog_supports_ph',
                  {
                    defaultValue:
                      'supports (comma-separated): streaming, tools, vision, json-mode',
                  }
                )}
                ariaLabel="Supports"
                disabled={disabled}
                onChange={(supports) => update(index, { supports })}
              />

              <div className={styles.stringListRow}>
                <input
                  className="input"
                  placeholder={t(
                    'config_management.visual.sections.orchestrator.catalog_cost_ph',
                    { defaultValue: 'cost-tier (cheap | mid | high)' }
                  )}
                  aria-label="Cost tier"
                  value={entry.costTier}
                  disabled={disabled}
                  onChange={(e) => update(index, { costTier: e.target.value })}
                />
                <input
                  className="input"
                  placeholder={t(
                    'config_management.visual.sections.orchestrator.catalog_latency_ph',
                    { defaultValue: 'latency-tier (fast | mid | slow)' }
                  )}
                  aria-label="Latency tier"
                  value={entry.latencyTier}
                  disabled={disabled}
                  onChange={(e) => update(index, { latencyTier: e.target.value })}
                />
                <input
                  className="input"
                  type="number"
                  inputMode="numeric"
                  placeholder={t(
                    'config_management.visual.sections.orchestrator.catalog_ctx_ph',
                    { defaultValue: 'context-window (tokens)' }
                  )}
                  aria-label="Context window"
                  value={entry.contextWindow}
                  disabled={disabled}
                  onChange={(e) => update(index, { contextWindow: e.target.value })}
                />
              </div>
            </div>
          </div>
        );
      })}

      {entries.length === 0 ? (
        <div className={styles.emptyState}>
          {t('config_management.visual.sections.orchestrator.catalog_empty', {
            defaultValue:
              'No catalog entries yet. Each entry teaches the orchestrator about one upstream model — pick a connected model and describe what it is good at.',
          })}
        </div>
      ) : null}

      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={add} disabled={disabled}>
          <IconPlus size={14} />{' '}
          {t('config_management.visual.sections.orchestrator.catalog_add', {
            defaultValue: 'Add model',
          })}
        </Button>
      </div>
    </div>
  );
});

/* ------------------------------------------------------------ */
/* CategoriesEditorV2 — categories with chip lists + pin pickers. */
/* ------------------------------------------------------------ */

interface CategoriesEditorV2Props {
  value: CategoryDraft[];
  catalog: CatalogEntryDraft[];
  models: ConnectedModel[];
  disabled?: boolean;
  onChange: (next: CategoryDraft[]) => void;
}

const CatalogIdSelect = memo(function CatalogIdSelect({
  value,
  catalog,
  disabled,
  placeholder,
  onChange,
}: {
  value: string;
  catalog: CatalogEntryDraft[];
  disabled?: boolean;
  placeholder?: string;
  onChange: (next: string) => void;
}) {
  const options = useMemo(() => {
    const base = [{ value: '', label: placeholder ?? '— none —' }];
    const seen = new Set<string>();
    catalog.forEach((entry) => {
      const id = (entry.entryId || entry.model).trim();
      if (!id || seen.has(id)) return;
      seen.add(id);
      const label = entry.provider ? `${id} · ${entry.provider}` : id;
      base.push({ value: id, label });
    });
    if (value && !seen.has(value)) {
      base.push({ value, label: `${value} (custom)` });
    }
    return base;
  }, [catalog, value, placeholder]);

  return <Select value={value} options={options} disabled={disabled} onChange={onChange} />;
});

export const CategoriesEditorV2 = memo(function CategoriesEditorV2({
  value,
  catalog,
  models: _models,
  disabled,
  onChange,
}: CategoriesEditorV2Props) {
  const { t } = useTranslation();
  const entries = value;

  const update = (index: number, patch: Partial<CategoryDraft>) =>
    onChange(entries.map((entry, i) => (i === index ? { ...entry, ...patch } : entry)));
  const remove = (index: number) => onChange(entries.filter((_, i) => i !== index));
  const add = () => onChange([...entries, makeCategoryDraft()]);

  return (
    <div className={styles.blockStack}>
      {entries.map((entry, index) => (
        <div key={entry.id} className={styles.ruleCard}>
          <div className={styles.ruleCardHeader}>
            <div className={styles.ruleCardTitle}>
              {t('config_management.visual.sections.orchestrator.category_entry', {
                defaultValue: 'Category {{n}}',
                n: index + 1,
              })}
            </div>
            <Button variant="ghost" size="sm" onClick={() => remove(index)} disabled={disabled}>
              {t('config_management.visual.common.delete')}
            </Button>
          </div>

          <div className={styles.blockStack}>
            <input
              className="input"
              placeholder={t(
                'config_management.visual.sections.orchestrator.category_name_ph',
                { defaultValue: 'name (e.g. code-review, math-proof, bn-en-translation)' }
              )}
              aria-label="Category name"
              value={entry.name}
              disabled={disabled}
              onChange={(e) => update(index, { name: e.target.value })}
            />

            <textarea
              className="input"
              rows={3}
              placeholder={t(
                'config_management.visual.sections.orchestrator.category_instructions_ph',
                {
                  defaultValue:
                    'Describe when this category applies. The LLM classifier reads this verbatim.',
                }
              )}
              aria-label="Instructions"
              value={entry.instructions}
              disabled={disabled}
              onChange={(e) => update(index, { instructions: e.target.value })}
            />

            <div className={styles.blockLabel}>
              {t('config_management.visual.sections.orchestrator.category_match', {
                defaultValue: 'Heuristic match (all non-empty rows must pass)',
              })}
            </div>
            <ChipsField
              value={entry.matchKeywords}
              placeholder="match.keywords (substrings, comma-separated)"
              disabled={disabled}
              onChange={(matchKeywords) => update(index, { matchKeywords })}
            />
            <ChipsField
              value={entry.matchRegex}
              placeholder="match.regex (Go regex, comma-separated)"
              disabled={disabled}
              onChange={(matchRegex) => update(index, { matchRegex })}
            />
            <ChipsField
              value={entry.matchAnyOf}
              placeholder="match.any-of (substrings, comma-separated)"
              disabled={disabled}
              onChange={(matchAnyOf) => update(index, { matchAnyOf })}
            />
            <ChipsField
              value={entry.matchNoneOf}
              placeholder="match.none-of (substrings, comma-separated)"
              disabled={disabled}
              onChange={(matchNoneOf) => update(index, { matchNoneOf })}
            />

            <div className={styles.stringListRow}>
              <input
                className="input"
                type="number"
                inputMode="numeric"
                placeholder="min-tokens"
                aria-label="Match min tokens"
                value={entry.matchMinTokens}
                disabled={disabled}
                onChange={(e) => update(index, { matchMinTokens: e.target.value })}
              />
              <input
                className="input"
                type="number"
                inputMode="numeric"
                placeholder="max-tokens"
                aria-label="Match max tokens"
                value={entry.matchMaxTokens}
                disabled={disabled}
                onChange={(e) => update(index, { matchMaxTokens: e.target.value })}
              />
            </div>

            <div className={styles.stringListRow}>
              <label className={styles.toggleRow}>
                <span>{t('config_management.visual.sections.orchestrator.category_code_block', {
                  defaultValue: 'Require code block',
                })}</span>
                <input
                  type="checkbox"
                  checked={entry.matchRequireCodeBlock}
                  disabled={disabled}
                  onChange={(e) =>
                    update(index, { matchRequireCodeBlock: e.target.checked })
                  }
                />
              </label>
              <label className={styles.toggleRow}>
                <span>{t('config_management.visual.sections.orchestrator.category_require_tools', {
                  defaultValue: 'Require tools',
                })}</span>
                <input
                  type="checkbox"
                  checked={entry.matchRequireTools}
                  disabled={disabled}
                  onChange={(e) => update(index, { matchRequireTools: e.target.checked })}
                />
              </label>
            </div>

            <div className={styles.blockLabel}>
              {t('config_management.visual.sections.orchestrator.category_prefer', {
                defaultValue: 'Prefer (ordered catalog ids — first match wins)',
              })}
            </div>
            <ChipsField
              value={entry.prefer}
              placeholder={t(
                'config_management.visual.sections.orchestrator.category_prefer_ph',
                { defaultValue: 'catalog ids, comma-separated' }
              )}
              disabled={disabled}
              onChange={(prefer) => update(index, { prefer })}
            />

            <div className={styles.blockLabel}>
              {t('config_management.visual.sections.orchestrator.category_role_pins', {
                defaultValue: 'Role pins (override Prefer for a single role)',
              })}
            </div>
            <div className={styles.stringListRow}>
              <CatalogIdSelect
                value={entry.rolePinThinker}
                catalog={catalog}
                disabled={disabled}
                placeholder="thinker — none"
                onChange={(rolePinThinker) => update(index, { rolePinThinker })}
              />
              <CatalogIdSelect
                value={entry.rolePinWorker}
                catalog={catalog}
                disabled={disabled}
                placeholder="worker — none"
                onChange={(rolePinWorker) => update(index, { rolePinWorker })}
              />
              <CatalogIdSelect
                value={entry.rolePinVerifier}
                catalog={catalog}
                disabled={disabled}
                placeholder="verifier — none"
                onChange={(rolePinVerifier) => update(index, { rolePinVerifier })}
              />
            </div>
          </div>
        </div>
      ))}

      {entries.length === 0 ? (
        <div className={styles.emptyState}>
          {t('config_management.visual.sections.orchestrator.categories_empty', {
            defaultValue:
              'No categories yet. Add one to define a task bucket — the classifier routes requests into a category and the Prefer list points at the catalog entries to use.',
          })}
        </div>
      ) : null}

      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={add} disabled={disabled}>
          <IconPlus size={14} />{' '}
          {t('config_management.visual.sections.orchestrator.categories_add', {
            defaultValue: 'Add category',
          })}
        </Button>
      </div>
    </div>
  );
});

/* ------------------------------------------------------------ */
/* Summary chips strip — quick "what's connected" overview.     */
/* ------------------------------------------------------------ */

export function ConnectedProvidersStrip({
  providers,
  loading,
  error,
}: {
  providers: ConnectedProvider[];
  loading: boolean;
  error: string | null;
}) {
  const { t } = useTranslation();
  if (loading && providers.length === 0) {
    return (
      <div className={styles.orchSummaryRow}>
        <span className={styles.orchSummaryChip}>
          {t('config_management.visual.sections.orchestrator.loading_models', {
            defaultValue: 'Loading connected models…',
          })}
        </span>
      </div>
    );
  }
  if (error && providers.length === 0) {
    return (
      <div className={styles.orchSummaryRow}>
        <span className={`${styles.orchSummaryChip} ${styles.modelPickerWarn}`}>
          {t('config_management.visual.sections.orchestrator.load_failed', {
            defaultValue: 'Could not load connected models — pickers still accept free text.',
          })}
        </span>
      </div>
    );
  }
  if (providers.length === 0) {
    return (
      <div className={styles.orchSummaryRow}>
        <span className={styles.orchSummaryChip}>
          {t('config_management.visual.sections.orchestrator.no_providers', {
            defaultValue:
              'No connected providers yet — connect a provider on the AI Providers page first.',
          })}
        </span>
      </div>
    );
  }
  return (
    <div className={styles.orchSummaryRow}>
      {providers.map((p) => (
        <span key={p.provider} className={styles.orchSummaryChip}>
          <IconChevronDown size={10} />
          {p.label}
          {p.modelCount > 0 ? ` · ${p.modelCount}` : ''}
        </span>
      ))}
    </div>
  );
}

/** Re-exported so VisualConfigEditor can call useConnectedModels indirectly. */
export { useConnectedModels };
