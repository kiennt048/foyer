/** @jsxImportSource preact */
/**
 * Inline SVG only.
 *
 * An icon font or a sprite file would be a second request the portal cannot afford
 * and an external resource that would have to be added to the walled garden on
 * every driver. These are drawn at 24×24 and inherit `currentColor`.
 */
import type { JSX } from "preact";

type P = JSX.SVGAttributes<SVGSVGElement>;

const base: P = {
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  "stroke-width": 1.75,
  "stroke-linecap": "round",
  "stroke-linejoin": "round",
  "aria-hidden": "true",
};

/** The venue mark: a doorway. Foyer — the room you pass through on the way in. */
export const Mark = (p: P) => (
  <svg {...base} stroke-width={1.6} {...p}>
    <path d="M4 21V5.5A1.5 1.5 0 0 1 5.5 4h8A1.5 1.5 0 0 1 15 5.5V21" />
    <path d="M2.5 21h19" />
    <path d="M15 9h3.5A1.5 1.5 0 0 1 20 10.5V21" />
    <circle cx="11.75" cy="12.75" r="0.9" fill="currentColor" stroke="none" />
  </svg>
);

export const Ticket = (p: P) => (
  <svg {...base} {...p}>
    <path d="M3 9.5V7a1 1 0 0 1 1-1h16a1 1 0 0 1 1 1v2.5a2.5 2.5 0 0 0 0 5V17a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-2.5a2.5 2.5 0 0 0 0-5Z" />
    <path d="M14 6v2M14 11v2M14 16v2" stroke-dasharray="0.1 3.4" />
  </svg>
);

export const Person = (p: P) => (
  <svg {...base} {...p}>
    <circle cx="12" cy="8" r="3.75" />
    <path d="M4.5 20a7.5 7.5 0 0 1 15 0" />
  </svg>
);

export const Phone = (p: P) => (
  <svg {...base} {...p}>
    <rect x="7" y="2.75" width="10" height="18.5" rx="2" />
    <path d="M11 18h2" />
  </svg>
);

export const Chevron = (p: P) => (
  <svg {...base} {...p}>
    <path d="m9 5 7 7-7 7" />
  </svg>
);

export const ArrowLeft = (p: P) => (
  <svg {...base} {...p}>
    <path d="M19 12H5m0 0 6-6m-6 6 6 6" />
  </svg>
);

export const Check = (p: P) => (
  <svg {...base} stroke-width={2.25} {...p}>
    <path d="m4.5 12.5 5 5 10-11" />
  </svg>
);

export const Alert = (p: P) => (
  <svg {...base} {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 7.5v5.5" />
    <circle cx="12" cy="16.5" r="1" fill="currentColor" stroke="none" />
  </svg>
);

export const Info = (p: P) => (
  <svg {...base} {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 16.5V11" />
    <circle cx="12" cy="7.75" r="1" fill="currentColor" stroke="none" />
  </svg>
);
