import {
  memo,
  useCallback,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { useNotificationStore } from '@/stores';
import styles from './VisualConfigEditor.module.scss';
import { copyToClipboard } from '@/utils/clipboard';
import type {
  CatalogEntryDraft,
  CategoryDraft,
  OrchestratorClassifierFallback,
  OrchestratorClassifierKind,
  OrchestratorVisualConfig,
  PayloadFilterRule,
  PayloadModelEntry,
  PayloadParamEntry,
  PayloadParamValidationErrorCode,
  PayloadParamValueType,
  PayloadRule,
} from '@/types/visualConfig';
import {
  makeCatalogEntryDraft,
  makeCategoryDraft,
  makeClientId,
} from '@/types/visualConfig';
import {
  getPayloadParamValidationError,
  VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS,
  VISUAL_CONFIG_PROTOCOL_OPTIONS,
} from '@/hooks/useVisualConfig';
import { maskApiKey } from '@/utils/format';
import { isValidApiKeyCharset } from '@/utils/validation';

/** Minimum character count before the expand/collapse toggle appears. */
const EXPAND_THRESHOLD = 30;

/** Auto-expanding textarea that collapses back to a single-line input on demand. */
function ExpandableInput({
  value,
  placeholder,
  ariaLabel,
  disabled,
  className,
  onChange,
}: {
  value: string;
  placeholder?: string;
  ariaLabel?: string;
  disabled?: boolean;
  className?: string;
  onChange: (nextValue: string) => void;
}) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(true);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const autoResize = useCallback((el: HTMLTextAreaElement) => {
    el.style.height = 'auto';
    el.style.height = `${el.scrollHeight}px`;
  }, []);

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    // Strip newlines — these fields are single-line identifiers/paths that
    // would break YAML serialization if they contained line breaks.
    const sanitized = e.target.value.replace(/[\r\n]/g, '');
    onChange(sanitized);
    // autoResize is handled by useLayoutEffect after React syncs the
    // sanitized value back to the DOM — calling it here would measure
    // stale content.
  };

  // Resize synchronously before paint to avoid visual flicker.
  useLayoutEffect(() => {
    if (!collapsed && textareaRef.current) {
      autoResize(textareaRef.current);
    }
  }, [collapsed, value, autoResize]);

  if (collapsed) {
    return (
      <div className={styles.expandableInputWrapper}>
        <input
          className={`input ${className ?? ''}`}
          placeholder={placeholder}
          aria-label={ariaLabel}
          value={value}
          onChange={(e) => onChange(e.target.value.replace(/[\r\n]/g, ''))}
          disabled={disabled}
        />
        {value.length > EXPAND_THRESHOLD && (
          <button
            type="button"
            className={styles.expandableToggle}
            disabled={disabled}
            onClick={() => {
              setCollapsed(false);
              requestAnimationFrame(() => {
                textareaRef.current?.focus();
              });
            }}
            title={t('common.expand')}
            aria-label={t('common.expand')}
          >
            ▼
          </button>
        )}
      </div>
    );
  }

  return (
    <div className={`${styles.expandableInputWrapper} ${styles.expandableInputExpanded}`}>
      <textarea
        ref={textareaRef}
        className={`input ${styles.expandableTextarea} ${className ?? ''}`}
        placeholder={placeholder}
        aria-label={ariaLabel}
        value={value}
        onChange={handleChange}
        disabled={disabled}
        rows={2}
      />
      <button
        type="button"
        className={styles.expandableToggle}
        disabled={disabled}
        onClick={() => setCollapsed(true)}
        title={t('common.collapse')}
        aria-label={t('common.collapse')}
      >
        ▲
      </button>
    </div>
  );
}

function getValidationMessage(
  t: ReturnType<typeof useTranslation>['t'],
  errorCode?: PayloadParamValidationErrorCode
) {
  if (!errorCode) return undefined;
  return t(`config_management.visual.validation.${errorCode}`);
}

function buildProtocolOptions(
  t: ReturnType<typeof useTranslation>['t'],
  rules: Array<{ models: PayloadModelEntry[] }>
) {
  const options: Array<{ value: string; label: string }> = VISUAL_CONFIG_PROTOCOL_OPTIONS.map(
    (option) => ({
      value: option.value,
      label: t(option.labelKey, { defaultValue: option.defaultLabel }),
    })
  );
  const seen = new Set<string>(options.map((option) => option.value));

  for (const rule of rules) {
    for (const model of rule.models) {
      const protocol = model.protocol;
      if (!protocol || !protocol.trim() || seen.has(protocol)) continue;
      seen.add(protocol);
      options.push({ value: protocol, label: protocol });
    }
  }

  return options;
}

export const ApiKeysCardEditor = memo(function ApiKeysCardEditor({
  value,
  disabled,
  onChange,
}: {
  value: string;
  disabled?: boolean;
  onChange: (nextValue: string) => void;
}) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const apiKeys = useMemo(
    () =>
      value
        .split('\n')
        .map((key) => key.trim())
        .filter(Boolean),
    [value]
  );
  const [apiKeyIds, setApiKeyIds] = useState(() => apiKeys.map(() => makeClientId()));
  const renderApiKeyIds = useMemo(() => {
    if (apiKeyIds.length === apiKeys.length) return apiKeyIds;
    if (apiKeyIds.length > apiKeys.length) return apiKeyIds.slice(0, apiKeys.length);
    return [
      ...apiKeyIds,
      ...Array.from({ length: apiKeys.length - apiKeyIds.length }, () => makeClientId()),
    ];
  }, [apiKeyIds, apiKeys.length]);

  const apiKeyInputId = useId();
  const apiKeyHintId = `${apiKeyInputId}-hint`;
  const apiKeyErrorId = `${apiKeyInputId}-error`;
  const [modalOpen, setModalOpen] = useState(false);
  const [editingApiKeyId, setEditingApiKeyId] = useState<string | null>(null);
  const [inputValue, setInputValue] = useState('');
  const [formError, setFormError] = useState('');

  function generateSecureApiKey(): string {
    const charset = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
    const array = new Uint8Array(17);
    crypto.getRandomValues(array);
    return 'sk-' + Array.from(array, (b) => charset[b % charset.length]).join('');
  }

  const openAddModal = () => {
    setEditingApiKeyId(null);
    setInputValue('');
    setFormError('');
    setModalOpen(true);
  };

  const openEditModal = (apiKeyId: string) => {
    const editingIndex = renderApiKeyIds.findIndex((id) => id === apiKeyId);
    setEditingApiKeyId(apiKeyId);
    setInputValue(apiKeys[editingIndex] ?? '');
    setFormError('');
    setModalOpen(true);
  };

  const closeModal = () => {
    setModalOpen(false);
    setInputValue('');
    setEditingApiKeyId(null);
    setFormError('');
  };

  const updateApiKeys = (nextKeys: string[]) => {
    onChange(nextKeys.join('\n'));
  };

  const handleDelete = (apiKeyId: string) => {
    const index = renderApiKeyIds.findIndex((id) => id === apiKeyId);
    if (index < 0) return;
    setApiKeyIds(renderApiKeyIds.filter((id) => id !== apiKeyId));
    updateApiKeys(apiKeys.filter((_, i) => i !== index));
  };

  const handleSave = () => {
    const trimmed = inputValue.trim();
    if (!trimmed) {
      setFormError(t('config_management.visual.api_keys.error_empty'));
      return;
    }
    if (!isValidApiKeyCharset(trimmed)) {
      setFormError(t('config_management.visual.api_keys.error_invalid'));
      return;
    }

    const editingIndex = editingApiKeyId
      ? renderApiKeyIds.findIndex((id) => id === editingApiKeyId)
      : -1;
    const nextKeys =
      editingApiKeyId === null
        ? [...apiKeys, trimmed]
        : apiKeys.map((key, idx) => (idx === editingIndex ? trimmed : key));
    if (editingApiKeyId === null) {
      setApiKeyIds([...renderApiKeyIds, makeClientId()]);
    }
    updateApiKeys(nextKeys);
    closeModal();
  };

  const handleCopy = async (apiKey: string) => {
    const copied = await copyToClipboard(apiKey);
    showNotification(
      t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
      copied ? 'success' : 'error'
    );
  };

  const handleGenerate = () => {
    setInputValue(generateSecureApiKey());
    setFormError('');
  };

  return (
    <div className="form-group" style={{ marginBottom: 0 }}>
      <div className={styles.blockHeaderRow}>
        <label style={{ margin: 0 }}>{t('config_management.visual.api_keys.label')}</label>
        <Button size="sm" onClick={openAddModal} disabled={disabled}>
          {t('config_management.visual.api_keys.add')}
        </Button>
      </div>

      {apiKeys.length === 0 ? (
        <div className={styles.emptyState}>{t('config_management.visual.api_keys.empty')}</div>
      ) : (
        <div className="item-list" style={{ marginTop: 4 }}>
          {apiKeys.map((key, index) => (
            <div key={renderApiKeyIds[index] ?? `${key}-${index}`} className="item-row">
              <div className="item-meta">
                <div className="pill">#{index + 1}</div>
                <div className="item-title">
                  {t('config_management.visual.api_keys.input_label')}
                </div>
                <div className="item-subtitle">{maskApiKey(String(key || ''))}</div>
              </div>
              <div className="item-actions">
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => handleCopy(key)}
                  disabled={disabled}
                >
                  {t('common.copy')}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => openEditModal(renderApiKeyIds[index] ?? '')}
                  disabled={disabled}
                >
                  {t('config_management.visual.common.edit')}
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  onClick={() => handleDelete(renderApiKeyIds[index] ?? '')}
                  disabled={disabled}
                >
                  {t('config_management.visual.common.delete')}
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      <div className="hint">{t('config_management.visual.api_keys.hint')}</div>

      <Modal
        open={modalOpen}
        onClose={closeModal}
        title={
          editingApiKeyId !== null
            ? t('config_management.visual.api_keys.edit_title')
            : t('config_management.visual.api_keys.add_title')
        }
        footer={
          <>
            <Button variant="secondary" onClick={closeModal} disabled={disabled}>
              {t('config_management.visual.common.cancel')}
            </Button>
            <Button onClick={handleSave} disabled={disabled}>
              {editingApiKeyId !== null
                ? t('config_management.visual.common.update')
                : t('config_management.visual.common.add')}
            </Button>
          </>
        }
      >
        <div className="form-group">
          <label htmlFor={apiKeyInputId}>
            {t('config_management.visual.api_keys.input_label')}
          </label>
          <div className={styles.apiKeyModalInputRow}>
            <input
              id={apiKeyInputId}
              className="input"
              placeholder={t('config_management.visual.api_keys.input_placeholder')}
              value={inputValue}
              onChange={(e) => setInputValue(e.target.value)}
              disabled={disabled}
              aria-describedby={formError ? `${apiKeyErrorId} ${apiKeyHintId}` : apiKeyHintId}
              aria-invalid={Boolean(formError)}
            />
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={handleGenerate}
              disabled={disabled}
            >
              {t('config_management.visual.api_keys.generate')}
            </Button>
          </div>
          <div id={apiKeyHintId} className="hint">
            {t('config_management.visual.api_keys.input_hint')}
          </div>
          {formError && (
            <div id={apiKeyErrorId} className="error-box">
              {formError}
            </div>
          )}
        </div>
      </Modal>
    </div>
  );
});

const StringListEditor = memo(function StringListEditor({
  value,
  disabled,
  placeholder,
  inputAriaLabel,
  onChange,
}: {
  value: string[];
  disabled?: boolean;
  placeholder?: string;
  inputAriaLabel?: string;
  onChange: (next: string[]) => void;
}) {
  const { t } = useTranslation();
  const items = value.length ? value : [];
  const [itemIds, setItemIds] = useState(() => items.map(() => makeClientId()));
  const renderItemIds = useMemo(() => {
    if (itemIds.length === items.length) return itemIds;
    if (itemIds.length > items.length) return itemIds.slice(0, items.length);
    return [
      ...itemIds,
      ...Array.from({ length: items.length - itemIds.length }, () => makeClientId()),
    ];
  }, [itemIds, items.length]);

  const updateItem = (index: number, nextValue: string) =>
    onChange(items.map((item, i) => (i === index ? nextValue : item)));
  const addItem = () => {
    setItemIds([...renderItemIds, makeClientId()]);
    onChange([...items, '']);
  };
  const removeItem = (index: number) => {
    setItemIds(renderItemIds.filter((_, i) => i !== index));
    onChange(items.filter((_, i) => i !== index));
  };

  return (
    <div className={styles.stringList}>
      {items.map((item, index) => (
        <div key={renderItemIds[index] ?? `item-${index}`} className={styles.stringListRow}>
          <ExpandableInput
            placeholder={placeholder}
            ariaLabel={inputAriaLabel ?? placeholder}
            value={item}
            onChange={(nextValue) => updateItem(index, nextValue)}
            disabled={disabled}
          />
          <Button variant="ghost" size="sm" onClick={() => removeItem(index)} disabled={disabled}>
            {t('config_management.visual.common.delete')}
          </Button>
        </div>
      ))}
      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={addItem} disabled={disabled}>
          {t('config_management.visual.common.add')}
        </Button>
      </div>
    </div>
  );
});

export const PayloadRulesEditor = memo(function PayloadRulesEditor({
  value,
  disabled,
  protocolFirst = false,
  rawJsonValues = false,
  onChange,
}: {
  value: PayloadRule[];
  disabled?: boolean;
  protocolFirst?: boolean;
  rawJsonValues?: boolean;
  onChange: (next: PayloadRule[]) => void;
}) {
  const { t } = useTranslation();
  const rules = value;
  const protocolOptions = useMemo(() => buildProtocolOptions(t, rules), [rules, t]);
  const payloadValueTypeOptions = useMemo(
    () =>
      VISUAL_CONFIG_PAYLOAD_VALUE_TYPE_OPTIONS.map((option) => ({
        value: option.value,
        label: t(option.labelKey, { defaultValue: option.defaultLabel }),
      })),
    [t]
  );
  const booleanValueOptions = useMemo(
    () => [
      { value: 'true', label: t('config_management.visual.payload_rules.boolean_true') },
      { value: 'false', label: t('config_management.visual.payload_rules.boolean_false') },
    ],
    [t]
  );

  const addRule = () => onChange([...rules, { id: makeClientId(), models: [], params: [] }]);
  const removeRule = (ruleIndex: number) => onChange(rules.filter((_, i) => i !== ruleIndex));

  const updateRule = (ruleIndex: number, patch: Partial<PayloadRule>) =>
    onChange(rules.map((rule, i) => (i === ruleIndex ? { ...rule, ...patch } : rule)));

  const addModel = (ruleIndex: number) => {
    const rule = rules[ruleIndex];
    const nextModel: PayloadModelEntry = { id: makeClientId(), name: '', protocol: undefined };
    updateRule(ruleIndex, { models: [...rule.models, nextModel] });
  };

  const removeModel = (ruleIndex: number, modelIndex: number) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, { models: rule.models.filter((_, i) => i !== modelIndex) });
  };

  const updateModel = (
    ruleIndex: number,
    modelIndex: number,
    patch: Partial<PayloadModelEntry>
  ) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, {
      models: rule.models.map((m, i) => (i === modelIndex ? { ...m, ...patch } : m)),
    });
  };

  const addParam = (ruleIndex: number) => {
    const rule = rules[ruleIndex];
    const nextParam: PayloadParamEntry = {
      id: makeClientId(),
      path: '',
      valueType: rawJsonValues ? 'json' : 'string',
      value: '',
    };
    updateRule(ruleIndex, { params: [...rule.params, nextParam] });
  };

  const removeParam = (ruleIndex: number, paramIndex: number) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, { params: rule.params.filter((_, i) => i !== paramIndex) });
  };

  const updateParam = (
    ruleIndex: number,
    paramIndex: number,
    patch: Partial<PayloadParamEntry>
  ) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, {
      params: rule.params.map((p, i) => (i === paramIndex ? { ...p, ...patch } : p)),
    });
  };

  const getValuePlaceholder = (valueType: PayloadParamValueType) => {
    switch (valueType) {
      case 'string':
        return t('config_management.visual.payload_rules.value_string');
      case 'number':
        return t('config_management.visual.payload_rules.value_number');
      case 'boolean':
        return t('config_management.visual.payload_rules.value_boolean');
      case 'json':
        return t('config_management.visual.payload_rules.value_json');
      default:
        return t('config_management.visual.payload_rules.value_default');
    }
  };

  const getParamErrorMessage = (param: PayloadParamEntry) => {
    const errorCode = getPayloadParamValidationError(
      rawJsonValues ? { ...param, valueType: 'json' } : param
    );
    return getValidationMessage(t, errorCode);
  };

  const renderParamValueEditor = (
    ruleIndex: number,
    paramIndex: number,
    param: PayloadParamEntry
  ) => {
    if (rawJsonValues) {
      return (
        <textarea
          className={`input ${styles.payloadJsonInput}`}
          placeholder={t('config_management.visual.payload_rules.value_raw_json')}
          aria-label={t('config_management.visual.payload_rules.param_value')}
          value={param.value}
          onChange={(e) =>
            updateParam(ruleIndex, paramIndex, { value: e.target.value, valueType: 'json' })
          }
          disabled={disabled}
        />
      );
    }

    if (param.valueType === 'boolean') {
      return (
        <Select
          value={
            param.value.toLowerCase() === 'true' || param.value.toLowerCase() === 'false'
              ? param.value.toLowerCase()
              : ''
          }
          options={booleanValueOptions}
          placeholder={t('config_management.visual.payload_rules.value_boolean')}
          disabled={disabled}
          ariaLabel={t('config_management.visual.payload_rules.param_value')}
          onChange={(nextValue) => updateParam(ruleIndex, paramIndex, { value: nextValue })}
        />
      );
    }

    if (param.valueType === 'json') {
      return (
        <textarea
          className={`input ${styles.payloadJsonInput}`}
          placeholder={getValuePlaceholder(param.valueType)}
          aria-label={t('config_management.visual.payload_rules.param_value')}
          value={param.value}
          onChange={(e) => updateParam(ruleIndex, paramIndex, { value: e.target.value })}
          disabled={disabled}
        />
      );
    }

    return (
      <ExpandableInput
        placeholder={getValuePlaceholder(param.valueType)}
        ariaLabel={t('config_management.visual.payload_rules.param_value')}
        value={param.value}
        onChange={(nextValue) => updateParam(ruleIndex, paramIndex, { value: nextValue })}
        disabled={disabled}
      />
    );
  };

  return (
    <div className={styles.blockStack}>
      {rules.map((rule, ruleIndex) => (
        <div key={rule.id} className={styles.ruleCard}>
          <div className={styles.ruleCardHeader}>
            <div className={styles.ruleCardTitle}>
              {t('config_management.visual.payload_rules.rule')} {ruleIndex + 1}
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => removeRule(ruleIndex)}
              disabled={disabled}
            >
              {t('config_management.visual.common.delete')}
            </Button>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.models')}
            </div>
            {(rule.models.length ? rule.models : []).map((model, modelIndex) => (
              <div
                key={model.id}
                className={[
                  styles.payloadRuleModelRow,
                  protocolFirst ? styles.payloadRuleModelRowProtocolFirst : '',
                ]
                  .filter(Boolean)
                  .join(' ')}
              >
                {protocolFirst ? (
                  <>
                    <Select
                      value={model.protocol ?? ''}
                      options={protocolOptions}
                      disabled={disabled}
                      ariaLabel={t('config_management.visual.payload_rules.provider_type')}
                      onChange={(nextValue) =>
                        updateModel(ruleIndex, modelIndex, {
                          protocol: (nextValue || undefined) as PayloadModelEntry['protocol'],
                        })
                      }
                    />
                    <ExpandableInput
                      placeholder={t('config_management.visual.payload_rules.model_name')}
                      ariaLabel={t('config_management.visual.payload_rules.model_name')}
                      value={model.name}
                      onChange={(nextValue) => updateModel(ruleIndex, modelIndex, { name: nextValue })}
                      disabled={disabled}
                    />
                  </>
                ) : (
                  <>
                    <ExpandableInput
                      placeholder={t('config_management.visual.payload_rules.model_name')}
                      ariaLabel={t('config_management.visual.payload_rules.model_name')}
                      value={model.name}
                      onChange={(nextValue) => updateModel(ruleIndex, modelIndex, { name: nextValue })}
                      disabled={disabled}
                    />
                    <Select
                      value={model.protocol ?? ''}
                      options={protocolOptions}
                      disabled={disabled}
                      ariaLabel={t('config_management.visual.payload_rules.provider_type')}
                      onChange={(nextValue) =>
                        updateModel(ruleIndex, modelIndex, {
                          protocol: (nextValue || undefined) as PayloadModelEntry['protocol'],
                        })
                      }
                    />
                  </>
                )}
                <Button
                  variant="ghost"
                  size="sm"
                  className={styles.payloadRowActionButton}
                  onClick={() => removeModel(ruleIndex, modelIndex)}
                  disabled={disabled}
                >
                  {t('config_management.visual.common.delete')}
                </Button>
              </div>
            ))}
            <div className={styles.actionRow}>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => addModel(ruleIndex)}
                disabled={disabled}
              >
                {t('config_management.visual.payload_rules.add_model')}
              </Button>
            </div>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.params')}
            </div>
            {(rule.params.length ? rule.params : []).map((param, paramIndex) => {
              const paramError = getParamErrorMessage(param);

              return (
                <div key={param.id} className={styles.payloadRuleParamGroup}>
                  <div className={styles.payloadRuleParamRow}>
                    <ExpandableInput
                      placeholder={t('config_management.visual.payload_rules.json_path')}
                      ariaLabel={t('config_management.visual.payload_rules.json_path')}
                      value={param.path}
                      onChange={(nextValue) => updateParam(ruleIndex, paramIndex, { path: nextValue })}
                      disabled={disabled}
                    />
                    {rawJsonValues ? null : (
                      <Select
                        value={param.valueType}
                        options={payloadValueTypeOptions}
                        disabled={disabled}
                        ariaLabel={t('config_management.visual.payload_rules.param_type')}
                        onChange={(nextValue) =>
                          updateParam(ruleIndex, paramIndex, {
                            valueType: nextValue as PayloadParamValueType,
                            value:
                              nextValue === 'boolean'
                                ? 'true'
                                : nextValue === 'json' && param.value.trim() === ''
                                  ? '{}'
                                  : param.value,
                          })
                        }
                      />
                    )}
                    {renderParamValueEditor(ruleIndex, paramIndex, param)}
                    <Button
                      variant="ghost"
                      size="sm"
                      className={styles.payloadRowActionButton}
                      onClick={() => removeParam(ruleIndex, paramIndex)}
                      disabled={disabled}
                    >
                      {t('config_management.visual.common.delete')}
                    </Button>
                  </div>
                  {paramError && (
                    <div className={`error-box ${styles.payloadParamError}`}>{paramError}</div>
                  )}
                </div>
              );
            })}
            <div className={styles.actionRow}>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => addParam(ruleIndex)}
                disabled={disabled}
              >
                {t('config_management.visual.payload_rules.add_param')}
              </Button>
            </div>
          </div>
        </div>
      ))}

      {rules.length === 0 && (
        <div className={styles.emptyState}>
          {t('config_management.visual.payload_rules.no_rules')}
        </div>
      )}

      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={addRule} disabled={disabled}>
          {t('config_management.visual.payload_rules.add_rule')}
        </Button>
      </div>
    </div>
  );
});

export const PayloadFilterRulesEditor = memo(function PayloadFilterRulesEditor({
  value,
  disabled,
  onChange,
}: {
  value: PayloadFilterRule[];
  disabled?: boolean;
  onChange: (next: PayloadFilterRule[]) => void;
}) {
  const { t } = useTranslation();
  const rules = value;
  const protocolOptions = useMemo(() => buildProtocolOptions(t, rules), [rules, t]);

  const addRule = () => onChange([...rules, { id: makeClientId(), models: [], params: [] }]);
  const removeRule = (ruleIndex: number) => onChange(rules.filter((_, i) => i !== ruleIndex));

  const updateRule = (ruleIndex: number, patch: Partial<PayloadFilterRule>) =>
    onChange(rules.map((rule, i) => (i === ruleIndex ? { ...rule, ...patch } : rule)));

  const addModel = (ruleIndex: number) => {
    const rule = rules[ruleIndex];
    const nextModel: PayloadModelEntry = { id: makeClientId(), name: '', protocol: undefined };
    updateRule(ruleIndex, { models: [...rule.models, nextModel] });
  };

  const removeModel = (ruleIndex: number, modelIndex: number) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, { models: rule.models.filter((_, i) => i !== modelIndex) });
  };

  const updateModel = (
    ruleIndex: number,
    modelIndex: number,
    patch: Partial<PayloadModelEntry>
  ) => {
    const rule = rules[ruleIndex];
    updateRule(ruleIndex, {
      models: rule.models.map((m, i) => (i === modelIndex ? { ...m, ...patch } : m)),
    });
  };

  return (
    <div className={styles.blockStack}>
      {rules.map((rule, ruleIndex) => (
        <div key={rule.id} className={styles.ruleCard}>
          <div className={styles.ruleCardHeader}>
            <div className={styles.ruleCardTitle}>
              {t('config_management.visual.payload_rules.rule')} {ruleIndex + 1}
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => removeRule(ruleIndex)}
              disabled={disabled}
            >
              {t('config_management.visual.common.delete')}
            </Button>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.models')}
            </div>
            {rule.models.map((model, modelIndex) => (
              <div key={model.id} className={styles.payloadFilterModelRow}>
                <ExpandableInput
                  placeholder={t('config_management.visual.payload_rules.model_name')}
                  ariaLabel={t('config_management.visual.payload_rules.model_name')}
                  value={model.name}
                  onChange={(nextValue) => updateModel(ruleIndex, modelIndex, { name: nextValue })}
                  disabled={disabled}
                />
                <Select
                  value={model.protocol ?? ''}
                  options={protocolOptions}
                  disabled={disabled}
                  ariaLabel={t('config_management.visual.payload_rules.provider_type')}
                  onChange={(nextValue) =>
                    updateModel(ruleIndex, modelIndex, {
                      protocol: (nextValue || undefined) as PayloadModelEntry['protocol'],
                    })
                  }
                />
                <Button
                  variant="ghost"
                  size="sm"
                  className={styles.payloadRowActionButton}
                  onClick={() => removeModel(ruleIndex, modelIndex)}
                  disabled={disabled}
                >
                  {t('config_management.visual.common.delete')}
                </Button>
              </div>
            ))}
            <div className={styles.actionRow}>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => addModel(ruleIndex)}
                disabled={disabled}
              >
                {t('config_management.visual.payload_rules.add_model')}
              </Button>
            </div>
          </div>

          <div className={styles.blockStack}>
            <div className={styles.blockLabel}>
              {t('config_management.visual.payload_rules.remove_params')}
            </div>
            <StringListEditor
              value={rule.params}
              disabled={disabled}
              placeholder={t('config_management.visual.payload_rules.json_path_filter')}
              inputAriaLabel={t('config_management.visual.payload_rules.json_path_filter')}
              onChange={(params) => updateRule(ruleIndex, { params })}
            />
          </div>
        </div>
      ))}

      {rules.length === 0 && (
        <div className={styles.emptyState}>
          {t('config_management.visual.payload_rules.no_rules')}
        </div>
      )}

      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={addRule} disabled={disabled}>
          {t('config_management.visual.payload_rules.add_rule')}
        </Button>
      </div>
    </div>
  );
});

/* ============================================================== */
/* v2/v2.1 orchestrator editors: catalog, categories, classifier. */
/* ============================================================== */

/**
 * ChipListInput edits a string[] as a comma-separated text field.
 *
 * While the input is focused, `buffer` holds the raw user text so
 * partial commas/spaces and intermediate states feel natural. While
 * unfocused, `buffer` is `null` and we display `value.join(', ')` —
 * which means parent-driven array changes (load YAML, undo) flow in
 * without any effect-driven re-sync. On blur we commit the parsed
 * array and clear the buffer.
 *
 * Use for tags, roles, capability flags, prefer-ids, keywords —
 * anywhere a one-line "a, b, c" list is the right shape.
 */
const ChipListInput = memo(function ChipListInput({
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

/**
 * CatalogEditor lists OrchestratorCatalogEntry rows. Each row is a
 * card containing the upstream model identity (provider, model, id)
 * plus the metadata the classifier reads (description, instructions,
 * tags, roles, cost/latency tiers, context window, supports).
 *
 * Empty rows (no provider AND no model) are kept in the editor for
 * editing but dropped at YAML serialization time.
 */
export const CatalogEditor = memo(function CatalogEditor({
  value,
  disabled,
  onChange,
}: {
  value: CatalogEntryDraft[];
  disabled?: boolean;
  onChange: (next: CatalogEntryDraft[]) => void;
}) {
  const { t } = useTranslation();
  const entries = value;

  const updateEntry = (index: number, patch: Partial<CatalogEntryDraft>) =>
    onChange(entries.map((entry, i) => (i === index ? { ...entry, ...patch } : entry)));
  const removeEntry = (index: number) =>
    onChange(entries.filter((_, i) => i !== index));
  const addEntry = () => onChange([...entries, makeCatalogEntryDraft()]);

  return (
    <div className={styles.blockStack}>
      {entries.map((entry, index) => (
        <div key={entry.id} className={styles.ruleCard}>
          <div className={styles.ruleCardHeader}>
            <div className={styles.ruleCardTitle}>
              {t('config_management.visual.sections.orchestrator.catalog_entry', {
                defaultValue: 'Catalog entry {{n}}',
                n: index + 1,
              })}
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => removeEntry(index)}
              disabled={disabled}
            >
              {t('config_management.visual.common.delete')}
            </Button>
          </div>

          <div className={styles.blockStack}>
            <input
              className="input"
              placeholder={t('config_management.visual.sections.orchestrator.catalog_id_ph', {
                defaultValue: 'id (e.g. claude-opus-4.8)',
              })}
              aria-label={t('config_management.visual.sections.orchestrator.catalog_id', {
                defaultValue: 'Catalog id',
              })}
              value={entry.entryId}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { entryId: e.target.value })}
            />
            <input
              className="input"
              placeholder={t(
                'config_management.visual.sections.orchestrator.catalog_provider_ph',
                { defaultValue: 'provider (e.g. claude, codex, gemini-cli)' }
              )}
              aria-label={t('config_management.visual.sections.orchestrator.catalog_provider', {
                defaultValue: 'Provider',
              })}
              value={entry.provider}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { provider: e.target.value })}
            />
            <input
              className="input"
              placeholder={t('config_management.visual.sections.orchestrator.catalog_model_ph', {
                defaultValue: 'model (e.g. claude-opus-4-5-20251101)',
              })}
              aria-label={t('config_management.visual.sections.orchestrator.catalog_model', {
                defaultValue: 'Model',
              })}
              value={entry.model}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { model: e.target.value })}
            />

            <input
              className="input"
              placeholder={t('config_management.visual.sections.orchestrator.catalog_desc_ph', {
                defaultValue: 'One-line description (LLM classifier hint)',
              })}
              aria-label={t('config_management.visual.sections.orchestrator.catalog_desc', {
                defaultValue: 'Description',
              })}
              value={entry.description}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { description: e.target.value })}
            />

            <textarea
              className="input"
              rows={4}
              placeholder={t(
                'config_management.visual.sections.orchestrator.catalog_instructions_ph',
                {
                  defaultValue:
                    'Paragraph instructions for direct-model routing — what is this model uniquely good at? Avoid using it for…',
                }
              )}
              aria-label={t(
                'config_management.visual.sections.orchestrator.catalog_instructions',
                { defaultValue: 'Instructions' }
              )}
              value={entry.instructions}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { instructions: e.target.value })}
            />

            <ChipListInput
              value={entry.tags}
              placeholder={t('config_management.visual.sections.orchestrator.catalog_tags_ph', {
                defaultValue: 'tags (comma-separated, e.g. code, vision, math)',
              })}
              ariaLabel={t('config_management.visual.sections.orchestrator.catalog_tags', {
                defaultValue: 'Tags',
              })}
              disabled={disabled}
              onChange={(tags) => updateEntry(index, { tags })}
            />

            <ChipListInput
              value={entry.roles}
              placeholder={t('config_management.visual.sections.orchestrator.catalog_roles_ph', {
                defaultValue: 'roles (comma-separated: thinker, worker, verifier, classifier)',
              })}
              ariaLabel={t('config_management.visual.sections.orchestrator.catalog_roles', {
                defaultValue: 'Roles',
              })}
              disabled={disabled}
              onChange={(roles) => updateEntry(index, { roles })}
            />

            <ChipListInput
              value={entry.supports}
              placeholder={t(
                'config_management.visual.sections.orchestrator.catalog_supports_ph',
                {
                  defaultValue:
                    'supports (comma-separated: streaming, tools, vision, json-mode)',
                }
              )}
              ariaLabel={t('config_management.visual.sections.orchestrator.catalog_supports', {
                defaultValue: 'Supports',
              })}
              disabled={disabled}
              onChange={(supports) => updateEntry(index, { supports })}
            />

            <div className={styles.stringListRow}>
              <input
                className="input"
                placeholder={t('config_management.visual.sections.orchestrator.catalog_cost_ph', {
                  defaultValue: 'cost-tier (cheap | mid | high)',
                })}
                aria-label={t('config_management.visual.sections.orchestrator.catalog_cost', {
                  defaultValue: 'Cost tier',
                })}
                value={entry.costTier}
                disabled={disabled}
                onChange={(e) => updateEntry(index, { costTier: e.target.value })}
              />
              <input
                className="input"
                placeholder={t(
                  'config_management.visual.sections.orchestrator.catalog_latency_ph',
                  { defaultValue: 'latency-tier (fast | mid | slow)' }
                )}
                aria-label={t('config_management.visual.sections.orchestrator.catalog_latency', {
                  defaultValue: 'Latency tier',
                })}
                value={entry.latencyTier}
                disabled={disabled}
                onChange={(e) => updateEntry(index, { latencyTier: e.target.value })}
              />
              <input
                className="input"
                type="number"
                inputMode="numeric"
                placeholder={t('config_management.visual.sections.orchestrator.catalog_ctx_ph', {
                  defaultValue: 'context-window (tokens)',
                })}
                aria-label={t('config_management.visual.sections.orchestrator.catalog_ctx', {
                  defaultValue: 'Context window',
                })}
                value={entry.contextWindow}
                disabled={disabled}
                onChange={(e) => updateEntry(index, { contextWindow: e.target.value })}
              />
            </div>
          </div>
        </div>
      ))}

      {entries.length === 0 && (
        <div className={styles.emptyState}>
          {t('config_management.visual.sections.orchestrator.catalog_empty', {
            defaultValue:
              'No catalog entries. Add one per upstream model you want the orchestrator to route to.',
          })}
        </div>
      )}

      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={addEntry} disabled={disabled}>
          {t('config_management.visual.sections.orchestrator.catalog_add', {
            defaultValue: 'Add catalog entry',
          })}
        </Button>
      </div>
    </div>
  );
});

/**
 * CategoriesEditor lists OrchestratorCategory rows. Each row carries
 * the category's identity (name, instructions), its deterministic
 * heuristic match predicates, the ordered Prefer list of catalog ids,
 * and the optional per-role pins for tri-role mode.
 */
export const CategoriesEditor = memo(function CategoriesEditor({
  value,
  disabled,
  onChange,
}: {
  value: CategoryDraft[];
  disabled?: boolean;
  onChange: (next: CategoryDraft[]) => void;
}) {
  const { t } = useTranslation();
  const entries = value;

  const updateEntry = (index: number, patch: Partial<CategoryDraft>) =>
    onChange(entries.map((entry, i) => (i === index ? { ...entry, ...patch } : entry)));
  const removeEntry = (index: number) =>
    onChange(entries.filter((_, i) => i !== index));
  const addEntry = () => onChange([...entries, makeCategoryDraft()]);

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
            <Button
              variant="ghost"
              size="sm"
              onClick={() => removeEntry(index)}
              disabled={disabled}
            >
              {t('config_management.visual.common.delete')}
            </Button>
          </div>

          <div className={styles.blockStack}>
            <input
              className="input"
              placeholder={t('config_management.visual.sections.orchestrator.category_name_ph', {
                defaultValue: 'name (e.g. code-review, math-proof, bn-en-translation)',
              })}
              aria-label={t('config_management.visual.sections.orchestrator.category_name', {
                defaultValue: 'Category name',
              })}
              value={entry.name}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { name: e.target.value })}
            />

            <textarea
              className="input"
              rows={3}
              placeholder={t(
                'config_management.visual.sections.orchestrator.category_instructions_ph',
                {
                  defaultValue:
                    'When this category applies — natural language. The LLM classifier sees this.',
                }
              )}
              aria-label={t(
                'config_management.visual.sections.orchestrator.category_instructions',
                { defaultValue: 'Instructions' }
              )}
              value={entry.instructions}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { instructions: e.target.value })}
            />

            <div className={styles.blockLabel}>
              {t('config_management.visual.sections.orchestrator.category_match', {
                defaultValue: 'Heuristic match predicates (logical AND across non-empty fields)',
              })}
            </div>

            <ChipListInput
              value={entry.matchKeywords}
              placeholder={t(
                'config_management.visual.sections.orchestrator.category_keywords_ph',
                { defaultValue: 'match.keywords (any-of substrings, comma-separated)' }
              )}
              ariaLabel="Match keywords"
              disabled={disabled}
              onChange={(matchKeywords) => updateEntry(index, { matchKeywords })}
            />
            <ChipListInput
              value={entry.matchRegex}
              placeholder={t('config_management.visual.sections.orchestrator.category_regex_ph', {
                defaultValue: 'match.regex (Go regex patterns, comma-separated)',
              })}
              ariaLabel="Match regex"
              disabled={disabled}
              onChange={(matchRegex) => updateEntry(index, { matchRegex })}
            />
            <ChipListInput
              value={entry.matchAnyOf}
              placeholder={t('config_management.visual.sections.orchestrator.category_anyof_ph', {
                defaultValue: 'match.any-of (substrings, comma-separated)',
              })}
              ariaLabel="Any of"
              disabled={disabled}
              onChange={(matchAnyOf) => updateEntry(index, { matchAnyOf })}
            />
            <ChipListInput
              value={entry.matchNoneOf}
              placeholder={t('config_management.visual.sections.orchestrator.category_noneof_ph', {
                defaultValue: 'match.none-of (forbidden substrings, comma-separated)',
              })}
              ariaLabel="None of"
              disabled={disabled}
              onChange={(matchNoneOf) => updateEntry(index, { matchNoneOf })}
            />

            <div className={styles.stringListRow}>
              <input
                className="input"
                type="number"
                inputMode="numeric"
                placeholder={t('config_management.visual.sections.orchestrator.category_min_ph', {
                  defaultValue: 'min-tokens',
                })}
                aria-label="Match min tokens"
                value={entry.matchMinTokens}
                disabled={disabled}
                onChange={(e) => updateEntry(index, { matchMinTokens: e.target.value })}
              />
              <input
                className="input"
                type="number"
                inputMode="numeric"
                placeholder={t('config_management.visual.sections.orchestrator.category_max_ph', {
                  defaultValue: 'max-tokens',
                })}
                aria-label="Match max tokens"
                value={entry.matchMaxTokens}
                disabled={disabled}
                onChange={(e) => updateEntry(index, { matchMaxTokens: e.target.value })}
              />
            </div>

            <div className={styles.stringListRow}>
              <label className={styles.toggleRow}>
                <span>
                  {t('config_management.visual.sections.orchestrator.category_code_block', {
                    defaultValue: 'Require code block',
                  })}
                </span>
                <input
                  type="checkbox"
                  checked={entry.matchRequireCodeBlock}
                  disabled={disabled}
                  onChange={(e) =>
                    updateEntry(index, { matchRequireCodeBlock: e.target.checked })
                  }
                />
              </label>
              <label className={styles.toggleRow}>
                <span>
                  {t('config_management.visual.sections.orchestrator.category_require_tools', {
                    defaultValue: 'Require tools',
                  })}
                </span>
                <input
                  type="checkbox"
                  checked={entry.matchRequireTools}
                  disabled={disabled}
                  onChange={(e) =>
                    updateEntry(index, { matchRequireTools: e.target.checked })
                  }
                />
              </label>
            </div>

            <div className={styles.blockLabel}>
              {t('config_management.visual.sections.orchestrator.category_prefer', {
                defaultValue: 'Prefer (ordered catalog ids)',
              })}
            </div>
            <ChipListInput
              value={entry.prefer}
              placeholder={t(
                'config_management.visual.sections.orchestrator.category_prefer_ph',
                {
                  defaultValue:
                    'catalog ids, comma-separated (first match in candidate set wins)',
                }
              )}
              ariaLabel="Prefer"
              disabled={disabled}
              onChange={(prefer) => updateEntry(index, { prefer })}
            />

            <div className={styles.blockLabel}>
              {t('config_management.visual.sections.orchestrator.category_role_pins', {
                defaultValue: 'Role pins (override Prefer for the named role)',
              })}
            </div>
            <input
              className="input"
              placeholder={t(
                'config_management.visual.sections.orchestrator.category_pin_thinker_ph',
                { defaultValue: 'role-pins.thinker (catalog id)' }
              )}
              aria-label="Thinker pin"
              value={entry.rolePinThinker}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { rolePinThinker: e.target.value })}
            />
            <input
              className="input"
              placeholder={t(
                'config_management.visual.sections.orchestrator.category_pin_worker_ph',
                { defaultValue: 'role-pins.worker (catalog id)' }
              )}
              aria-label="Worker pin"
              value={entry.rolePinWorker}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { rolePinWorker: e.target.value })}
            />
            <input
              className="input"
              placeholder={t(
                'config_management.visual.sections.orchestrator.category_pin_verifier_ph',
                { defaultValue: 'role-pins.verifier (catalog id)' }
              )}
              aria-label="Verifier pin"
              value={entry.rolePinVerifier}
              disabled={disabled}
              onChange={(e) => updateEntry(index, { rolePinVerifier: e.target.value })}
            />
          </div>
        </div>
      ))}

      {entries.length === 0 && (
        <div className={styles.emptyState}>
          {t('config_management.visual.sections.orchestrator.categories_empty', {
            defaultValue:
              'No categories. The orchestrator will use only legacy code/math/recall classification until you add one.',
          })}
        </div>
      )}

      <div className={styles.actionRow}>
        <Button variant="secondary" size="sm" onClick={addEntry} disabled={disabled}>
          {t('config_management.visual.sections.orchestrator.categories_add', {
            defaultValue: 'Add category',
          })}
        </Button>
      </div>
    </div>
  );
});

/**
 * ClassifierEditor configures how requests are bucketed — heuristic
 * only, LLM-picks-a-category, hybrid (heuristic first, LLM on miss),
 * or direct-model (LLM picks a catalog id directly). The LLM sub-form
 * is the larger surface: provider/model + caching + timeouts + the
 * optional override prompt template + fallback strategy.
 */
type ClassifierPatch = Partial<
  Pick<
    OrchestratorVisualConfig,
    | 'classifierKind'
    | 'classifierFirstMatchWins'
    | 'classifierLlmEnabled'
    | 'classifierLlmProvider'
    | 'classifierLlmModel'
    | 'classifierLlmTimeoutMs'
    | 'classifierLlmCacheTtlSeconds'
    | 'classifierLlmMaxInputChars'
    | 'classifierLlmPromptTemplate'
    | 'classifierLlmFallbackOnError'
    | 'classifierLlmDefaultCategory'
  >
>;

export const ClassifierEditor = memo(function ClassifierEditor({
  values,
  disabled,
  onChange,
}: {
  values: OrchestratorVisualConfig;
  disabled?: boolean;
  onChange: (patch: ClassifierPatch) => void;
}) {
  const { t } = useTranslation();
  const kindOptions = useMemo(
    () => [
      {
        value: '',
        label: t('config_management.visual.sections.orchestrator.classifier_kind_default', {
          defaultValue: '(unset — orchestrator default)',
        }),
      },
      { value: 'heuristic', label: 'heuristic' },
      { value: 'llm', label: 'llm' },
      { value: 'hybrid', label: 'hybrid' },
      { value: 'direct-model', label: 'direct-model' },
    ],
    [t]
  );
  const fallbackOptions = useMemo(
    () => [
      {
        value: '',
        label: t('config_management.visual.sections.orchestrator.classifier_fb_default', {
          defaultValue: '(unset — heuristic)',
        }),
      },
      { value: 'heuristic', label: 'heuristic' },
      { value: 'default-category', label: 'default-category' },
      { value: 'fail', label: 'fail' },
    ],
    [t]
  );

  const llmActive =
    values.classifierKind === 'llm' ||
    values.classifierKind === 'hybrid' ||
    values.classifierKind === 'direct-model' ||
    values.classifierLlmEnabled;

  return (
    <div className={styles.blockStack}>
      <div className={styles.fieldShell}>
        <label className={styles.fieldLabel}>
          {t('config_management.visual.sections.orchestrator.classifier_kind', {
            defaultValue: 'Classifier mode',
          })}
        </label>
        <Select
          value={values.classifierKind}
          options={kindOptions}
          disabled={disabled}
          onChange={(nextValue) =>
            onChange({ classifierKind: nextValue as OrchestratorClassifierKind })
          }
        />
        <div className={styles.fieldHint}>
          {t('config_management.visual.sections.orchestrator.classifier_kind_hint', {
            defaultValue:
              'heuristic = deterministic match only. llm = LLM picks a category name. hybrid = heuristic, then LLM on miss. direct-model = LLM picks a catalog id directly from per-entry instructions paragraphs.',
          })}
        </div>
      </div>

      <label className={styles.toggleRow}>
        <span>
          {t('config_management.visual.sections.orchestrator.classifier_first_match', {
            defaultValue: 'Heuristic: first-match-wins',
          })}
        </span>
        <input
          type="checkbox"
          checked={values.classifierFirstMatchWins}
          disabled={disabled}
          onChange={(e) => onChange({ classifierFirstMatchWins: e.target.checked })}
        />
      </label>

      <div className={styles.subsection}>
        <div className={styles.subsectionHeader}>
          <h3 className={styles.subsectionTitle}>
            {t('config_management.visual.sections.orchestrator.classifier_llm_title', {
              defaultValue: 'LLM classifier',
            })}
          </h3>
          <p className={styles.subsectionDescription}>
            {t('config_management.visual.sections.orchestrator.classifier_llm_desc', {
              defaultValue:
                'Used when mode is llm, hybrid, or direct-model. Pick a cheap, fast upstream — the call is per-request and cached by request hash.',
            })}
          </p>
        </div>

        <label className={styles.toggleRow}>
          <span>
            {t('config_management.visual.sections.orchestrator.classifier_llm_enabled', {
              defaultValue: 'Enable LLM classifier',
            })}
          </span>
          <input
            type="checkbox"
            checked={values.classifierLlmEnabled}
            disabled={disabled}
            onChange={(e) => onChange({ classifierLlmEnabled: e.target.checked })}
          />
        </label>

        <div className={styles.stringListRow}>
          <input
            className="input"
            placeholder={t(
              'config_management.visual.sections.orchestrator.classifier_llm_provider_ph',
              { defaultValue: 'provider (e.g. openai-compatibility, claude)' }
            )}
            aria-label="LLM classifier provider"
            value={values.classifierLlmProvider}
            disabled={disabled || !llmActive}
            onChange={(e) => onChange({ classifierLlmProvider: e.target.value })}
          />
          <input
            className="input"
            placeholder={t(
              'config_management.visual.sections.orchestrator.classifier_llm_model_ph',
              { defaultValue: 'model (e.g. gpt-5-mini)' }
            )}
            aria-label="LLM classifier model"
            value={values.classifierLlmModel}
            disabled={disabled || !llmActive}
            onChange={(e) => onChange({ classifierLlmModel: e.target.value })}
          />
        </div>

        <div className={styles.stringListRow}>
          <input
            className="input"
            type="number"
            inputMode="numeric"
            placeholder={t(
              'config_management.visual.sections.orchestrator.classifier_llm_timeout_ph',
              { defaultValue: 'timeout-ms (default 800)' }
            )}
            aria-label="LLM classifier timeout"
            value={values.classifierLlmTimeoutMs}
            disabled={disabled || !llmActive}
            onChange={(e) => onChange({ classifierLlmTimeoutMs: e.target.value })}
          />
          <input
            className="input"
            type="number"
            inputMode="numeric"
            placeholder={t(
              'config_management.visual.sections.orchestrator.classifier_llm_cache_ph',
              { defaultValue: 'cache-ttl-seconds (default 300)' }
            )}
            aria-label="LLM classifier cache TTL"
            value={values.classifierLlmCacheTtlSeconds}
            disabled={disabled || !llmActive}
            onChange={(e) => onChange({ classifierLlmCacheTtlSeconds: e.target.value })}
          />
          <input
            className="input"
            type="number"
            inputMode="numeric"
            placeholder={t(
              'config_management.visual.sections.orchestrator.classifier_llm_maxchars_ph',
              { defaultValue: 'max-input-chars (default 4000)' }
            )}
            aria-label="LLM classifier max input chars"
            value={values.classifierLlmMaxInputChars}
            disabled={disabled || !llmActive}
            onChange={(e) => onChange({ classifierLlmMaxInputChars: e.target.value })}
          />
        </div>

        <textarea
          className="input"
          rows={4}
          placeholder={t(
            'config_management.visual.sections.orchestrator.classifier_llm_prompt_ph',
            {
              defaultValue:
                'Optional routing prompt override. Placeholders: {{categories}}, {{models}}, {{request}}.',
            }
          )}
          aria-label="LLM classifier prompt template"
          value={values.classifierLlmPromptTemplate}
          disabled={disabled || !llmActive}
          onChange={(e) => onChange({ classifierLlmPromptTemplate: e.target.value })}
        />

        <div className={styles.stringListRow}>
          <div className={styles.fieldShell}>
            <label className={styles.fieldLabel}>
              {t('config_management.visual.sections.orchestrator.classifier_llm_fallback', {
                defaultValue: 'On error',
              })}
            </label>
            <Select
              value={values.classifierLlmFallbackOnError}
              options={fallbackOptions}
              disabled={disabled || !llmActive}
              onChange={(nextValue) =>
                onChange({
                  classifierLlmFallbackOnError: nextValue as OrchestratorClassifierFallback,
                })
              }
            />
          </div>
          <input
            className="input"
            placeholder={t(
              'config_management.visual.sections.orchestrator.classifier_llm_default_cat_ph',
              { defaultValue: 'default-category (used with default-category fallback)' }
            )}
            aria-label="Default category"
            value={values.classifierLlmDefaultCategory}
            disabled={disabled || !llmActive}
            onChange={(e) => onChange({ classifierLlmDefaultCategory: e.target.value })}
          />
        </div>
      </div>
    </div>
  );
});
