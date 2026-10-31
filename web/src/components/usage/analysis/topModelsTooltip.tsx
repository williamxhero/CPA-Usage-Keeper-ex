import { useCallback, useEffect, useRef, useState, type PointerEvent, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import type { Chart, TooltipModel } from 'chart.js';
import { buildUsageChartTooltipStyle, type UsageChartTheme } from '@/utils/usage/chartConfig';
import { useAnchorPosition } from '@/hooks/useAnchorPosition';
import styles from './AnalysisPanel.module.scss';

type TooltipContent = {
  source: object;
  signature: string;
  x: number;
  y: number;
  touch: boolean;
  title: string;
  rows: { text: string; color: string }[];
  footer: string;
};

function TopModelsTooltip({ content, canvasRef, panelRef, isMobile, theme }: {
  content: TooltipContent;
  canvasRef: RefObject<HTMLCanvasElement | null>;
  panelRef: RefObject<HTMLDivElement | null>;
  isMobile: boolean;
  theme: UsageChartTheme;
}) {
  const tooltipStyle = buildUsageChartTooltipStyle(theme);
  const updatePosition = useCallback((rect: DOMRect) => {
    const panel = panelRef.current;
    if (!panel) return;
    const viewport = window.visualViewport;
    const width = viewport?.width ?? window.innerWidth;
    const height = viewport?.height ?? window.innerHeight;
    const leftEdge = (viewport?.offsetLeft ?? 0) + 8;
    const topEdge = (viewport?.offsetTop ?? 0) + 8;
    const mobile = isMobile || content.touch;
    panel.style.width = 'max-content';
    panel.style.maxWidth = `${width - 16}px`;
    panel.style.maxHeight = `${Math.min(380, mobile ? height * 0.6 : height - 16)}px`;
    const bounds = panel.getBoundingClientRect();
    const rightEdge = leftEdge + width - 16;
    const bottomEdge = topEdge + height - 16;
    const anchorX = rect.left + content.x;
    const anchorY = rect.top + content.y;
    const preferredLeft = anchorX + 10 + bounds.width <= rightEdge ? anchorX + 10 : anchorX - bounds.width - 10;
    const left = mobile ? leftEdge : Math.max(leftEdge, Math.min(preferredLeft, rightEdge - bounds.width));
    const top = mobile ? bottomEdge - bounds.height : Math.max(topEdge, Math.min(anchorY - bounds.height / 2, bottomEdge - bounds.height));
    // 使用页面坐标并跟踪可视视口，兼容手机地址栏收缩、缩放与页面滚动。
    panel.style.left = `${left + window.scrollX}px`;
    panel.style.top = `${top + window.scrollY}px`;
  }, [content, isMobile, panelRef]);
  useAnchorPosition(true, canvasRef, updatePosition);

  return (
    <div ref={panelRef} role="tooltip" data-top-models-tooltip className={styles.topModelsTooltip}
      style={{ pointerEvents: content.touch ? 'auto' : 'none', backgroundColor: tooltipStyle.backgroundColor,
        color: tooltipStyle.bodyColor, borderColor: tooltipStyle.borderColor }}>
      <div className={styles.topModelsTooltipHeader}>
        <strong style={{ color: tooltipStyle.titleColor }}>{content.title}</strong>
      </div>
      <ol key={content.title} className={styles.topModelsTooltipRows}>
        {content.rows.map((row, index) => (
          <li key={index}>
            <span className={styles.topModelsTooltipDot} style={{ backgroundColor: row.color }} aria-hidden="true" />
            <span>{row.text}</span>
          </li>
        ))}
      </ol>
      <strong className={styles.topModelsTooltipFooter}>{content.footer}</strong>
    </div>
  );
}

export function useTopModelsTooltip(source: object, enabled: boolean, isMobile: boolean, theme: UsageChartTheme) {
  const chartRef = useRef<Chart | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);
  const touchInput = useRef(false);
  const [content, setContent] = useState<TooltipContent | null>(null);
  const visible = enabled && content?.source === source ? content : null;

  const dismiss = useCallback(() => {
    const chart = chartRef.current;
    if (chart?.canvas) {
      chart.tooltip?.setActiveElements([], { x: 0, y: 0 });
      chart.setActiveElements([]);
      chart.draw();
    }
    setContent(null);
  }, []);
  const closeOnLeave = useCallback(() => {
    if (!touchInput.current) dismiss();
  }, [dismiss]);

  const external = useCallback(({ chart, tooltip }: { chart: Chart; tooltip: TooltipModel<'bar'> }) => {
    if (tooltip.opacity === 0) {
      closeOnLeave();
      return;
    }
    if (!tooltip.body?.length) {
      setContent(null);
      return;
    }
    chartRef.current = chart;
    canvasRef.current = chart.canvas;
    const title = tooltip.title.join('\n');
    const rows = tooltip.body.map((body, index) => ({
      text: [...body.before, ...body.lines, ...body.after].join('\n'),
      color: String(tooltip.labelColors[index].borderColor),
    }));
    const footer = tooltip.footer.join('\n');
    const signature = JSON.stringify([title, rows, footer]);
    const { caretX: x, caretY: y } = tooltip;
    const touch = touchInput.current;
    setContent((previous) => previous?.source === source && previous.signature === signature
      && previous.x === x && previous.y === y && previous.touch === touch
      ? previous : { source, signature, title, rows, footer, x, y, touch });
  }, [closeOnLeave, source]);

  useEffect(() => {
    if (!visible) return;
    const outside = (event: globalThis.PointerEvent) => {
      if (event.target instanceof Node && event.target !== canvasRef.current && !panelRef.current?.contains(event.target)) dismiss();
    };
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') dismiss(); };
    document.addEventListener('pointerdown', outside);
    document.addEventListener('keydown', escape);
    return () => {
      document.removeEventListener('pointerdown', outside);
      document.removeEventListener('keydown', escape);
    };
  }, [dismiss, visible]);
  // 桌面浮层不截获指针，滚轮在画布上仍可浏览长列表；触摸浮层可直接滚动。
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!visible || !canvas) return;
    const scroll = (event: WheelEvent) => {
      const rows = panelRef.current?.querySelector('ol');
      if (!rows || rows.scrollHeight <= rows.clientHeight) return;
      event.preventDefault();
      rows.scrollTop += event.deltaY;
    };
    canvas.addEventListener('wheel', scroll, { passive: false });
    return () => canvas.removeEventListener('wheel', scroll);
  }, [visible]);

  return {
    external,
    onPointerDown: (event: PointerEvent) => { touchInput.current = event.pointerType === 'touch'; },
    onPointerMove: (event: PointerEvent) => { if (event.pointerType === 'mouse') touchInput.current = false; },
    tooltip: visible ? createPortal(
      <TopModelsTooltip content={visible} canvasRef={canvasRef} panelRef={panelRef} isMobile={isMobile}
        theme={theme} />,
      document.body,
    ) : null,
  };
}
