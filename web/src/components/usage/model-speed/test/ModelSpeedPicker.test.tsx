// @vitest-environment happy-dom
import { act, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ModelSpeedPicker } from '../ModelSpeedPicker';

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
globalThis.IS_REACT_ACT_ENVIRONMENT = true;
const candidates = Array.from({ length: 6 }, (_, index) => ({ model: `model-${index}`, total_tokens: 100 - index }));
function Harness({ initial = ['model-0'] }: { initial?: string[] }) {
  const [selected, setSelected] = useState(initial);
  return <ModelSpeedPicker candidates={candidates} selected={selected} onChange={setSelected} onReset={() => setSelected(candidates.slice(0, 5).map((item) => item.model))} />;
}

describe('model speed picker', () => {
  let container: HTMLDivElement;
  let root: Root;
  beforeEach(() => { container = document.createElement('div'); document.body.append(container); root = createRoot(container); });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.restoreAllMocks(); });
  const open = async (initial?: string[]) => {
    await act(async () => root.render(<Harness initial={initial} />));
    await act(async () => container.querySelector<HTMLButtonElement>('button')!.click());
  };
  const options = () => Array.from(document.querySelectorAll<HTMLButtonElement>('[data-model-speed-picker] [role="option"]'));
  const input = () => document.querySelector<HTMLInputElement>('[data-model-speed-picker] input')!;
  const search = async (value: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input(), value);
      input().dispatchEvent(new Event('input', { bubbles: true }));
    });
  };

  it('searches full model names, keeps the popup open and enforces the five model limit', async () => {
    await open(candidates.slice(0, 5).map((item) => item.model));
    expect(options()).toHaveLength(6);
    expect(options()[5].disabled).toBe(true);
    await act(async () => options()[0].click());
    expect(options()[5].disabled).toBe(false);
    await act(async () => options()[5].click());
    expect(options()[5].getAttribute('aria-selected')).toBe('true');
    await search('MODEL-5');
    expect(options()).toHaveLength(1);
    expect(options()[0].textContent).toBe('model-5');
    await search('not-here');
    expect(options()).toHaveLength(0);
    expect(document.querySelector('[data-model-speed-picker] [role="status"]')!.textContent).toBe('usage_stats.model_speed_no_results');
  });

  it('supports keyboard selection, Escape, outside pointer and reset', async () => {
    await open([]);
    await act(async () => input().dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true })));
    await act(async () => input().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })));
    expect(options()[1].getAttribute('aria-selected')).toBe('true');
    await act(async () => input().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    expect(document.querySelector('[data-model-speed-picker]')).toBeNull();
    expect(document.activeElement).toBe(container.querySelector('button'));
    await act(async () => container.querySelector<HTMLButtonElement>('button')!.click());
    const reset = document.querySelector<HTMLButtonElement>('[data-model-speed-reset]')!;
    await act(async () => {
      input().dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
      reset.focus();
    });
    const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true });
    await act(async () => reset.dispatchEvent(enter));
    expect(enter.defaultPrevented).toBe(false);
    expect(options().filter((item) => item.getAttribute('aria-selected') === 'true')).toHaveLength(1);
    await act(async () => reset.click());
    expect(options().filter((item) => item.getAttribute('aria-selected') === 'true')).toHaveLength(5);
    await act(async () => document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true })));
    expect(document.querySelector('[data-model-speed-picker]')).toBeNull();
  });

  it('keeps an absent selected model available for cancellation', async () => {
    await open(['model-0', 'old-model-full-name']);
    expect(options().at(-1)!.textContent).toBe('old-model-full-name');
    expect(options().at(-1)!.getAttribute('aria-selected')).toBe('true');
    await act(async () => options().at(-1)!.click());
    expect(options().some((item) => item.textContent === 'old-model-full-name')).toBe(false);
  });
});
