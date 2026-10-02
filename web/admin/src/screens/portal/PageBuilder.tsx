import { useEffect, useId, useRef, useState } from "react";
import type {
  Language,
  PortalBlock,
  PortalBlockKind,
  PortalBlockPlacement,
  PortalBlockText,
  PortalDesign,
  PortalSocialNetwork,
} from "@foyer/contract";
import {
  accentTheme,
  ACCENT_PRESETS,
  DEFAULT_ACCENT,
  DESIGN_MAX_BLOCKS,
  hasText,
  isLink,
  isLive,
  localDate,
  type DesignFault,
} from "@foyer/contract";
import { TEMPLATES, newBlockId, type TemplateId } from "@foyer/portal/src/templates.js";
import { lazyCopy } from "../../i18n/catalogue.js";
import { t } from "../../i18n/portal.js";
import { ChevronDown, Info, Ledger, People, Plus, Ticket } from "../../ui/icons.js";
import { ConfirmDialog, Modal, Notice, Pill } from "../../ui/primitives.js";

/**
 * The page an owner builds: a template to start from, a colour, a headline, and the
 * blocks — on the page before sign-in and the page after.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Everything here edits a draft and nothing here saves.** The screen's one Save
 * button is the only write, the live preview beside this shows the draft as it is
 * typed, and Discard puts every block back. That is what makes it safe to hand to
 * somebody who has never seen the product: there is no action on this screen a
 * guest can see before the owner decides they are finished.
 *
 * **Blocks, not a canvas.** The field's page builders (Smart WiFi Platform, Tanaza,
 * Social WiFi) give owners a fixed set of blocks, and the reason here is sharper
 * than ease: the page renders before authentication to anyone within radio range,
 * so venue-authored markup would be stored XSS on a page a guest was just asked to
 * trust. Reordering is Move up / Move down rather than drag and drop — two buttons
 * work from a keyboard, a screen reader and a phone, and a page has twelve blocks
 * at most.
 */

export const LANGUAGE_NAME: Record<Language, string> = {
  get en() {
    return t("portal.language.en");
  },
  get vi() {
    return t("portal.language.vi");
  },
};

/*
 * Buttons and social links are not offered before sign-in at all, rather than
 * offered and refused: the guest's phone cannot reach any other website yet, so
 * the block would be a button that spins. The server refuses the combination too
 * (`link-before-sign-in`), for a client that is not this one.
 */
const KINDS_BY_PLACE: Record<PortalBlockPlacement, readonly PortalBlockKind[]> = {
  welcome: ["notice", "text", "offer", "list"],
  connected: ["text", "offer", "list", "button", "social"],
};

const KIND_NAME: Record<PortalBlockKind, string> = {
  get notice() {
    return t("portal.kind.notice");
  },
  get text() {
    return t("portal.kind.text");
  },
  get offer() {
    return t("portal.kind.offer");
  },
  get list() {
    return t("portal.kind.list");
  },
  get button() {
    return t("portal.kind.button");
  },
  get social() {
    return t("portal.kind.social");
  },
};

const KIND_HINT: Record<PortalBlockKind, string> = {
  get notice() {
    return t("portal.kind.notice.hint");
  },
  get text() {
    return t("portal.kind.text.hint");
  },
  get offer() {
    return t("portal.kind.offer.hint");
  },
  get list() {
    return t("portal.kind.list.hint");
  },
  get button() {
    return t("portal.kind.button.hint");
  },
  get social() {
    return t("portal.kind.social.hint");
  },
};

function KindIcon({ kind }: { kind: PortalBlockKind }) {
  switch (kind) {
    case "notice":
      return <Info className="kind-icon" />;
    case "offer":
      return <Ticket className="kind-icon" />;
    case "button":
      return <Plus className="kind-icon" />;
    case "social":
      return <People className="kind-icon" />;
    default:
      return <Ledger className="kind-icon" />;
  }
}

const NETWORKS: readonly PortalSocialNetwork[] = [
  "facebook",
  "zalo",
  "instagram",
  "tiktok",
  "youtube",
  "google-review",
  "website",
  "phone",
];

const TEMPLATE_NAME: Record<TemplateId, [string, string]> = lazyCopy(() => ({
  cafe: [t("portal.template.cafe"), t("portal.template.cafe.hint")],
  restaurant: [t("portal.template.restaurant"), t("portal.template.restaurant.hint")],
  hotel: [t("portal.template.hotel"), t("portal.template.hotel.hint")],
  shop: [t("portal.template.shop"), t("portal.template.shop.hint")],
  event: [t("portal.template.event"), t("portal.template.event.hint")],
  blank: [t("portal.template.blank"), t("portal.template.blank.hint")],
}));

type AccentId = "foyer" | "coffee" | "amber" | "red" | "rose" | "purple" | "blue" | "green" | "slate";
const ACCENT_NAME: Record<AccentId, string> = {
  get foyer() {
    return t("portal.accent.foyer");
  },
  get coffee() {
    return t("portal.accent.coffee");
  },
  get amber() {
    return t("portal.accent.amber");
  },
  get red() {
    return t("portal.accent.red");
  },
  get rose() {
    return t("portal.accent.rose");
  },
  get purple() {
    return t("portal.accent.purple");
  },
  get blue() {
    return t("portal.accent.blue");
  },
  get green() {
    return t("portal.accent.green");
  },
  get slate() {
    return t("portal.accent.slate");
  },
};

const SOCIAL_NAME: Record<PortalSocialNetwork, string> = {
  get facebook() {
    return t("portal.social.facebook");
  },
  get instagram() {
    return t("portal.social.instagram");
  },
  get tiktok() {
    return t("portal.social.tiktok");
  },
  get zalo() {
    return t("portal.social.zalo");
  },
  get youtube() {
    return t("portal.social.youtube");
  },
  get "google-review"() {
    return t("portal.social.google-review");
  },
  get website() {
    return t("portal.social.website");
  },
  get phone() {
    return t("portal.social.phone");
  },
};

export function isEmptyDesign(design: PortalDesign): boolean {
  return design.blocks.length === 0 && !design.accent && !design.headline.en?.trim() && !design.headline.vi?.trim();
}

// ── templates ────────────────────────────────────────────────────────────────

export function TemplateGallery({
  venueName,
  design,
  onApply,
}: {
  venueName: string;
  design: PortalDesign;
  onApply: (d: PortalDesign) => void;
}) {
  const [pending, setPending] = useState<TemplateId | null>(null);
  const [open, setOpen] = useState(false);
  const titleId = useId();
  const empty = isEmptyDesign(design);

  const apply = (id: TemplateId) => {
    const template = TEMPLATES.find((x) => x.id === id);
    if (!template) return;
    onApply(template.build(venueName.trim() || "Foyer"));
    setPending(null);
    setOpen(false);
  };

  const grid = (
    <ul className="template-grid">
      {TEMPLATES.map((tpl) => (
        <li key={tpl.id}>
          <button
            type="button"
            className="template-tile"
            data-template={tpl.id}
            onClick={() => (empty ? apply(tpl.id) : setPending(tpl.id))}
          >
            <span className="template-name">{TEMPLATE_NAME[tpl.id][0]}</span>
            <span className="template-hint">{TEMPLATE_NAME[tpl.id][1]}</span>
          </button>
        </li>
      ))}
    </ul>
  );

  return (
    <>
      {/*
        A page nobody has designed opens on the templates, because a blank page is the
        hardest thing to ask a non-technical person for. Once there is a page, the
        gallery folds into a button — replacing somebody's work takes a deliberate
        step, and then a confirmation.
      */}
      {empty ? (
        <section className="card portal-section" aria-labelledby={titleId}>
          <h2 className="section-title" id={titleId}>
            {t("portal.templates.title")}
          </h2>
          <p className="section-lede">{t("portal.templates.lede")}</p>
          {grid}
        </section>
      ) : (
        <div className="builder-bar">
          <button type="button" className="btn btn-sm" onClick={() => setOpen(true)}>
            {t("portal.templates.open")}
          </button>
        </div>
      )}

      {open && !pending ? (
        <Modal labelledBy={titleId} onClose={() => setOpen(false)} dismissOnBackdrop>
          <div className="dialog-card dialog-wide">
            <div className="dialog-head">
              <h2 className="dialog-title" id={titleId}>
                {t("portal.templates.title")}
              </h2>
            </div>
            <div className="dialog-body">
              <p>{t("portal.templates.lede")}</p>
              {grid}
            </div>
            <div className="dialog-foot">
              <button type="button" className="btn" onClick={() => setOpen(false)}>
                {t("portal.templates.cancel")}
              </button>
            </div>
          </div>
        </Modal>
      ) : null}

      <ConfirmDialog
        open={pending !== null}
        title={t("portal.templates.replace.title")}
        verb={t("portal.templates.replace.confirm")}
        consequence={t("portal.templates.replace.body")}
        onConfirm={() => {
          if (pending) apply(pending);
        }}
        onCancel={() => setPending(null)}
      />
    </>
  );
}

// ── colour and headline ──────────────────────────────────────────────────────

export function LookSection({
  design,
  onChange,
  editLang,
  defaultHeadline,
}: {
  design: PortalDesign;
  onChange: (d: PortalDesign) => void;
  editLang: Language;
  defaultHeadline: string;
}) {
  const id = useId();
  const accent = design.accent;
  const shown = accent ? accentTheme(accent).light.accent : null;
  const custom = accent !== null && !ACCENT_PRESETS.some((p) => p.hex === accent);

  return (
    <section className="card portal-section">
      <h2 className="section-title">{t("portal.brand.title")}</h2>

      <div className="field">
        <label htmlFor={`${id}-headline`}>{t("portal.headlineFor", LANGUAGE_NAME[editLang])}</label>
        <input
          id={`${id}-headline`}
          value={design.headline[editLang] ?? ""}
          maxLength={60}
          placeholder={defaultHeadline}
          autoComplete="off"
          aria-describedby={`${id}-headline-hint`}
          onChange={(e) => onChange({ ...design, headline: { ...design.headline, [editLang]: e.target.value } })}
        />
        <p className="field-hint" id={`${id}-headline-hint`}>
          {t("portal.headlineHint", defaultHeadline)}
        </p>
      </div>

      <fieldset className="field swatch-field">
        <legend>{t("portal.brand.colour")}</legend>
        <div className="swatches" role="radiogroup" aria-describedby={`${id}-colour-hint`}>
          {ACCENT_PRESETS.map((p) => {
            const name = ACCENT_NAME[p.id as AccentId] ?? p.id;
            return (
              <button
                key={p.id}
                type="button"
                role="radio"
                aria-checked={p.hex === accent}
                aria-label={name}
                title={name}
                className="swatch"
                data-preset={p.id}
                onClick={() => onChange({ ...design, accent: p.hex })}
              >
                {/* The venue's colour is data the owner picked, so it is painted from
                    the value rather than from a token — it is the thing being chosen.
                    The default paints the console's own accent, which is the same
                    teal. */}
                <span
                  className="swatch-chip"
                  style={{ background: p.hex ?? "var(--accent)" }}
                  aria-hidden="true"
                />
              </button>
            );
          })}
          <label className="swatch swatch-custom" data-checked={custom || undefined}>
            <input
              type="color"
              value={accent ?? DEFAULT_ACCENT}
              onChange={(e) => onChange({ ...design, accent: e.target.value.toLowerCase() })}
            />
            <span>{t("portal.brand.custom")}</span>
          </label>
        </div>
        <p className="field-hint" id={`${id}-colour-hint`}>
          {t("portal.brand.colourHint")}
          {accent && shown && shown !== accent ? <> {t("portal.brand.adjusted", shown)}</> : null}
        </p>
      </fieldset>
    </section>
  );
}

// ── blocks ───────────────────────────────────────────────────────────────────

export function BlockList({
  design,
  onChange,
  place,
  onPlace,
  editLang,
  languages,
  fault,
  today: siteToday,
}: {
  design: PortalDesign;
  onChange: (d: PortalDesign) => void;
  place: PortalBlockPlacement;
  onPlace: (p: PortalBlockPlacement) => void;
  /** The site's calendar day (`useSiteToday`); the browser's own when absent. */
  today?: string;
  editLang: Language;
  languages: Language[];
  fault: DesignFault | null;
}) {
  const [open, setOpen] = useState<string | null>(null);
  const [menu, setMenu] = useState(false);
  const addRef = useRef<HTMLButtonElement>(null);
  const focusNext = useRef<string | null>(null);
  const titleId = useId();
  const here = design.blocks.map((b, i) => ({ b, i })).filter(({ b }) => b.placement === place);
  const full = design.blocks.length >= DESIGN_MAX_BLOCKS;
  const today = siteToday ?? localDate();

  /* The block a fault is about opens, on its page, so the field that is wrong is on
     screen when the owner looks for it.

     The two dependencies — `faulted?.id` and `fault?.fault` — are the *identity of
     the fault*: which block, and what is wrong with it. Never the design it was
     found in. `designFault` returns a fresh object on every render, and the list
     re-renders on every keystroke the owner types, so a list that watched the fault
     object (or the design) would re-open a card and re-place it under the cursor,
     mid-edit, forever. Watching the two values instead means: open the offending
     block once, and follow the fault when it moves to another block or changes
     kind — which is what happens the moment the owner starts fixing it.

     `place` and `onPlace` are out for the same reason and cost nothing: `onPlace` is
     a `useState` setter, and after it runs, `place` is the block's own placement, so
     the condition is false. Both are read at run time from the render that
     scheduled the effect, so nothing here is a stale closure. What would change it:
     a fault whose fix leaves the block and the kind identical and must re-run on the
     new design. None of the five in `DesignFault` is that. */
  const faulted = fault ? design.blocks[fault.block] : undefined;
  useEffect(() => {
    if (!faulted) return;
    if (faulted.placement !== place) onPlace(faulted.placement);
    setOpen(faulted.id);
  }, [faulted?.id, fault?.fault]);

  /* A block that was just added gets the cursor in its first field, so adding a
     block and typing into it is one motion. */
  useEffect(() => {
    if (!focusNext.current) return;
    const at = `[data-block="${focusNext.current}"]`;
    document.querySelector<HTMLElement>(`${at} input:not([type=checkbox]), ${at} textarea`)?.focus();
    focusNext.current = null;
  });

  const setBlock = (index: number, next: PortalBlock) =>
    onChange({ ...design, blocks: design.blocks.map((b, i) => (i === index ? next : b)) });

  const move = (index: number, dir: -1 | 1) => {
    // Swap with the neighbour *on this page*: the list interleaves both pages, and
    // "move up" must mean up on the page the owner is looking at.
    const pos = here.findIndex(({ i }) => i === index);
    const other = here[pos + dir];
    if (!other) return;
    const blocks = [...design.blocks];
    [blocks[index], blocks[other.i]] = [blocks[other.i]!, blocks[index]!];
    onChange({ ...design, blocks });
  };

  const remove = (index: number) => {
    // Focus goes to the one control that outlives every block on the page.
    addRef.current?.focus();
    onChange({ ...design, blocks: design.blocks.filter((_, i) => i !== index) });
  };

  const add = (kind: PortalBlockKind) => {
    const block: PortalBlock = {
      id: newBlockId(kind),
      kind,
      placement: place,
      visible: true,
      content: kind === "list" ? { [editLang]: { items: [{ label: "", value: "" }] } } : {},
      ...(kind === "social" ? { links: [{ network: "facebook" as const, url: "" }] } : {}),
      ...(kind === "button" ? { url: "" } : {}),
    };
    onChange({ ...design, blocks: [...design.blocks, block] });
    setMenu(false);
    setOpen(block.id);
    focusNext.current = block.id;
  };

  return (
    <section className="card portal-section" aria-labelledby={titleId}>
      <h2 className="section-title" id={titleId}>
        {t("portal.content.title")}
      </h2>

      {/* Which of the two pages is being edited. A pair of pressed buttons rather
          than tabs: the list below is one region whose contents change, not two
          panels, and the preview follows the same choice. */}
      <div className="segmented" role="group" aria-label={t("portal.previewing.page")}>
        {(["welcome", "connected"] as const).map((p) => (
          <button
            key={p}
            type="button"
            aria-pressed={place === p}
            className="segment"
            data-place={p}
            onClick={() => onPlace(p)}
          >
            {t(p === "welcome" ? "portal.place.welcome" : "portal.place.connected")}
            <span className="segment-count">{design.blocks.filter((b) => b.placement === p).length}</span>
          </button>
        ))}
      </div>

      {place === "welcome" ? (
        <p className="section-lede">{t("portal.place.welcome.hint")}</p>
      ) : (
        <Notice level="caution-minor" title={t("portal.place.connected.title")}>
          {t("portal.place.connected.body")}
        </Notice>
      )}

      {here.length === 0 ? (
        <div className="blocks-empty">
          <p className="blocks-empty-title">{t("portal.blocks.empty.title")}</p>
          <p className="muted">{t("portal.blocks.empty.body")}</p>
        </div>
      ) : (
        <ol className="block-list">
          {here.map(({ b, i }, pos) => (
            <BlockCard
              key={b.id}
              block={b}
              today={today}
              editLang={editLang}
              languages={languages}
              open={open === b.id}
              faulted={fault?.block === i ? fault : null}
              first={pos === 0}
              last={pos === here.length - 1}
              onToggle={() => setOpen(open === b.id ? null : b.id)}
              onChange={(next) => setBlock(i, next)}
              onMove={(dir) => move(i, dir)}
              onRemove={() => remove(i)}
            />
          ))}
        </ol>
      )}

      <div className="add-block">
        <button
          ref={addRef}
          type="button"
          className="btn"
          aria-expanded={menu}
          aria-controls={menu ? `${titleId}-kinds` : undefined}
          aria-disabled={full || undefined}
          onClick={() => {
            if (!full) setMenu(!menu);
          }}
        >
          <Plus />
          {t("portal.blocks.add")}
          <ChevronDown />
        </button>
        {full ? <p className="field-hint">{t("portal.blocks.full", DESIGN_MAX_BLOCKS)}</p> : null}
        {menu && !full ? (
          <ul className="kind-menu" id={`${titleId}-kinds`}>
            {KINDS_BY_PLACE[place].map((kind) => (
              <li key={kind}>
                <button type="button" className="kind-option" data-kind={kind} onClick={() => add(kind)}>
                  <KindIcon kind={kind} />
                  <span>
                    <span className="kind-name">{KIND_NAME[kind]}</span>
                    <span className="kind-hint">{KIND_HINT[kind]}</span>
                  </span>
                </button>
              </li>
            ))}
          </ul>
        ) : null}
        {menu && place === "welcome" ? <p className="field-hint">{t("portal.blocks.linksOnlyAfter")}</p> : null}
      </div>
      {place === "connected" ? <p className="field-hint">{t("portal.place.connected.hint")}</p> : null}
    </section>
  );
}

function summaryOf(block: PortalBlock, lang: Language, languages: Language[]): string {
  const order: Language[] = [lang, ...languages.filter((l) => l !== lang), "vi", "en"];
  for (const l of order) {
    const c = block.content[l];
    const line = c?.title?.trim() || c?.label?.trim() || c?.items?.find((i) => i.label.trim())?.label.trim();
    if (line) return line;
  }
  if (block.kind === "social") {
    const names = (block.links ?? []).map((l) => SOCIAL_NAME[l.network]);
    if (names.length) return names.join(" · ");
  }
  return t("portal.block.untitled");
}

function faultCopy(fault: DesignFault["fault"]): string {
  switch (fault) {
    case "duplicate":
      return t("portal.design.duplicate");
    case "link-before-sign-in":
      return t("portal.design.linkBeforeSignIn");
    case "required":
      return t("portal.field.urlRequired");
    case "not-a-link":
      return t("portal.field.urlInvalid");
    case "ends-before-start":
      return t("portal.schedule.invalid");
  }
}

function BlockCard({
  block,
  today,
  editLang,
  languages,
  open,
  faulted,
  first,
  last,
  onToggle,
  onChange,
  onMove,
  onRemove,
}: {
  block: PortalBlock;
  today: string;
  editLang: Language;
  languages: Language[];
  open: boolean;
  faulted: DesignFault | null;
  first: boolean;
  last: boolean;
  onToggle: () => void;
  onChange: (b: PortalBlock) => void;
  onMove: (dir: -1 | 1) => void;
  onRemove: () => void;
}) {
  const id = useId();
  const scheduleState =
    block.startsOn && block.startsOn > today ? "notYet" : block.endsOn && block.endsOn < today ? "ended" : null;
  const live = isLive(block, today);
  const summary = summaryOf(block, editLang, languages);

  return (
    <li className="block-card" data-block={block.id} data-live={live || undefined} data-open={open || undefined}>
      <div className="block-head">
        <KindIcon kind={block.kind} />
        <button
          type="button"
          className="block-summary"
          aria-expanded={open}
          aria-controls={open ? `${id}-body` : undefined}
          onClick={onToggle}
        >
          <span className="block-kind">{KIND_NAME[block.kind]}</span>
          <span className="block-line">{summary}</span>
        </button>
        <div className="block-flags">
          {!block.visible ? <Pill level="neutral" label={t("portal.block.hidden")} /> : null}
          {scheduleState ? (
            <Pill
              level="caution-minor"
              label={t(scheduleState === "notYet" ? "portal.block.notYet" : "portal.block.ended")}
            />
          ) : block.startsOn || block.endsOn ? (
            <Pill level="info" label={t("portal.block.scheduled", block.startsOn ?? "…", block.endsOn ?? "…")} />
          ) : null}
          {faulted ? <Pill level="urgent" label={faultCopy(faulted.fault)} /> : null}
        </div>
        <div className="block-actions">
          <label className="block-visible">
            <input
              type="checkbox"
              checked={block.visible}
              aria-label={`${t("portal.block.visible")} · ${summary}`}
              onChange={(e) => onChange({ ...block, visible: e.target.checked })}
            />
            <span aria-hidden="true">{t("portal.block.visible")}</span>
          </label>
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            aria-disabled={first || undefined}
            aria-label={`${t("portal.block.up")} · ${summary}`}
            onClick={() => !first && onMove(-1)}
          >
            <span aria-hidden="true">↑</span>
          </button>
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            aria-disabled={last || undefined}
            aria-label={`${t("portal.block.down")} · ${summary}`}
            onClick={() => !last && onMove(1)}
          >
            <span aria-hidden="true">↓</span>
          </button>
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            aria-label={`${t("portal.block.remove")} · ${summary}`}
            onClick={onRemove}
          >
            {t("portal.block.remove")}
          </button>
        </div>
      </div>

      {open ? (
        <div className="block-editor" id={`${id}-body`}>
          <BlockFields block={block} editLang={editLang} languages={languages} faulted={faulted} onChange={onChange} />
          <details
            className="block-schedule"
            open={Boolean(block.startsOn || block.endsOn) || faulted?.fault === "ends-before-start"}
          >
            <summary>{t("portal.schedule")}</summary>
            <p className="field-hint">{t("portal.schedule.hint")}</p>
            <div className="schedule-row">
              <div className="field">
                <label htmlFor={`${id}-from`}>{t("portal.schedule.from")}</label>
                <input
                  id={`${id}-from`}
                  type="date"
                  value={block.startsOn ?? ""}
                  onChange={(e) => onChange({ ...block, startsOn: e.target.value || null })}
                />
              </div>
              <div className="field">
                <label htmlFor={`${id}-to`}>{t("portal.schedule.to")}</label>
                <input
                  id={`${id}-to`}
                  type="date"
                  value={block.endsOn ?? ""}
                  aria-invalid={Boolean(block.startsOn && block.endsOn && block.endsOn < block.startsOn) || undefined}
                  onChange={(e) => onChange({ ...block, endsOn: e.target.value || null })}
                />
              </div>
            </div>
            {block.startsOn && block.endsOn && block.endsOn < block.startsOn ? (
              <p className="field-error" role="alert">
                {t("portal.schedule.invalid")}
              </p>
            ) : null}
          </details>
          <div>
            <button type="button" className="btn btn-sm" onClick={onToggle}>
              {t("portal.block.done")}
            </button>
          </div>
        </div>
      ) : null}
    </li>
  );
}

function BlockFields({
  block,
  editLang,
  languages,
  faulted,
  onChange,
}: {
  block: PortalBlock;
  editLang: Language;
  languages: Language[];
  faulted: DesignFault | null;
  onChange: (b: PortalBlock) => void;
}) {
  const id = useId();
  const text: PortalBlockText = block.content[editLang] ?? {};
  const other = languages.find((l) => l !== editLang && hasText(block.content[l]));
  const setText = (next: PortalBlockText) => onChange({ ...block, content: { ...block.content, [editLang]: next } });
  const items = text.items ?? [];
  const links = block.links ?? [];

  return (
    <div className="block-fields">
      {/* The fallback rule, said as the consequence: what a guest reading this
          language will see, and a one-tap way to start a translation from it. */}
      {block.kind !== "social" && !hasText(block.content[editLang]) && other ? (
        <div className="block-fallback">
          <p className="field-hint">{t("portal.block.otherLanguage", LANGUAGE_NAME[other])}</p>
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => setText(structuredClone(block.content[other] ?? {}))}
          >
            {t("portal.block.copyFrom", LANGUAGE_NAME[other])}
          </button>
        </div>
      ) : null}

      {block.kind === "notice" ? (
        <TextInput
          id={`${id}-title`}
          label={t("portal.field.notice")}
          max={80}
          value={text.title}
          onChange={(v) => setText({ ...text, title: v })}
        />
      ) : block.kind === "button" ? null : (
        <TextInput
          id={`${id}-title`}
          label={t(block.kind === "social" ? "portal.field.optionalHeading" : "portal.field.title")}
          max={80}
          value={text.title}
          onChange={(v) => setText({ ...text, title: v })}
        />
      )}

      {block.kind === "text" || block.kind === "offer" || block.kind === "notice" ? (
        <div className="field">
          <label htmlFor={`${id}-body`}>{t(block.kind === "text" ? "portal.field.body" : "portal.field.details")}</label>
          <textarea
            id={`${id}-body`}
            rows={block.kind === "notice" ? 2 : 3}
            maxLength={500}
            value={text.body ?? ""}
            onChange={(e) => setText({ ...text, body: e.target.value })}
          />
        </div>
      ) : null}

      {block.kind === "offer" ? (
        <TextInput
          id={`${id}-code`}
          label={t("portal.field.code")}
          hint={t("portal.field.codeHint")}
          max={24}
          value={block.code ?? ""}
          mono
          onChange={(v) => onChange({ ...block, code: v.toUpperCase() || null })}
        />
      ) : null}

      {block.kind === "list" ? (
        <fieldset className="field rows-field">
          <legend>{t("portal.field.items")}</legend>
          {items.map((item, n) => (
            <div className="row-edit" key={n}>
              <input
                aria-label={`${t("portal.field.itemLabel")} ${n + 1}`}
                placeholder={t("portal.field.itemLabel")}
                maxLength={60}
                value={item.label}
                onChange={(e) =>
                  setText({ ...text, items: items.map((x, k) => (k === n ? { ...x, label: e.target.value } : x)) })
                }
              />
              <input
                className="row-value"
                aria-label={`${t("portal.field.itemValue")} ${n + 1}`}
                placeholder={t("portal.field.itemValue")}
                maxLength={30}
                value={item.value ?? ""}
                onChange={(e) =>
                  setText({ ...text, items: items.map((x, k) => (k === n ? { ...x, value: e.target.value } : x)) })
                }
              />
              <button
                type="button"
                className="btn btn-sm btn-ghost"
                aria-label={`${t("portal.field.removeItem")} ${n + 1}`}
                onClick={() => setText({ ...text, items: items.filter((_, k) => k !== n) })}
              >
                <span aria-hidden="true">×</span>
              </button>
            </div>
          ))}
          {items.length < 12 ? (
            <div>
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => setText({ ...text, items: [...items, { label: "", value: "" }] })}
              >
                <Plus />
                {t("portal.field.addItem")}
              </button>
            </div>
          ) : null}
        </fieldset>
      ) : null}

      {block.kind === "button" ? (
        <>
          <TextInput
            id={`${id}-label`}
            label={t("portal.field.label")}
            max={40}
            value={text.label}
            onChange={(v) => setText({ ...text, label: v })}
          />
          <LinkInput
            id={`${id}-url`}
            label={t("portal.field.url")}
            value={block.url ?? ""}
            required={block.visible}
            forced={faulted?.fault === "required" || faulted?.fault === "not-a-link"}
            onChange={(v) => onChange({ ...block, url: v })}
          />
        </>
      ) : null}

      {block.kind === "social" ? (
        <fieldset className="field rows-field">
          <legend>{t("portal.kind.social")}</legend>
          {links.map((link, n) => (
            <div className="row-edit" key={n}>
              <select
                aria-label={`${t("portal.field.network")} ${n + 1}`}
                value={link.network}
                onChange={(e) =>
                  onChange({
                    ...block,
                    links: links.map((x, k) => (k === n ? { ...x, network: e.target.value as PortalSocialNetwork } : x)),
                  })
                }
              >
                {NETWORKS.map((net) => (
                  <option key={net} value={net}>
                    {SOCIAL_NAME[net]}
                  </option>
                ))}
              </select>
              <LinkInput
                id={`${id}-link-${n}`}
                label={`${t("portal.field.url")} · ${SOCIAL_NAME[link.network]}`}
                hideLabel
                value={link.url}
                required={block.visible}
                forced={faulted?.fault === "not-a-link"}
                onChange={(v) => onChange({ ...block, links: links.map((x, k) => (k === n ? { ...x, url: v } : x)) })}
              />
              <button
                type="button"
                className="btn btn-sm btn-ghost"
                aria-label={`${t("portal.field.removeLink")} · ${SOCIAL_NAME[link.network]}`}
                onClick={() => onChange({ ...block, links: links.filter((_, k) => k !== n) })}
              >
                <span aria-hidden="true">×</span>
              </button>
            </div>
          ))}
          {links.length < 8 ? (
            <div>
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => onChange({ ...block, links: [...links, { network: "facebook", url: "" }] })}
              >
                <Plus />
                {t("portal.field.addLink")}
              </button>
            </div>
          ) : null}
        </fieldset>
      ) : null}
    </div>
  );
}

function TextInput({
  id,
  label,
  hint,
  max,
  value,
  mono,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  max: number;
  value: string | undefined;
  mono?: boolean;
  onChange: (v: string) => void;
}) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <input
        id={id}
        value={value ?? ""}
        maxLength={max}
        autoComplete="off"
        className={mono ? "mono" : undefined}
        aria-describedby={hint ? `${id}-hint` : undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      {hint ? (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      ) : null}
    </div>
  );
}

/**
 * A link, checked as it is typed against the rule the server applies.
 *
 * Owners type `facebook.com/quan` far more often than `https://…`, so a bare host
 * gets `https://` put in front when the field loses focus — the one repair that
 * cannot change what they meant. Anything else that is not a link says so under the
 * field, rather than at Save.
 */
function LinkInput({
  id,
  label,
  hideLabel,
  value,
  required,
  forced,
  onChange,
}: {
  id: string;
  label: string;
  hideLabel?: boolean;
  value: string;
  required: boolean;
  forced: boolean;
  onChange: (v: string) => void;
}) {
  const [touched, setTouched] = useState(false);
  const blank = !value.trim();
  const bad = !blank && !isLink(value.trim());
  const missing = blank && required;
  const show = (touched || forced) && (bad || missing);

  return (
    <div className={hideLabel ? "field field-inline" : "field"}>
      <label htmlFor={id} className={hideLabel ? "visually-hidden" : undefined}>
        {label}
      </label>
      <input
        id={id}
        type="url"
        inputMode="url"
        value={value}
        maxLength={500}
        placeholder="https://"
        autoComplete="off"
        aria-invalid={show || undefined}
        aria-describedby={show ? `${id}-error` : hideLabel ? undefined : `${id}-hint`}
        onChange={(e) => onChange(e.target.value)}
        onBlur={() => {
          setTouched(true);
          const v = value.trim();
          if (v && !/^[a-z]+:/i.test(v) && /^[^\s/]+\.[^\s]+$/.test(v)) onChange(`https://${v}`);
          else if (v !== value) onChange(v);
        }}
      />
      {show ? (
        <p className="field-error" role="alert" id={`${id}-error`}>
          {missing ? t("portal.field.urlRequired") : t("portal.field.urlInvalid")}
        </p>
      ) : hideLabel ? null : (
        <p className="field-hint" id={`${id}-hint`}>
          {t("portal.field.urlHint")}
        </p>
      )}
    </div>
  );
}
