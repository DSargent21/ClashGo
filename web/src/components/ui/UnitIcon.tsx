import React from 'react';

/**
 * Compact line glyphs for the units in the static army planning profiles.
 * Kept separate from the general Icon set: these are illustrative silhouettes,
 * not interface affordances, and the list grows with the strategy data.
 */
const GLYPHS: Record<string, React.ReactNode> = {
  'Electro Dragon': (
    <>
      <path d="M3 12c1-4 4-6 8-6h4l4-3-1 5 2 3-4 2c-1 4-4 6-8 6l-3 2-1-4-3-2z" />
      <path d="M11 8l-3 5h3l-2 5 6-6h-3l2-4" />
    </>
  ),
  Balloon: (
    <>
      <ellipse cx="12" cy="9" rx="6" ry="7" />
      <path d="M12 16v3M9 20h6l-1.5-2h-3z" />
      <path d="M9 6c.5-1 1.5-1.5 3-1.5" />
    </>
  ),
  Valkyrie: (
    <>
      <path d="M6 8c0-3 2.7-5 6-5s6 2 6 5v6c0 3-2.7 5-6 5s-6-2-6-5z" />
      <path d="M6 9L2 6v6l4 1M18 9l4-3v6l-4 1" />
      <path d="M9 13h6" />
    </>
  ),
  'Stone Slammer': (
    <>
      <path d="M3 20h18M5 20v-7h7v3h7v4" />
      <circle cx="9" cy="9" r="3.2" />
      <path d="M9 5.8V3M5.8 9H3M9 12.2V15M12.2 9H15" />
    </>
  ),
  'Rage Spell': (
    <>
      <path d="M9 3h6M10 3v5l-4 7a4 4 0 003.6 6h4.8A4 4 0 0018 15l-4-7V3" />
      <path d="M7.5 15h9M12 11l-1 2h2l-1 2" />
    </>
  ),
  'Ice Spell': (
    <>
      <path d="M12 2v20M4 7l16 10M20 7L4 17" />
      <path d="M9 4l3 2 3-2M9 20l3-2 3 2" />
    </>
  ),
  'Earthquake Spell': (
    <>
      <path d="M3 19h18" />
      <path d="M5 16l4-7 4 4 3-8 3 11" />
      <path d="M8 3l1 4M17 2l-1 4" />
    </>
  ),
  'Barbarian King': (
    <>
      <path d="M4 8l3 10h10l3-10-5 4-3-7-3 7z" />
      <path d="M7 21h10M10 13h4" />
    </>
  ),
  'Archer Queen': (
    <>
      <path d="M5 4c8 2 8 14 0 16" />
      <path d="M5 4l13 8-13 8" />
      <path d="M7 12h12M19 12l-3-2M19 12l-3 2" />
    </>
  ),
  'Grand Warden': (
    <>
      <circle cx="7" cy="6" r="3" />
      <path d="M9.5 8.5L20 20" />
      <path d="M13 13l4-2M16 16l4-2" />
    </>
  ),
  'Minion Prince': (
    <>
      <path d="M12 11C9 6 5 5 3 6c1 5 4 7 9 7" />
      <path d="M12 11c3-5 7-6 9-5-1 5-4 7-9 7" />
      <path d="M8 19h8M12 13v6" />
    </>
  ),
  'Dragon Duke': (
    <>
      <path d="M4 8l3-4 3 3h4l3-3 3 4-2 8-5 5-5-5z" />
      <path d="M9 12h1M14 12h1M10 16h4" />
    </>
  ),
  'Royal Champion': (
    <>
      <path d="M12 3v12M9 5l3-3 3 3" />
      <path d="M7 12h10l-1 7-4 2-4-2z" />
      <path d="M10 15h4" />
    </>
  ),
};

const UnitIcon: React.FC<{ name: string; size?: number }> = ({ name, size = 14 }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.7"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    {GLYPHS[name] ?? <path d="M12 3l2.5 5.5L20 11l-5.5 2.5L12 19l-2.5-5.5L4 11l5.5-2.5z" />}
  </svg>
);

export default UnitIcon;
