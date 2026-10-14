import { useCallback, useLayoutEffect, useRef, type KeyboardEvent, type PointerEvent } from "react";
import { clampArtifactWidth } from "./uiPreferences";

interface PanelResizeOptions {
  width: number;
  onResize: (width: number) => void;
  onResizeEnd?: (width: number) => void;
  enabled?: boolean;
}

// 右侧面板共用拖动逻辑：合并同帧移动，结束时按最终坐标保存，取消后不留回调。
export function usePanelResize({ width, onResize, onResizeEnd, enabled = true }: PanelResizeOptions) {
  const callbacks = useRef({ onResize, onResizeEnd });
  const currentWidth = useRef(width);
  const drag = useRef<{ x: number; width: number; pointerId: number; target: HTMLDivElement } | null>(null);
  const frame = useRef<number | null>(null);
  const pendingWidth = useRef<number | null>(null);

  useLayoutEffect(() => {
    callbacks.current = { onResize, onResizeEnd };
    currentWidth.current = width;
  }, [width, onResize, onResizeEnd]);

  const clearFrame = useCallback(() => {
    if (frame.current !== null) cancelAnimationFrame(frame.current);
    frame.current = null;
    pendingWidth.current = null;
  }, []);
  const stopDragging = useCallback(() => {
    clearFrame();
    const previous = drag.current;
    drag.current = null;
    if (previous?.target.hasPointerCapture(previous.pointerId)) previous.target.releasePointerCapture(previous.pointerId);
  }, [clearFrame]);
  const applyWidth = useCallback((value: number) => {
    const next = clampArtifactWidth(value);
    if (next !== currentWidth.current) {
      currentWidth.current = next;
      callbacks.current.onResize(next);
    }
    return next;
  }, []);

  useLayoutEffect(() => {
    if (!enabled) stopDragging();
    return stopDragging;
  }, [enabled, stopDragging]);

  const onPointerDown = useCallback((event: PointerEvent<HTMLDivElement>) => {
    if (!enabled || event.button !== 0 || drag.current) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    drag.current = { x: event.clientX, width: currentWidth.current, pointerId: event.pointerId, target: event.currentTarget };
  }, [enabled]);
  const onPointerMove = useCallback((event: PointerEvent<HTMLDivElement>) => {
    const start = drag.current;
    if (!start || start.pointerId !== event.pointerId || !event.currentTarget.hasPointerCapture(event.pointerId)) return;
    pendingWidth.current = clampArtifactWidth(start.width + start.x - event.clientX);
    if (frame.current !== null) return;
    frame.current = requestAnimationFrame(() => {
      frame.current = null;
      const next = pendingWidth.current;
      pendingWidth.current = null;
      if (next !== null && drag.current) applyWidth(next);
    });
  }, [applyWidth]);
  const onPointerUp = useCallback((event: PointerEvent<HTMLDivElement>) => {
    const start = drag.current;
    if (!start || start.pointerId !== event.pointerId) return;
    const next = clampArtifactWidth(start.width + start.x - event.clientX);
    stopDragging();
    applyWidth(next);
    callbacks.current.onResizeEnd?.(next);
  }, [applyWidth, stopDragging]);
  const onPointerCancel = useCallback((event: PointerEvent<HTMLDivElement>) => {
    if (drag.current?.pointerId === event.pointerId) stopDragging();
  }, [stopDragging]);
  const onKeyDown = useCallback((event: KeyboardEvent<HTMLDivElement>) => {
    if (!enabled) return;
    const width = currentWidth.current;
    const next = event.key === "ArrowLeft" ? width + 20 : event.key === "ArrowRight" ? width - 20 : event.key === "Home" ? 420 : event.key === "End" ? 640 : null;
    if (next === null) return;
    event.preventDefault();
    stopDragging();
    const applied = applyWidth(next);
    callbacks.current.onResizeEnd?.(applied);
  }, [enabled, applyWidth, stopDragging]);

  return { onPointerDown, onPointerMove, onPointerUp, onPointerCancel, onLostPointerCapture: onPointerCancel, onKeyDown };
}
