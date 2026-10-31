import { useCallback, useEffect, useRef, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import { useAnchorPosition } from '@/hooks/useAnchorPosition';
import { buildUsageChartTooltipStyle, type UsageChartTheme } from '@/utils/usage/chartConfig';
import styles from './ModelSpeedChart.module.scss';

export function ModelSpeedTooltip({ canvasRef, x, y, touch, theme, title, rows, dismiss }: {
  canvasRef: RefObject<HTMLCanvasElement | null>;
  x: number;
  y: number;
  touch: boolean;
  theme: UsageChartTheme;
  title: string;
  rows: { model: string; color: string; detail: string; secondary: string; highlighted?: boolean }[];
  dismiss: () => void;
}) {
  const tooltipStyle = buildUsageChartTooltipStyle(theme);
  const panelRef = useRef<HTMLDivElement>(null);
  const update = useCallback((rect: DOMRect) => {
    const panel = panelRef.current;
    if (!panel) return;
    const viewport = window.visualViewport;
    const width = viewport?.width ?? window.innerWidth;
    const height = viewport?.height ?? window.innerHeight;
    const leftEdge = (viewport?.offsetLeft ?? 0) + 8;
    const topEdge = (viewport?.offsetTop ?? 0) + 8;
    panel.style.maxWidth = `${width - 16}px`;
    const bounds = panel.getBoundingClientRect();
    const rightEdge = leftEdge + width - 16;
    const bottomEdge = topEdge + height - 16;
    const cursorX = rect.left + x;
    const cursorY = rect.top + y;
    const preferredLeft = cursorX + 10 + bounds.width <= rightEdge ? cursorX + 10 : cursorX - bounds.width - 10;
    panel.style.left = `${window.scrollX + (touch ? leftEdge : Math.max(leftEdge, Math.min(preferredLeft, rightEdge - bounds.width)))}px`;
    panel.style.top = `${window.scrollY + (touch ? bottomEdge - bounds.height : Math.max(topEdge, Math.min(cursorY - bounds.height / 2, bottomEdge - bounds.height)))}px`;
  }, [touch, x, y]);
  useAnchorPosition(true, canvasRef, update);
  useEffect(() => {
    const outside = (event: PointerEvent) => {
      if (event.target instanceof Node && event.target !== canvasRef.current) dismiss();
    };
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') dismiss(); };
    document.addEventListener('pointerdown', outside);
    document.addEventListener('keydown', escape);
    return () => { document.removeEventListener('pointerdown', outside); document.removeEventListener('keydown', escape); };
  }, [canvasRef, dismiss]);

  // 浮层只展示数据，让鼠标移动与离开继续传给画布；样式与 Token Usage Over Time 共用。
  return createPortal(<div ref={panelRef} role="tooltip" className={styles.tooltip} data-model-speed-tooltip
    style={{ pointerEvents: 'none', backgroundColor: tooltipStyle.backgroundColor, color: tooltipStyle.bodyColor,
      borderColor: tooltipStyle.borderColor, borderWidth: tooltipStyle.borderWidth, padding: tooltipStyle.padding }}>
    <strong className={styles.tooltipTitle} style={{ color: tooltipStyle.titleColor, marginBottom: tooltipStyle.titleMarginBottom }}>{title}</strong>
    <ol className={styles.tooltipRows} style={{ gap: tooltipStyle.bodySpacing }}>
      {rows.map((row) => <li key={row.model} className={row.highlighted ? styles.tooltipRowHighlighted : undefined}
        data-model-speed-highlighted={row.highlighted || undefined}>
        <span className={styles.tooltipDot} style={{ backgroundColor: row.color }} aria-hidden="true" />
        <span><strong>{row.model}</strong> · <strong className={styles.tooltipValue} style={{ color: tooltipStyle.titleColor }} data-model-speed-value>{row.detail}</strong>
          <br /><small className={styles.tooltipSecondary}>{row.secondary}</small></span>
      </li>)}
    </ol>
  </div>, document.body);
}
