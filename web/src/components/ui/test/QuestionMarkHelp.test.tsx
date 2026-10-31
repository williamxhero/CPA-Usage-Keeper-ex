// @vitest-environment happy-dom

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { QuestionMarkHelp } from '../QuestionMarkHelp'

globalThis.IS_REACT_ACT_ENVIRONMENT = true

const dispatchPointer = async (target: Element, type: string, pointerType: string) => {
  await act(async () => {
    const event = new Event(type, { bubbles: true, cancelable: true })
    Object.defineProperty(event, 'pointerType', { value: pointerType })
    target.dispatchEvent(event)
  })
}

describe('QuestionMarkHelp', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
  })

  it('keeps an accessible description mounted and supports focus and touch dismissal', async () => {
    await act(async () => {
      root.render(
        <QuestionMarkHelp
          label="Compatibility details"
          description="OpenAI Compatible is not supported."
          tooltipClassName="test-tooltip"
          tooltipVisibleClassName="test-tooltip-visible"
        >
          <span>OpenAI Compatible is not supported.</span>
        </QuestionMarkHelp>,
      )
    })

    const button = container.querySelector<HTMLButtonElement>('[data-question-mark-help]')!
    const description = document.getElementById(button.getAttribute('aria-describedby')!)!
    const tooltip = document.getElementById(button.getAttribute('aria-controls')!)!

    expect(button.getAttribute('aria-expanded')).toBe('false')
    expect(description.textContent).toBe('OpenAI Compatible is not supported.')
    expect(tooltip.getAttribute('role')).toBe('tooltip')
    expect(tooltip.getAttribute('aria-hidden')).toBe('true')
    expect(tooltip.parentElement).toBe(document.body)

    await act(async () => button.focus())
    expect(tooltip.getAttribute('aria-hidden')).toBe('false')
    await act(async () => button.blur())
    expect(tooltip.getAttribute('aria-hidden')).toBe('true')

    await dispatchPointer(button, 'pointerdown', 'touch')
    expect(tooltip.getAttribute('aria-hidden')).toBe('false')
    await dispatchPointer(button, 'pointerdown', 'touch')
    expect(tooltip.getAttribute('aria-hidden')).toBe('true')

    await dispatchPointer(button, 'pointerdown', 'touch')
    await dispatchPointer(document.body, 'pointerdown', 'mouse')
    expect(tooltip.getAttribute('aria-hidden')).toBe('true')
  })

  async function renderInlineHelp() {
    await act(async () => {
      root.render(
        <QuestionMarkHelp
          label="Help details"
          description="Help explanation."
          portal={false}
          tooltipClassName="inline-tooltip"
          tooltipVisibleClassName="inline-tooltip-visible"
          buttonProps={{ 'data-inline-help': 'true' }}
          tooltipProps={{ 'data-inline-tooltip': 'true' }}
        >
          Help explanation.
        </QuestionMarkHelp>,
      )
    })

    const button = container.querySelector<HTMLButtonElement>('[data-inline-help]')!
    const tooltip = container.querySelector<HTMLElement>('[data-inline-tooltip]')!

    return { button, tooltip }
  }

  it('closes an inline tooltip when keyboard focus leaves after activation', async () => {
    const { button, tooltip } = await renderInlineHelp()

    expect(button.textContent).toBe('?')
    expect(tooltip.getAttribute('aria-hidden')).toBe('true')
    await act(async () => button.focus())
    await act(async () => button.click())
    expect(tooltip.getAttribute('aria-hidden')).toBe('false')
    await act(async () => button.blur())
    expect(tooltip.getAttribute('aria-hidden')).toBe('true')
  })

  it('does not reopen an inline tooltip when touch taps also emit click events', async () => {
    const { button, tooltip } = await renderInlineHelp()

    await dispatchPointer(button, 'pointerdown', 'touch')
    await act(async () => button.click())
    expect(tooltip.getAttribute('aria-hidden')).toBe('false')

    await dispatchPointer(button, 'pointerdown', 'touch')
    await act(async () => button.click())
    expect(tooltip.getAttribute('aria-hidden')).toBe('true')
  })
})
