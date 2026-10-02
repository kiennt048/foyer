import type { SVGProps } from "react";

type P = SVGProps<SVGSVGElement> & { size?: number };

function Icon({ size = 16, children, ...rest }: P & { children: React.ReactNode }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.75}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...rest}
    >
      {children}
    </svg>
  );
}

export const Doorway = (p: P) => (
  <Icon {...p} strokeWidth={1.6}>
    <path d="M4 21V5.5A1.5 1.5 0 0 1 5.5 4h8A1.5 1.5 0 0 1 15 5.5V21" />
    <path d="M2.5 21h19" />
    <path d="M15 9h3.5A1.5 1.5 0 0 1 20 10.5V21" />
    <circle cx="11.75" cy="12.75" r="0.9" fill="currentColor" stroke="none" />
  </Icon>
);

export const Gauge = (p: P) => (
  <Icon {...p}>
    <path d="M4 18a8 8 0 1 1 16 0" />
    <path d="m12 14 4-3.5" />
  </Icon>
);

export const Router = (p: P) => (
  <Icon {...p}>
    <rect x="3" y="13" width="18" height="7" rx="1.5" />
    <path d="M7 16.5h.01M10.5 16.5h.01" />
    <path d="M12 10V4M8.5 6.5 12 4l3.5 2.5" />
  </Icon>
);

export const Ticket = (p: P) => (
  <Icon {...p}>
    <path d="M3 9.5V7a1 1 0 0 1 1-1h16a1 1 0 0 1 1 1v2.5a2.5 2.5 0 0 0 0 5V17a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-2.5a2.5 2.5 0 0 0 0-5Z" />
    <path d="M14 6v2M14 11v2M14 16v2" strokeDasharray="0.1 3.4" />
  </Icon>
);

export const Wifi = (p: P) => (
  <Icon {...p}>
    <path d="M2.5 8.5a15 15 0 0 1 19 0" />
    <path d="M5.5 12a10.5 10.5 0 0 1 13 0" />
    <path d="M8.5 15.5a6 6 0 0 1 7 0" />
    <circle cx="12" cy="19" r="1" fill="currentColor" stroke="none" />
  </Icon>
);

export const People = (p: P) => (
  <Icon {...p}>
    <circle cx="9" cy="8" r="3.25" />
    <path d="M3 19a6 6 0 0 1 12 0" />
    <path d="M16 5.5a3.25 3.25 0 0 1 0 5M17.5 14.2A6 6 0 0 1 21 19" />
  </Icon>
);

export const Search = (p: P) => (
  <Icon {...p}>
    <circle cx="10.5" cy="10.5" r="6.5" />
    <path d="m15.5 15.5 4 4" />
  </Icon>
);

export const Copy = (p: P) => (
  <Icon {...p} strokeWidth={1.6}>
    <rect x="9" y="9" width="11" height="11" rx="2" />
    <path d="M5 15V5a2 2 0 0 1 2-2h8" />
  </Icon>
);

export const Filter = (p: P) => (
  <Icon {...p} strokeWidth={1.6}>
    <path d="M4 6h16M7 12h10M10 18h4" />
  </Icon>
);

export const Check = (p: P) => (
  <Icon {...p} strokeWidth={2.25}>
    <path d="m4.5 12.5 5 5 10-11" />
  </Icon>
);

export const Alert = (p: P) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 7.5v5.5" />
    <circle cx="12" cy="16.5" r="1" fill="currentColor" stroke="none" />
  </Icon>
);

/**
 * An alarm, as opposed to a caution — and a **triangle**, which is the whole point.
 *
 * `Alert` and `Info` are the same circle with a dot moved 5.8px at `size={16}`, so
 * with only those two glyph buckets across eight status levels, `urgent` would share
 * one with `warning`: between an alarm and a caution there would be no non-hue
 * difference at all, and the tints sit at deliberately equal luminance so hue would
 * be every bit of it.
 *
 * A triangle rather than a fifth circle because a circle at 11px in a pill is a
 * circle whatever is inside it. The silhouette is what survives the size, the
 * greyscale and the colour-deficient reader.
 */
export const Danger = (p: P) => (
  <Icon {...p}>
    <path d="M12 3.6 21.2 19.4H2.8L12 3.6Z" />
    <path d="M12 9.5v4" />
    <circle cx="12" cy="16.6" r="0.9" fill="currentColor" stroke="none" />
  </Icon>
);

export const Info = (p: P) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 16.5V11" />
    <circle cx="12" cy="7.75" r="1" fill="currentColor" stroke="none" />
  </Icon>
);

export const Clock = (p: P) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 7v5.25l3.25 2" />
  </Icon>
);

export const Chevron = (p: P) => (
  <Icon {...p}>
    <path d="m9 5 7 7-7 7" />
  </Icon>
);

export const ChevronDown = (p: P) => (
  <Icon {...p}>
    <path d="m5 9 7 7 7-7" />
  </Icon>
);

export const Plus = (p: P) => (
  <Icon {...p}>
    <path d="M12 5v14M5 12h14" />
  </Icon>
);

export const Printer = (p: P) => (
  <Icon {...p} strokeWidth={1.6}>
    <path d="M7 8V3h10v5" />
    <rect x="3" y="8" width="18" height="8" rx="2" />
    <path d="M7 14h10v7H7z" />
  </Icon>
);

export const Refresh = (p: P) => (
  <Icon {...p}>
    <path d="M20 11a8 8 0 1 0-.6 4" />
    <path d="M20 5v6h-6" />
  </Icon>
);

export const Key = (p: P) => (
  <Icon {...p}>
    <circle cx="8" cy="12" r="4" />
    <path d="M12 12h9M18 12v3.5M15.5 12v2.5" />
  </Icon>
);

export const Slash = (p: P) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="m6 18 12-12" />
  </Icon>
);

export const Inbox = (p: P) => (
  <Icon {...p} strokeWidth={1.5}>
    <path d="M3 13h5l1.5 3h5L16 13h5" />
    <path d="M4.6 5.5h14.8L21 13v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-5Z" />
  </Icon>
);

/** The walled garden. A boundary, not a padlock — this encloses, it does not secure. */
export const Shield = (p: P) => (
  <Icon {...p} strokeWidth={1.6}>
    <path d="M12 3 4.5 6v6c0 4.2 3 7.6 7.5 9 4.5-1.4 7.5-4.8 7.5-9V6L12 3Z" />
  </Icon>
);

/** Sign-in attempts: a door that did not open. */
export const DoorBarred = (p: P) => (
  <Icon {...p} strokeWidth={1.6}>
    <path d="M5 21V4.5A1.5 1.5 0 0 1 6.5 3h8A1.5 1.5 0 0 1 16 4.5V21" />
    <path d="M3 21h18" />
    <path d="M8.5 12h5" />
  </Icon>
);

/**
 * An operator: a person with a pass, not a guest.
 *
 * Deliberately not `People`, which is Accounts — the guests — and deliberately not
 * `Key`, which is the shared secret on the access-point screen. Two nav rows drawn
 * with one glyph are two rows an operator tells apart by reading, which is what the
 * glyph is there to save them.
 */
export const IdCard = (p: P) => (
  <Icon {...p} strokeWidth={1.6}>
    <rect x="3" y="5" width="18" height="14" rx="2" />
    <circle cx="9" cy="11" r="2" />
    <path d="M6 16c.6-1.3 1.7-2 3-2s2.4.7 3 2" />
    <path d="M15 10.5h3.5M15 14h3.5" />
  </Icon>
);

/** The audit log: a record read in time order, and never edited. */
export const Ledger = (p: P) => (
  <Icon {...p} strokeWidth={1.6}>
    <path d="M5 3.5h12.5A1.5 1.5 0 0 1 19 5v15.5H6.5A1.5 1.5 0 0 1 5 19V3.5Z" />
    <path d="M8.5 3.5v17" />
    <path d="M11 8h5M11 12h5M11 16h3" />
  </Icon>
);

/** A controller: the box in the rack, not the radio on the ceiling. */
export const Server = (p: P) => (
  <Icon {...p} strokeWidth={1.6}>
    <rect x="3" y="4" width="18" height="7" rx="1.5" />
    <rect x="3" y="13" width="18" height="7" rx="1.5" />
    <path d="M7 7.5h.01M7 16.5h.01" />
  </Icon>
);
