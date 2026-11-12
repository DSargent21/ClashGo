import React from 'react';
import { createPortal } from 'react-dom';
import Icon from './Icon';

export interface DrawerProps {
  open: boolean;
  onClose: () => void;
  kicker?: React.ReactNode;
  title: React.ReactNode;
  children: React.ReactNode;
  footer?: React.ReactNode;
}

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

/**
 * Right-side drawer used for raid reports and the account editor.
 *
 * Handles the things a raw fixed panel always forgets: ESC to close,
 * scrim click, body scroll lock, initial focus, focus restore, and a
 * Tab loop so keyboard users cannot wander behind the scrim.
 */
const Drawer: React.FC<DrawerProps> = ({ open, onClose, kicker, title, children, footer }) => {
  const panelRef = React.useRef<HTMLElement>(null);
  const titleId = React.useId();
  const closeRef = React.useRef(onClose);
  closeRef.current = onClose;
  const [rendered, setRendered] = React.useState(open);
  const [closing, setClosing] = React.useState(false);

  // Keep the panel mounted long enough to play its exit transition.
  React.useEffect(() => {
    if (open) {
      setRendered(true);
      setClosing(false);
      return;
    }
    if (!rendered) return;
    setClosing(true);
    const timer = window.setTimeout(() => {
      setRendered(false);
      setClosing(false);
    }, 280);
    return () => window.clearTimeout(timer);
  }, [open, rendered]);

  React.useEffect(() => {
    if (!open) return;

    const previouslyFocused = document.activeElement as HTMLElement | null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    const focusTimer = window.setTimeout(() => {
      const panel = panelRef.current;
      const target = panel?.querySelector<HTMLElement>('[data-autofocus]') ?? panel;
      target?.focus();
    }, 0);

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        closeRef.current();
        return;
      }
      if (event.key !== 'Tab' || !panelRef.current) return;

      const focusable = Array.from(panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (focusable.length === 0) {
        event.preventDefault();
        panelRef.current.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', onKeyDown);
    return () => {
      window.clearTimeout(focusTimer);
      document.removeEventListener('keydown', onKeyDown);
      document.body.style.overflow = previousOverflow;
      previouslyFocused?.focus?.();
    };
  }, [open]);

  if (!rendered) return null;

  return createPortal(
    <>
      <div className={`scrim${closing ? ' closing' : ''}`} onClick={onClose} aria-hidden="true" />
      <aside
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={`drawer${closing ? ' closing' : ''}`}
      >
        <header className="drawer-head">
          <div>
            {kicker ? <div className="drawer-kicker">{kicker}</div> : null}
            <h2 id={titleId}>{title}</h2>
          </div>
          <button
            type="button"
            data-autofocus
            className="icon-btn plain"
            onClick={onClose}
            aria-label="Close panel"
          >
            <Icon name="x" size={16} />
          </button>
        </header>
        <div className="drawer-body">{children}</div>
        {footer ? <div className="drawer-foot">{footer}</div> : null}
      </aside>
    </>,
    document.body,
  );
};

export default Drawer;
