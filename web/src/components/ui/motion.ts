/**
 * Anchored transient surfaces.
 *
 * Transient UI grows from the control that opened it. The panel gets a
 * transform-origin in panel-local pixels; CSS then runs the scale/opacity
 * keyframes. The fallback keeps keyboard-opened or first-run surfaces stable.
 */
export const SURFACE_EXIT_MS = 160;

const clamp = (value: number, min: number, max: number): number =>
  Math.min(Math.max(value, min), max);

export type SurfacePlacement = 'right' | 'center';

export function setAnchorOrigin(
  panel: HTMLElement | null,
  anchor?: HTMLElement | null,
  placement: SurfacePlacement = 'center',
): void {
  if (!panel) return;

  const width = panel.offsetWidth;
  const height = panel.offsetHeight;
  if (!width || !height) return;

  // The panel is scaled while entering, so getBoundingClientRect() would report
  // its shrunken rect and skew the anchor. Derive the untouched layout rect
  // instead: the right drawer is pinned to the right, everything else centers.
  const left = placement === 'right' ? window.innerWidth - width : (window.innerWidth - width) / 2;
  const top = placement === 'right' ? 0 : (window.innerHeight - height) / 2;
  panel.style.setProperty('--anchor-x', placement === 'right' ? '100%' : '50%');
  panel.style.setProperty('--anchor-y', placement === 'right' ? '0%' : '50%');

  const trigger =
    anchor ??
    (document.activeElement instanceof HTMLElement && document.activeElement !== document.body
      ? document.activeElement
      : null);
  if (!trigger || !trigger.isConnected) return;

  const triggerRect = trigger.getBoundingClientRect();
  if (!triggerRect.width || !triggerRect.height) return;

  panel.style.setProperty(
    '--anchor-x',
    `${clamp(triggerRect.left + triggerRect.width / 2 - left, 0, width)}px`,
  );
  panel.style.setProperty(
    '--anchor-y',
    `${clamp(triggerRect.top + triggerRect.height / 2 - top, 0, height)}px`,
  );
}
