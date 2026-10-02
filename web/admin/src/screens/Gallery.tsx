import { Fragment, useState, type ReactNode } from "react";
import { ApiError } from "../api.js";
import { STATUS_LEVELS, type StatusLevel } from "../format.js";
import * as Icons from "../ui/icons.js";
import {
  ConfirmDialog,
  Empty,
  Heartbeat,
  Notice,
  Pill,
  SkeletonRows,
  Toasts,
  type Toast,
} from "../ui/primitives.js";
import {
  Absent,
  BytesCell,
  IdCell,
  Mono,
  Stack,
  Table,
  TimeCell,
  type Column,
} from "../ui/table.js";

/**
 * `/design` — the design-system gallery.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why it exists.** Without it, checking this console's contrast means constructing
 * every state by driving the application: throttle the network to catch a skeleton,
 * find the one access point whose API key has expired to see that pill, open a
 * dialog to see the scrim. That is affordable as a one-off and unaffordable as a
 * habit, and it leaves no regression check — the states are scattered across eight
 * screens. Here they are one page and one screenshot, in both themes at once, which
 * makes a contrast check and its regression check the same artefact.
 *
 * **Why it is not in the navigation.** An operator has no use for it. It is a team
 * surface, reachable by typing the path, absent from the sidebar and absent from the
 * command palette (which builds itself from `NAV` plus the current screen's
 * registered shortcuts, so it excludes this route without being told to). It is also
 * loaded lazily, so an operator never downloads it.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Two rules keep it honest, and they are the difference between a gallery and a
 * second design system:**
 *
 * 1. **Enumerate, never transcribe.** Status levels come from `STATUS_LEVELS`, which
 *    is also what `StatusLevel` is derived from. Icons come from
 *    `import * as Icons`. Text tiers and surfaces come from one array each, declared
 *    once, below. A hand-written list of the eight statuses would be a second place
 *    to forget the ninth — which is exactly the failure the page is meant to prevent.
 *    `gallery.test.tsx` asserts the rendered markup carries every level the type
 *    declares.
 * 2. **Borrow selectors, never values.** There is not a colour, a size or a duration
 *    in this file or in the gallery's stylesheet that also exists elsewhere. Two
 *    states cannot be reached without a pointer — keyboard focus, and the reveal on
 *    a hovered identifier cell — so the real rules gained a `[data-demo~="…"]`
 *    selector rather than the gallery restating what those rules do.
 *
 * **What it deliberately does not cover.** Anything that is a screen rather than a
 * primitive: the sidebar, the site switcher, the command palette, the shortcut
 * sheet, the detail dock, the batch bar, the voucher print sheet. Those are
 * compositions with their own data and their own routes, and a gallery that grows to
 * include whole screens becomes a ninth screen to maintain. It also does not cover
 * the responsive layout, which needs a viewport, not a page.
 */

/* ── the things that are enumerated once ──────────────────────────────────── */

/**
 * The surfaces text is painted on, in the order they stack.
 *
 * The tiers are shown on every one of them because a tier that is legible on the
 * page and not on a sunken table header is the normal failure, not the exotic one —
 * the contrast pass found `--text-muted` passing against the canvas and failing on
 * the header, the hovered row and the checked row, which is the whole reason this
 * matrix is a matrix rather than a row of three swatches.
 */
const SURFACES = [
  { token: "--canvas", where: "The page itself" },
  { token: "--surface", where: "Cards, the table body, inputs, secondary buttons" },
  { token: "--surface-raised", where: "The detail dock, popovers, toasts, dialogs" },
  { token: "--surface-sunken", where: "Table headers, the dialog footer, the sidebar" },
  { token: "--surface-hover", where: "The row under the pointer" },
  { token: "--surface-selected", where: "A checked row, a pressed filter chip" },
] as const;

/**
 * The three text tiers, and what each is for.
 *
 * This section is the artefact that stops the next person collapsing them. They are
 * separated in CIE L* rather than by contrast ratio, because "tell apart at a
 * glance" tracks lightness; the steps are 21.9 and 11.4 in the light theme and 20.7
 * and 13.7 in the dark one, deliberately the same shape.
 */
const TIERS = [
  {
    tier: "primary",
    token: "--text",
    what: "Values. The thing the operator opened the screen to read.",
  },
  {
    tier: "secondary",
    token: "--text-secondary",
    what: "Prose that qualifies a value: descriptions, freshness captions, notes.",
  },
  {
    tier: "muted",
    token: "--text-muted",
    what: "Labels the eye is meant to skip: column headers, units, field hints.",
  },
] as const;

/** Sample rows. Ugly on purpose, as every fixture in this project is. */
interface Row {
  id: string;
  mac: string;
  /* `ssid`, not `hostname`. The contract has no hostname — recovering one needs DHCP
     fingerprinting, which is out of scope — and a design gallery is where an
     invented field would be copied from. */
  ssid: string | null;
  octets: number | null;
  seen: string | null;
}

const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();

const ROWS: Row[] = [
  { id: "3c22fb060be5", mac: "3c:22:fb:06:0b:e5", ssid: "Foyer-Guest", octets: 4_812_004_000, seen: ago(2) },
  // A NAS that reports no SSID — common on the controller-API path, and the reason
  // the absent tier had to stay legible.
  { id: "8ade2b91c407", mac: "8a:de:2b:91:c4:07", ssid: null, octets: 291_004_112, seen: ago(21) },
  // No accounting ever arrived. Not zero, not unlimited — nothing.
  { id: "0242ac110002", mac: "02:42:ac:11:00:02", ssid: null, octets: null, seen: null },
];

/* ── the page ─────────────────────────────────────────────────────────────── */

export function DesignGallery() {
  return (
    <div className="page gallery">
      <div className="page-head">
        <div>
          <h1 className="page-title">Design system</h1>
          <p className="page-desc">
            Every primitive in every state, both themes, one page. Not in the navigation and
            not in the command palette — reach it by typing <Mono>/design</Mono>. Reviewing a
            change to a token or a primitive means comparing this page before and after.
          </p>
        </div>
      </div>

      <div className="gal-themes">
        <ThemePanel scheme="light" />
        <ThemePanel scheme="dark" />
      </div>
    </div>
  );
}

/**
 * One theme's worth of the system.
 *
 * `color-scheme` is the entire mechanism: every token in `tokens.css` is a
 * `light-dark()` pair, and this attribute chooses which half resolves. Nothing else
 * in the console pins a scheme — the shell follows the operating system.
 */
function ThemePanel({ scheme }: { scheme: "light" | "dark" }) {
  const [toasts, setToasts] = useState<Toast[]>([
    { id: 1, message: "Voucher HGT-4K2M revoked.", undo: () => {} },
    { id: 2, message: "Shared secret regenerated. It is shown once, above." },
  ]);
  const [modalOpen, setModalOpen] = useState(false);

  return (
    <section className="gal-theme" data-scheme={scheme}>
      <h2 className="gal-theme-label">{scheme} theme</h2>

      <Section
        title="Text tiers"
        why="Three tiers, on every surface they are painted on. A dense table is read by scanning the value and skipping the label, which only works while the label is visibly a label — so the tiers have to stay distinguishable at a glance on the sunken header and the checked row, not only on the page."
      >
        <div className="gal-stack" style={{ marginBottom: "0.75rem" }}>
          {TIERS.map((tier) => (
            <div key={tier.tier} className="gal-item">
              <span className="gal-tier-text" data-tier={tier.tier}>
                <strong>{tier.tier}</strong> — {tier.what}
              </span>
              <span className="gal-caption">{tier.token}</span>
            </div>
          ))}
        </div>

        <div className="gal-matrix">
          {/* The two cells are grid children, so the map may not wrap them in a box. */}
          {SURFACES.map((surface) => (
            <Fragment key={surface.token}>
              <div className="gal-matrix-head" style={{ background: `var(${surface.token})` }}>
                <b>{surface.token}</b>
                {surface.where}
              </div>
              <div className="gal-matrix-cell" style={{ background: `var(${surface.token})` }}>
                {TIERS.map((tier) => (
                  <span key={tier.tier} className="gal-tier-text" data-tier={tier.tier}>
                    {tier.tier}
                  </span>
                ))}
              </div>
            </Fragment>
          ))}
        </div>
      </Section>

      <Section
        title="Status"
        why="Eight levels, keyed on what the operator must do rather than on how bad it sounds. The tints sit at deliberately equal luminance so no status is louder than another by accident, and every pill is dark text on a pale tint so the label carries the meaning and hue is redundant reinforcement. Separation from the page and separation from each other are both real properties, and the light theme had a defect in exactly the second."
      >
        <div className="gal-row">
          {STATUS_LEVELS.map((level) => (
            <Item key={level} caption={level}>
              <Pill level={level} label={level} />
            </Item>
          ))}
        </div>
      </Section>

      <Section
        title="Freshness is a segment, not a hue"
        why="Status and freshness are independent facts: a session is Online, and separately its last accounting sample is twenty minutes old. Encoding the second as a colour change corrupts the first, so it joins on as a second segment of the same pill. This is the system's most distinctive decision and the one most likely to be tidied away by someone who has not read why."
      >
        <div className="gal-row">
          <Item caption="live · sampling">
            <Pill
              level="positive"
              label="Online"
              icon={<Heartbeat still={false} />}
              qualifiers={[]}
            />
          </Item>
          <Item caption="live · stopped">
            <Pill level="positive" label="Online" icon={<Heartbeat still />} />
          </Item>
          <Item caption="+ stale qualifier">
            <Pill
              level="positive"
              label="Online"
              icon={<Heartbeat still />}
              qualifiers={[{ label: "20 min", icon: <Icons.Clock size={11} /> }]}
            />
          </Item>
          <Item caption="+ absent qualifier">
            <Pill
              level="neutral"
              label="No data"
              icon={<Icons.Slash size={11} />}
              qualifiers={[{ label: "no accounting", icon: <Icons.Clock size={11} /> }]}
            />
          </Item>
          <Item caption="two qualifiers">
            <Pill
              level="caution-minor"
              label="Orphaned"
              icon={<Icons.Alert size={11} />}
              qualifiers={[
                { label: "40 days", icon: <Icons.Clock size={11} /> },
                { label: "no stop record" },
              ]}
            />
          </Item>
        </div>
        <p className="gal-why" style={{ marginTop: "0.75rem" }}>
          The heartbeat is the only motion in this console that means something. It pulses
          while interim updates arrive and stops when they stop; under{" "}
          <Mono>prefers-reduced-motion</Mono> it keeps breathing at a third of the amplitude
          and with the scale removed, because freezing it would make the two states above
          identical.
        </p>
      </Section>

      <Section
        title="Notices"
        why="The same eight levels at block scale. Rendering all of them beside each other is what found the missing `positive` rule — the component accepted the level, compiled, and painted a transparent block with body text on it."
      >
        <div className="notice-stack">
          {STATUS_LEVELS.map((level) => (
            <Notice
              key={level}
              level={level}
              title={`${level} — a one-line title that names the fault`}
              action={
                level === "urgent" ? (
                  <button type="button" className="btn btn-sm">
                    View
                  </button>
                ) : undefined
              }
            >
              The body says what to do about it, in a sentence an operator can act on
              without opening a manual.
            </Notice>
          ))}
        </div>
      </Section>

      <Section
        title="Buttons"
        why="One primary action per page and no more; everything else is secondary or ghost. Disabled is a sunken fill with a muted label rather than an opacity, because unavailable and illegible are different things — the old 0.55 took a disabled primary to 2.60:1. Destructive is an ink, not a fill: btn-danger alone is the destructive *trigger* — the treatment a row Delete wears — and the fill requires btn-primary btn-danger together, which is the confirm button inside a ConfirmDialog and nothing else. Both are shown because the fill is still sanctioned, and a reference that stopped drawing it would read as a treatment withdrawn."
      >
        {(["", "btn-primary", "btn-danger", "btn-primary btn-danger", "btn-ghost"] as const).map((variant) => (
          <div className="gal-row" key={variant || "default"} style={{ marginBottom: "0.5rem" }}>
            {(["rest", "disabled"] as const).map((state) => (
              <Item key={state} caption={`${variant || "btn"} · ${state}`}>
                <button type="button" className={`btn ${variant}`} disabled={state === "disabled"}>
                  Regenerate secret
                </button>
              </Item>
            ))}
          </div>
        ))}
        <div className="gal-row">
          <Item caption="btn-sm">
            <button type="button" className="btn btn-sm">
              Clear filters
            </button>
          </Item>
          <Item caption="btn-icon">
            <button type="button" className="btn btn-icon" aria-label="Refresh">
              <Icons.Refresh size={15} />
            </button>
          </Item>
          <Item caption="with icon">
            <button type="button" className="btn btn-primary">
              <Icons.Plus size={15} />
              Add access point
            </button>
          </Item>
          <Item caption="focus ring">
            <button type="button" className="btn" data-demo="focus">
              Keyboard focus
            </button>
          </Item>
          {/* Stated rather than mocked up: there is no busy affordance. Every caller
              that has one — the confirm dialog is the only one — passes `busy` and
              gets the disabled rendering with the same label. That is a gap, not a
              style; it belongs in an issue, not in a swatch here. */}
          <Item caption="busy → disabled">
            <button type="button" className="btn btn-danger" disabled>
              Regenerating…
            </button>
          </Item>
        </div>
      </Section>

      <Section
        title="Filter chips"
        why="A chip's own fill sits at 1.06 against the page, so the border is the whole of what says this is a control. Pressed moves the fill and the border together; hover moves only the fill, so hovering a chip never looks like pressing one."
      >
        <div className="chip-group">
          <button type="button" className="chip" aria-pressed="false">
            Online <span className="chip-count">142</span>
          </button>
          <button type="button" className="chip" aria-pressed="true">
            Orphaned <span className="chip-count">3</span>
          </button>
          <button type="button" className="chip" aria-pressed="false" data-demo="focus">
            Ended
          </button>
        </div>
      </Section>

      <Section
        title="Cell types"
        why="Monospace, tabular figures, alignment and truncation strategy are properties of what a value is, not of the column it lands in — so no screen can forget to monospace a MAC. Identifier truncation keeps the tail, because MACs and Acct-Session-Ids carry their discriminating characters at the end."
      >
        <div className="gal-row">
          <Item caption="Absent">
            <Absent hint="No accounting has arrived for this session." />
          </Item>
          <Item caption="Mono">
            <Mono>10.42.7.180</Mono>
          </Item>
          <Item caption="BytesCell">
            <BytesCell value={4_812_004_000} />
          </Item>
          <Item caption="BytesCell · null">
            <BytesCell value={null} />
          </Item>
          <Item caption="TimeCell">
            <TimeCell iso={ago(21)} />
          </Item>
          <Item caption="TimeCell · absent">
            <TimeCell iso={null} absent="Never used." />
          </Item>
        </div>
        <div className="gal-row" style={{ marginTop: "0.75rem" }}>
          <Item caption="IdCell">
            <IdCell value="3c:22:fb:06:0b:e5" subject="MAC address" onFilter={() => {}} />
          </Item>
          {/* The actions are revealed on `mouseenter` *and* `focusin`; without this the
              gallery would be the one page that cannot show them. */}
          <Item caption="IdCell · actions revealed">
            <span className="demo-reveal">
              <IdCell value="a1b2c3d4e5f60718" subject="session ID" onFilter={() => {}} />
            </span>
          </Item>
        </div>
        <div className="gal-row" style={{ marginTop: "0.75rem" }}>
          {/* An account and its display name, not a client and its hostname: there
              is no hostname, the contract removed the field, and a gallery is the
              last place that should demonstrate one. */}
          <Item caption="Stack">
            {/* ASCII on purpose. A guard test fails on any Vietnamese character in
                `src/` outside `i18n/vi/`, which is what keeps copy in its catalogues — venue text
                arrives at runtime and is marked with `lang`, never written here. */}
            <Stack primary="quan.nguyen" secondary="Quan Nguyen" />
          </Item>
          <Item caption="Stack · absent secondary">
            <Stack primary="lan.tran" absentText="no display name" />
          </Item>
          <Item caption="Stack · no secondary">
            <Stack primary={<Mono>02:42:ac:11:00:02</Mono>} />
          </Item>
          <Item caption="stack-sub-caution">
            <Stack
              primary={<Mono>02:42:ac:11:00:02</Mono>}
              secondary={<span className="stack-sub-caution">clock skew</span>}
            />
          </Item>
        </div>
      </Section>

      <Section
        title="The four fetch states"
        why="Loading, error, empty, data — in that order and never as nested ternaries, because they are four different designs and the difference is the point. Loading is skeleton rows rather than a spinner so the column layout survives. Zero-results-from-filters is a fifth rendering of empty and must not be confused with never-had-data: one offers to clear the filters, the other offers to create the first thing."
      >
        <div className="gal-stack">
          <Labelled caption="1 · loading">
            <Table columns={COLUMNS} rows={[]} rowKey={(r) => r.id} isLoading />
          </Labelled>
          <Labelled caption="2 · error">
            <Table
              columns={COLUMNS}
              rows={[]}
              rowKey={(r) => r.id}
              /*
                A real refusal, not a hand-written sentence. The table renders copy
                from the catalogue, so an `Error` with prose in it would show the
                *unrecognised* fallback here — and the gallery would be reviewing a
                sentence the product cannot produce, which is the fixtures rule in
                the one place a design is looked at hardest.
              */
              error={
                new ApiError(409, {
                  type: "/problems/controller-unreachable",
                  title: "Cannot read from this controller",
                  status: 409,
                  params: { name: "UniFi — Cloud Key Gen2", reachability: "unreachable" },
                })
              }
            />
          </Labelled>
          <Labelled caption="3 · empty, never had data">
            <Table
              columns={COLUMNS}
              rows={[]}
              rowKey={(r) => r.id}
              empty={
                <Empty
                  title="No access points."
                  description="Register one to start accepting authentication requests."
                  action={
                    <button type="button" className="btn btn-primary">
                      Add access point
                    </button>
                  }
                />
              }
            />
          </Labelled>
          <Labelled caption="4 · empty, filters exclude everything">
            <Table
              columns={COLUMNS}
              rows={[]}
              rowKey={(r) => r.id}
              filtered
              onClearFilters={() => {}}
            />
          </Labelled>
          <Labelled caption="5 · data — cursor row, checked rows, refetch indicator in the header">
            <Table
              columns={COLUMNS}
              rows={ROWS}
              rowKey={(r) => r.id}
              rowLabel={(r) => r.mac}
              onRowClick={() => {}}
              selectedKey={ROWS[1]!.id}
              refreshing
              selection={{
                checked: new Set([ROWS[0]!.id, ROWS[1]!.id]),
                onToggle: () => {},
                onToggleAll: () => {},
                rowLabel: (r) => `Select ${r.mac}`,
                allLabel: "Select every visible session",
              }}
            />
          </Labelled>
        </div>
      </Section>

      <Section
        title="Empty states"
        why="Three of them, never one. This is the never-had-data state: active language, and an action whose label mirrors the title — 'No access points.' then 'Add access point', never 'Get started'."
      >
        <div className="gal-stack">
          <Empty title="No vouchers." />
          <Empty
            title="No accounts."
            description="Accounts are for staff who sign in with a username, not for guests."
            action={
              <button type="button" className="btn btn-primary">
                Add account
              </button>
            }
          />
        </div>
      </Section>

      <Section
        title="Skeletons"
        why="Never a spinner in a table: skeleton rows keep the column layout, so the table does not collapse and re-expand while someone is reading it. The shimmer is the only thing saying the request is still in flight, which is why reduced motion slows it instead of freezing it — a frozen shimmer reads as a hung page."
      >
        <div className="gal-stack">
          <span className="skeleton" style={{ width: "60%" }} />
          <span className="skeleton" style={{ width: "35%" }} />
          <span className="skeleton" style={{ height: 2, width: "100%" }} />
          <div className="card" style={{ maxWidth: "12rem" }}>
            <div className="metric-label">Clients online</div>
            <div className="metric-value">
              <span className="skeleton" style={{ width: "3ch", height: "1em" }} />
            </div>
          </div>
          <table>
            <tbody>
              <SkeletonRows rows={2} cols={4} />
            </tbody>
          </table>
        </div>
      </Section>

      <Section
        title="Form fields"
        why="Pointer focus tints the border and widens it with a soft ring; keyboard focus gets the console's one outline, at 2px offset so it lands on the surface behind the control rather than on its own fill. Both are shown, because for a while only the first existed and all four inputs had no keyboard indicator at all."
      >
        <div className="form-grid" style={{ maxWidth: "22rem" }}>
          <div className="field">
            <label htmlFor={`gal-name-${scheme}`}>Access point name</label>
            <input id={`gal-name-${scheme}`} type="text" defaultValue="Reception AP" />
            <p className="field-hint">Shown in the sessions table and on the alarm strip.</p>
          </div>
          <div className="field">
            <label htmlFor={`gal-ring-${scheme}`}>Pointer focus</label>
            <input id={`gal-ring-${scheme}`} type="text" data-demo="focus-ring" defaultValue="10.42.7.1" />
          </div>
          <div className="field">
            <label htmlFor={`gal-kbd-${scheme}`}>Keyboard focus</label>
            <input id={`gal-kbd-${scheme}`} type="text" data-demo="focus" defaultValue="10.42.7.1" />
          </div>
          <div className="field">
            <label htmlFor={`gal-port-${scheme}`}>CoA port</label>
            <input id={`gal-port-${scheme}`} type="number" defaultValue={3799} />
            <p className="field-error">1700 on most Cisco platforms. This one is not numeric.</p>
          </div>
          <div className="field">
            <label htmlFor={`gal-driver-${scheme}`}>Driver</label>
            <select id={`gal-driver-${scheme}`} defaultValue="unifi">
              <option value="unifi">UniFi — controller API</option>
              <option value="mikrotik">MikroTik — RFC 5176 CoA</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor={`gal-off-${scheme}`}>Disabled</label>
            <input id={`gal-off-${scheme}`} type="text" defaultValue="Not editable" disabled />
          </div>
        </div>
      </Section>

      <Section
        title="The secret, shown once"
        why="The API never returns a shared secret. What makes that livable is the metadata beside it — last used, and whether the device is talking at all. At generation it is monospace, `user-select: all`, and then it is gone."
      >
        <div className="secret">7f2a9c41d8e60b3554a1cc9e2f70bd18</div>
        <div className="gal-row" style={{ marginTop: "0.5rem" }}>
          <Pill level="positive" label="Secret set" />
          <Pill level="caution-minor" label="No secret" />
          <Pill level="urgent" label="API key expired" icon={<Icons.Key size={11} />} />
        </div>
      </Section>

      <Section
        title="Environment"
        why="Colour encodes danger of the current context, not goodness of the environment. The live site is the one where a mistake reaches real guests, so it is the one that carries amber."
      >
        <div className="gal-row">
          <span className="env-badge" data-env="live">
            Live
          </span>
          <span className="env-badge" data-env="lab">
            Lab
          </span>
        </div>
      </Section>

      <Section
        title="Destructive confirmation, two tiers"
        why="Reversible actions never come here at all — they happen immediately, with undo. The medium tier names the consequence and the recovery path. The high tier asks for the resource's own name and renders no Cancel button, because typing the name proves you know which resource; typing the word 'confirm' only proves deliberation."
      >
        <div className="demo-overlay gal-stack">
          <ConfirmDialog
            open={false}
            tier="confirm"
            title="Regenerate the shared secret?"
            verb="Regenerate secret"
            consequence="Every EAP conversation in progress on this access point is dropped, and the TLS session cache is cleared. Corporate laptops on site will re-authenticate."
            recovery="The new secret is shown once. Paste it into the access point before anyone reconnects."
            onConfirm={() => {}}
            onCancel={() => {}}
          />
          <ConfirmDialog
            open={false}
            tier="type-name"
            /* An ASCII site name, deliberately: `admin.test.ts` fails the build on a
               Vietnamese string surviving anywhere in `src/`, and the guard is right —
               English is the source of truth for this console. A venue name with full
               diacritics belongs in the fixtures, where the truncation and the font
               stack are actually exercised. */
            title="Delete District 1 — Riverside?"
            verb="Delete site"
            confirmName="District 1 — Riverside"
            consequence="Every access point, voucher and account belonging to this site is deleted with it. Accounting history is retained but is no longer reachable from the console."
            onConfirm={() => {}}
            onCancel={() => {}}
          />
        </div>
        {/*
          A label on the button and the explanation beside it, rather than a sentence
          inside a `.btn`.

          `.btn` is `white-space: nowrap` — correct for a label, and it meant this
          one measured 473px in a 358px column and was the whole of `/design`'s
          remaining 118px of sideways scroll once `.gal-themes` stopped collapsing
          short. The grid was genuinely wrong; this was the
          second cause underneath it, and it is the gallery breaking the console's
          own rule that a control is named in two or three words.
        */}
        <p className="gal-why" style={{ marginTop: "0.75rem" }}>
          The scrim is the one part the cards above cannot show.
        </p>
        <button type="button" className="btn btn-sm" onClick={() => setModalOpen(true)}>
          Open as a real modal
        </button>
        <ConfirmDialog
          open={modalOpen}
          tier="confirm"
          title="Regenerate the shared secret?"
          verb="Regenerate secret"
          consequence="Every EAP conversation in progress on this access point is dropped, and the TLS session cache is cleared."
          recovery="The new secret is shown once."
          onConfirm={() => setModalOpen(false)}
          onCancel={() => setModalOpen(false)}
        />
      </Section>

      <Section
        title="Toasts"
        why="Undo is what makes a low-friction destructive action safe. Where undo is offered the action gets no confirmation dialog as well — one or the other, never both. They are laid out in flow here; in the console they are fixed to the bottom-right corner."
      >
        <div className="demo-overlay">
          <Toasts
            toasts={toasts}
            onDismiss={(id) => setToasts((all) => all.filter((x) => x.id !== id))}
          />
        </div>
      </Section>

      <Section
        title="Icons"
        why="Enumerated from the module rather than listed, so a new icon appears here without anyone remembering to add it. One stroke weight, one 24-unit grid, `currentColor` throughout — an icon is never a second colour decision."
      >
        <div className="gal-row">
          {Object.entries(Icons).map(([name, Glyph]) => (
            <Item key={name} caption={name}>
              <Glyph size={20} />
            </Item>
          ))}
        </div>
      </Section>
    </section>
  );
}

/* ── small local scaffolding ──────────────────────────────────────────────── */

function Section({ title, why, children }: { title: string; why: string; children: ReactNode }) {
  return (
    <section className="gal-section">
      <h3>{title}</h3>
      <p className="gal-why">{why}</p>
      {children}
    </section>
  );
}

function Item({ caption, children }: { caption: string; children: ReactNode }) {
  return (
    <span className="gal-item">
      {children}
      <span className="gal-caption">{caption}</span>
    </span>
  );
}

function Labelled({ caption, children }: { caption: string; children: ReactNode }) {
  return (
    <div className="gal-item">
      <span className="gal-caption">{caption}</span>
      {children}
    </div>
  );
}

/* The columns the fetch-state tables use. Three-way alignment: identifiers and
   categorical values left, quantities right. */
const COLUMNS: Column<Row>[] = [
  {
    key: "mac",
    narrow: "keep",
    header: "Client",
    cell: (r) => (
      <Stack
        primary={<Mono>{r.mac}</Mono>}
        secondary={r.ssid ?? undefined}
        absentText={r.ssid ? undefined : "no SSID reported"}
      />
    ),
  },
  {
    key: "status",
    narrow: "keep",
    header: "Session state",
    cell: (r) =>
      r.octets === null ? (
        <Pill level="neutral" label="No data" icon={<Icons.Slash size={11} />} />
      ) : (
        <Pill
          level="positive"
          label="Online"
          icon={<Heartbeat still={r.seen !== ROWS[0]!.seen} />}
          qualifiers={
            r.seen === ROWS[0]!.seen
              ? []
              : [{ label: "20 min", icon: <Icons.Clock size={11} /> }]
          }
        />
      ),
  },
  { key: "bytes", narrow: "drop", header: "Data in", align: "end", cell: (r) => <BytesCell value={r.octets} /> },
  {
    key: "seen",
    narrow: "drop",
    header: "Last interim update time",
    cell: (r) => <TimeCell iso={r.seen} absent="No accounting has arrived." />,
  },
];
