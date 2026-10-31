import { useCallback, useEffect, useId, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { useAnchorPosition } from '@/hooks/useAnchorPosition';
import { MenuScrollArea } from '@/components/ui/MenuScrollArea';
import { IconCheck, IconChevronDown } from '@/components/ui/icons';
import selectStyles from '@/components/ui/Select.module.scss';
import { MODEL_SPEED_SELECTION_LIMIT, sortModelSpeedCandidates, type ModelSpeedCandidate } from './modelSpeedSelection';
import styles from './ModelSpeedPicker.module.scss';

export function ModelSpeedPicker({ candidates, selected, onChange, onReset, disabled = false, onOpenChange }: {
  candidates: readonly ModelSpeedCandidate[];
  selected: readonly string[];
  onChange: (models: string[]) => void;
  onReset: () => void;
  disabled?: boolean;
  onOpenChange?: (open: boolean) => void;
}) {
  const { t } = useTranslation();
  const touch = useMediaQuery('(pointer: coarse)');
  const id = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [highlight, setHighlight] = useState(0);
  const [position, setPosition] = useState<CSSProperties | null>(null);
  const isOpen = open && !disabled;
  const label = t('usage_stats.model_speed_models');
  const limitReached = selected.length >= MODEL_SPEED_SELECTION_LIMIT;
  const visible = useMemo(() => {
    const all = [...candidates];
    // 当前区间不再出现的选中模型仍可取消，候选排序保持 API 的 Token 顺序。
    const known = new Set(all.map((item) => item.model));
    for (const model of selected) if (!known.has(model)) all.push({ model, total_tokens: 0 });
    const search = query.trim().toLowerCase();
    const sorted = sortModelSpeedCandidates(all);
    return search ? sorted.filter((item) => item.model.toLowerCase().includes(search)) : sorted;
  }, [candidates, query, selected]);

  const close = useCallback((focus = false) => {
    setOpen(false);
    if (focus) triggerRef.current?.focus({ preventScroll: true });
  }, []);
  const updatePosition = useCallback((rect: DOMRect) => {
    const viewport = window.visualViewport;
    const width = viewport?.width ?? window.innerWidth;
    const height = viewport?.height ?? window.innerHeight;
    const leftEdge = (viewport?.offsetLeft ?? 0) + 8;
    const topEdge = (viewport?.offsetTop ?? 0) + 8;
    const dropdownWidth = Math.min(320, width - 16);
    const below = topEdge + height - 16 - rect.bottom - 8;
    const above = rect.top - topEdge - 8;
    const down = below >= 220 || below >= above;
    setPosition({ position: 'absolute', left: window.scrollX + Math.max(leftEdge, Math.min(rect.right - dropdownWidth, leftEdge + width - 16 - dropdownWidth)),
      top: window.scrollY + (down ? rect.bottom + 8 : rect.top - 8), width: dropdownWidth,
      maxHeight: Math.max(0, Math.min(360, down ? below : above)), transform: down ? undefined : 'translateY(-100%)', zIndex: 2010 });
  }, []);
  useAnchorPosition(isOpen, triggerRef, updatePosition);

  const positioned = isOpen && position !== null;
  useEffect(() => { onOpenChange?.(isOpen); }, [isOpen, onOpenChange]);
  useEffect(() => {
    if (!positioned) return;
    if (!touch) searchRef.current?.focus({ preventScroll: true });
    const outside = (event: globalThis.PointerEvent) => {
      const target = event.target as Node;
      if (!triggerRef.current?.contains(target) && !dropdownRef.current?.contains(target)) close();
    };
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [close, positioned, touch]);
  useEffect(() => {
    if (!isOpen) return;
    const option = document.getElementById(`${id}-option-${highlight}`);
    const viewport = dropdownRef.current?.querySelector<HTMLElement>('[data-menu-scroll-viewport]');
    if (!option || !viewport) return;
    const bounds = option.getBoundingClientRect();
    const area = viewport.getBoundingClientRect();
    if (bounds.top < area.top) viewport.scrollTop += bounds.top - area.top;
    else if (bounds.bottom > area.bottom) viewport.scrollTop += bounds.bottom - area.bottom;
  }, [highlight, id, isOpen, position]);

  const toggle = (index: number) => {
    const model = visible[index]?.model;
    if (!model) return;
    const active = selected.includes(model);
    if (!active && limitReached) return;
    onChange(active ? selected.filter((item) => item !== model) : [...selected, model]);
  };
  const handleKeyDown = (event: KeyboardEvent) => {
    if (event.nativeEvent.isComposing) return;
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(true); }
    else if (event.target !== searchRef.current && (event.target as HTMLElement).getAttribute('role') !== 'option') return;
    else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      setHighlight((current) => (current + (event.key === 'ArrowDown' ? 1 : -1) + visible.length) % Math.max(1, visible.length));
    } else if (event.key === 'Enter' || (event.key === ' ' && event.target !== searchRef.current)) {
      event.preventDefault(); toggle(highlight);
    }
  };

  return <>
    <button ref={triggerRef} type="button" className={`${selectStyles.trigger} ${styles.trigger}`} disabled={disabled}
      aria-label={label} aria-haspopup="listbox" aria-expanded={isOpen} aria-controls={isOpen ? `${id}-list` : undefined}
      onClick={() => { if (isOpen) close(); else { setQuery(''); setHighlight(0); setOpen(true); } }}
      onKeyDown={(event) => { if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); setQuery(''); setHighlight(0); setOpen(true); } else if (event.key === 'Escape') close(); }}>
      <span>{label} · {selected.length}/{MODEL_SPEED_SELECTION_LIMIT}</span>
      <span className={selectStyles.triggerIcon} aria-hidden="true"><IconChevronDown size={14} /></span>
    </button>
    {isOpen && position && createPortal(
      <div ref={dropdownRef} className={`${selectStyles.dropdown} ${styles.dropdown}`} style={position} data-model-speed-picker
        onKeyDown={handleKeyDown} onBlur={(event) => { if (event.relatedTarget && !event.currentTarget.contains(event.relatedTarget) && event.relatedTarget !== triggerRef.current) close(); }}>
        <input ref={searchRef} className={`${selectStyles.trigger} ${selectStyles.searchInput} ${styles.search}`} type="search"
          aria-label={t('usage_stats.model_speed_search')} role="combobox" aria-expanded="true" aria-autocomplete="list" aria-controls={`${id}-list`}
          aria-activedescendant={visible.length > 0 ? `${id}-option-${Math.min(highlight, visible.length - 1)}` : undefined}
          placeholder={t('usage_stats.model_speed_search')} value={query} autoComplete="off" autoCapitalize="none" spellCheck={false}
          onChange={(event) => { setQuery(event.target.value); setHighlight(0); }} />
        <div id={`${id}-list`} role="listbox" aria-label={label} aria-multiselectable="true" className={styles.options}>
        <MenuScrollArea>
            {visible.map((item, index) => {
              const active = selected.includes(item.model);
              const unavailable = !active && limitReached;
              return <button key={item.model} id={`${id}-option-${index}`} type="button" role="option" tabIndex={-1}
                aria-selected={active} aria-disabled={unavailable || undefined} disabled={unavailable} title={item.model}
                className={`${selectStyles.option} ${active ? selectStyles.optionActive : ''} ${index === highlight ? selectStyles.optionHighlighted : ''} ${unavailable ? selectStyles.optionDisabled : ''}`}
                onMouseDown={(event) => event.preventDefault()} onClick={() => toggle(index)}>
                <span className={selectStyles.optionLabel}>{item.model}</span>
                {active && <span className={selectStyles.optionSuffix} aria-hidden="true"><IconCheck size={12} /></span>}
              </button>;
            })}
        </MenuScrollArea>
        </div>
        {visible.length === 0 && <div className={selectStyles.noResults} role="status">{t('usage_stats.model_speed_no_results')}</div>}
        <div className={styles.footer}>
          <span role="status">{t('usage_stats.model_speed_limit', { count: MODEL_SPEED_SELECTION_LIMIT })}</span>
          <button type="button" data-model-speed-reset className={styles.reset} onClick={onReset}>{t('usage_stats.model_speed_reset')}</button>
        </div>
      </div>, document.body,
    )}
  </>;
}
