import React from 'react';

/**
 * ClashGO inline icon set.
 *
 * A deliberately small, dependency-free replacement for the Material
 * Symbols font. Every glyph is a 24x24 stroked path drawn with
 * `currentColor`, so icons inherit text color and never trigger a font
 * request. Add new names to `IconName` and `paths` together.
 */
export type IconName =
  | 'dashboard'
  | 'chart'
  | 'users'
  | 'sliders'
  | 'settings'
  | 'play'
  | 'pause'
  | 'stop'
  | 'power'
  | 'search'
  | 'refresh'
  | 'download'
  | 'chevronRight'
  | 'chevronDown'
  | 'chevronLeft'
  | 'chevronUp'
  | 'check'
  | 'x'
  | 'star'
  | 'coin'
  | 'drop'
  | 'bolt'
  | 'clock'
  | 'activity'
  | 'shield'
  | 'device'
  | 'alert'
  | 'trash'
  | 'edit'
  | 'plus'
  | 'minus'
  | 'sun'
  | 'moon'
  | 'sidebar'
  | 'external'
  | 'cpu'
  | 'wifi'
  | 'file'
  | 'info'
  | 'arrowUp'
  | 'arrowDown'
  | 'timer'
  | 'target'
  | 'swords'
  | 'layers'
  | 'flask'
  | 'calendar'
  | 'inbox'
  | 'lock'
  | 'server'
  | 'spark';

const paths: Record<IconName, React.ReactNode> = {
  dashboard: (
    <>
      <rect x="3.5" y="3.5" width="7" height="7" rx="1.8" />
      <rect x="13.5" y="3.5" width="7" height="7" rx="1.8" />
      <rect x="3.5" y="13.5" width="7" height="7" rx="1.8" />
      <rect x="13.5" y="13.5" width="7" height="7" rx="1.8" />
    </>
  ),
  chart: (
    <>
      <path d="M4 20h16" />
      <path d="M7.5 20v-5.5M12 20V7M16.5 20v-8.5" />
    </>
  ),
  users: (
    <>
      <circle cx="9" cy="8.5" r="3.2" />
      <path d="M3.8 19.5c.5-3 2.5-4.7 5.2-4.7s4.7 1.7 5.2 4.7" />
      <path d="M16 5.6a3 3 0 0 1 0 5.8M17.4 15.2c1.7.5 2.7 1.9 3 4.3" />
    </>
  ),
  sliders: (
    <>
      <path d="M4 6.5h16M4 12h16M4 17.5h16" />
      <circle cx="15" cy="6.5" r="2.2" fill="currentColor" stroke="none" />
      <circle cx="9" cy="12" r="2.2" fill="currentColor" stroke="none" />
      <circle cx="16" cy="17.5" r="2.2" fill="currentColor" stroke="none" />
    </>
  ),
  settings: (
    <>
      <path d="M9.6 3.9c.1-.5.6-.9 1.1-.9h2.6c.5 0 1 .4 1.1.9l.2 1.3c.1.4.3.7.6.9l.2.1c.3.2.7.3 1.1.1l1.2-.5c.5-.2 1 0 1.4.5l1.3 2.2c.3.5.2 1.1-.3 1.4l-1 .8c-.3.2-.4.6-.4 1v.3c0 .4.1.8.4 1l1 .8c.4.4.5 1 .3 1.4l-1.3 2.2c-.3.5-.9.7-1.4.5l-1.2-.5c-.4-.1-.8-.1-1.1.1l-.2.1c-.3.2-.5.5-.6.9l-.2 1.3c-.1.5-.6.9-1.1.9h-2.6c-.5 0-1-.4-1.1-.9l-.2-1.3c-.1-.4-.3-.7-.6-.9l-.2-.1c-.3-.2-.7-.3-1.1-.1l-1.2.5c-.5.2-1 0-1.4-.5L3.9 15c-.3-.5-.2-1.1.3-1.4l1-.8c.3-.2.4-.6.4-1v-.3c0-.4-.1-.8-.4-1l-1-.8c-.4-.4-.5-1-.3-1.4l1.3-2.2c.3-.5.9-.7 1.4-.5l1.2.5c.4.1.8.1 1.1-.1l.2-.1c.3-.2.5-.5.6-.9z" />
      <circle cx="12" cy="12" r="2.7" />
    </>
  ),
  play: <path d="M7 4.5v15l13-7.5z" fill="currentColor" stroke="none" />,
  pause: <path d="M7 4h3.5v16H7zM13.5 4H17v16h-3.5z" fill="currentColor" stroke="none" />,
  stop: <rect x="6" y="6" width="12" height="12" rx="2" fill="currentColor" stroke="none" />,
  power: (
    <>
      <path d="M12 3.5v8" />
      <path d="M6.4 6.6a8 8 0 1 0 11.2 0" />
    </>
  ),
  search: (
    <>
      <circle cx="11" cy="11" r="6.5" />
      <path d="M20 20l-3.8-3.8" />
    </>
  ),
  refresh: (
    <>
      <path d="M20 12a8 8 0 1 1-2.34-5.66" />
      <path d="M20 4v5h-5" />
    </>
  ),
  download: (
    <>
      <path d="M12 4v10M7.5 10.5L12 15l4.5-4.5" />
      <path d="M4.5 19.5h15" />
    </>
  ),
  chevronRight: <path d="M9 6l6 6-6 6" />,
  chevronDown: <path d="M6 9l6 6 6-6" />,
  chevronLeft: <path d="M15 6l-6 6 6 6" />,
  chevronUp: <path d="M6 15l6-6 6 6" />,
  check: <path d="M5 12.5l4.5 4.5L19 7.5" />,
  x: <path d="M6 6l12 12M18 6L6 18" />,
  star: <polygon points="12 2.6 15 8.5 21.5 9.4 16.7 14 17.9 20.5 12 17.4 6.1 20.5 7.3 14 2.5 9.4 9 8.5" />,
  coin: (
    <>
      <circle cx="12" cy="12" r="8.2" />
      <path d="M12 7v10M14.5 9.5c-.5-.8-1.4-1.2-2.5-1.2-1.4 0-2.4.7-2.4 1.7 0 2.3 4.8 1.2 4.8 3.5 0 1-1 1.8-2.4 1.8-1.1 0-2-.5-2.5-1.3" />
    </>
  ),
  drop: <path d="M12 3.5s5.8 6.1 5.8 9.7a5.8 5.8 0 1 1-11.6 0C6.2 9.6 12 3.5 12 3.5z" />,
  bolt: <path d="M13 2.5L4.5 13.5H11L9.5 21.5 19 10h-6.5z" />,
  clock: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 7.5V12l3 2" />
    </>
  ),
  activity: <path d="M3 12h4l2.5-6 4 12 2.5-6h5" />,
  shield: (
    <>
      <path d="M12 3.5l7.5 2.8v5.6c0 4.9-3.2 8.2-7.5 10.3-4.3-2.1-7.5-5.4-7.5-10.3V6.3z" />
      <path d="M9 12l2 2 4-4" />
    </>
  ),
  device: (
    <>
      <rect x="6.5" y="3" width="11" height="18" rx="2.4" />
      <path d="M10.5 18h3" />
    </>
  ),
  alert: (
    <>
      <path d="M12 3.8L21 20H3z" />
      <path d="M12 9.5v4.6M12 17.2v.3" />
    </>
  ),
  trash: (
    <>
      <path d="M4.5 6.5h15M9.5 6V4.5h5V6M6.5 6.5l1 13h9l1-13" />
      <path d="M10 10.5v5M14 10.5v5" />
    </>
  ),
  edit: (
    <>
      <path d="M4 20l1-4L16.5 4.5a2.1 2.1 0 0 1 3 3L8 19z" />
      <path d="M14.5 6.5l3 3" />
    </>
  ),
  plus: <path d="M12 5v14M5 12h14" />,
  minus: <path d="M5 12h14" />,
  sun: (
    <>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.2 5.2l1.4 1.4M17.4 17.4l1.4 1.4M18.8 5.2l-1.4 1.4M6.6 17.4l-1.4 1.4" />
    </>
  ),
  moon: <path d="M20 14.2A8.2 8.2 0 0 1 9.8 4a8.5 8.5 0 1 0 10.2 10.2z" />,
  sidebar: (
    <>
      <rect x="3.5" y="4" width="17" height="16" rx="2.4" />
      <path d="M9.5 4v16" />
    </>
  ),
  external: (
    <>
      <path d="M14 4h6v6M20 4l-8.5 8.5" />
      <path d="M18 14.5V19a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h4.5" />
    </>
  ),
  cpu: (
    <>
      <rect x="6.5" y="6.5" width="11" height="11" rx="2" />
      <rect x="10" y="10" width="4" height="4" rx="0.8" />
      <path d="M9.5 2.8v3.7M14.5 2.8v3.7M9.5 17.5v3.7M14.5 17.5v3.7M2.8 9.5h3.7M2.8 14.5h3.7M17.5 9.5h3.7M17.5 14.5h3.7" />
    </>
  ),
  wifi: (
    <>
      <path d="M3.5 9.2a13 13 0 0 1 17 0M6.5 12.6a8.5 8.5 0 0 1 11 0M9.4 16a4 4 0 0 1 5.2 0" />
      <circle cx="12" cy="19" r="1" fill="currentColor" stroke="none" />
    </>
  ),
  file: (
    <>
      <path d="M6 3.5h7l5 5v12H6z" />
      <path d="M13 3.5v5h5M9 13h6M9 16.5h6" />
    </>
  ),
  info: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 11v5M12 7.8v.3" />
    </>
  ),
  arrowUp: <path d="M12 19V5M6 11l6-6 6 6" />,
  arrowDown: <path d="M12 5v14M6 13l6 6 6-6" />,
  timer: (
    <>
      <circle cx="12" cy="13" r="7.5" />
      <path d="M12 9.5V13l2.5 2M9.5 2.5h5" />
    </>
  ),
  target: (
    <>
      <circle cx="12" cy="12" r="7.5" />
      <circle cx="12" cy="12" r="2.6" />
      <path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3" />
    </>
  ),
  swords: (
    <>
      <path d="M6.8 17.2L19.5 4.5M4.8 15.2l4 4M6.8 17.2l-2.3 2.3" />
      <path d="M17.2 17.2L4.5 4.5M19.2 15.2l-4 4M17.2 17.2l2.3 2.3" />
    </>
  ),
  layers: (
    <>
      <path d="M12 3.5l8.5 4.4L12 12.3 3.5 7.9z" />
      <path d="M3.5 12.2L12 16.6l8.5-4.4M3.5 16.4L12 20.8l8.5-4.4" />
    </>
  ),
  flask: (
    <>
      <path d="M9 3.5h6M10 3.5v5.2L5.6 17a2.2 2.2 0 0 0 1.9 3.4h9a2.2 2.2 0 0 0 1.9-3.4L14 8.7V3.5" />
      <path d="M7.7 14.5h8.6" />
    </>
  ),
  calendar: (
    <>
      <rect x="3.5" y="5" width="17" height="15.5" rx="2" />
      <path d="M3.5 9.5h17M8 3v4M16 3v4" />
    </>
  ),
  inbox: (
    <>
      <path d="M3.5 13.5l2.4-8h12.2l2.4 8v6a1 1 0 0 1-1 1h-15a1 1 0 0 1-1-1z" />
      <path d="M3.5 13.5h4.2l1.3 2h6l1.3-2h4.2" />
    </>
  ),
  lock: (
    <>
      <rect x="5" y="10" width="14" height="10" rx="2" />
      <path d="M8 10V7.5a4 4 0 0 1 8 0V10" />
    </>
  ),
  server: (
    <>
      <rect x="3.5" y="4" width="17" height="6" rx="1.8" />
      <rect x="3.5" y="14" width="17" height="6" rx="1.8" />
      <path d="M7 7h.2M7 17h.2M11 7h5M11 17h5" />
    </>
  ),
  spark: <path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z" />,
};

export interface IconProps {
  name: IconName;
  size?: number;
  strokeWidth?: number;
  className?: string;
  style?: React.CSSProperties;
}

const Icon: React.FC<IconProps> = React.memo(({ name, size = 16, strokeWidth = 1.8, className, style }) => (
  <svg
    className={className}
    style={style}
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth={strokeWidth}
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    {paths[name]}
  </svg>
));

Icon.displayName = 'Icon';

export default Icon;
