import React from 'react';

export interface SwitchProps {
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  /** Accessible name when the switch has no visible label. */
  ariaLabel?: string;
  className?: string;
}

/** Bare switch control — use when the label lives next to it. */
export const Switch = React.memo<SwitchProps>(({
  checked,
  onChange,
  disabled = false,
  ariaLabel,
  className = '',
}) => (
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={ariaLabel}
    disabled={disabled}
    onClick={() => onChange(!checked)}
    className={`switch ${checked ? 'on' : ''} ${className}`.trim()}
  />
));

Switch.displayName = 'Switch';

export interface SwitchRowProps {
  checked: boolean;
  onChange: (next: boolean) => void;
  label: React.ReactNode;
  description?: React.ReactNode;
  disabled?: boolean;
  ariaLabel?: string;
}

/**
 * Full-row switch: whole row is the hit target and carries the switch
 * role, so the visible label is also the accessible name.
 */
export const SwitchRow = React.memo<SwitchRowProps>(({
  checked,
  onChange,
  label,
  description,
  disabled = false,
  ariaLabel,
}) => (
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={ariaLabel}
    disabled={disabled}
    onClick={() => onChange(!checked)}
    className="switch-row"
  >
    <span className="switch-text">
      <b>{label}</b>
      {description ? <span>{description}</span> : null}
    </span>
    <span className={`switch ${checked ? 'on' : ''}`.trim()} aria-hidden="true" />
  </button>
));

SwitchRow.displayName = 'SwitchRow';

export default Switch;
