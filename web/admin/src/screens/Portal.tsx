import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import type {
  Language,
  PortalBlockPlacement,
  PortalContext,
  PortalDesign,
  PortalMethod,
  PortalSettings,
} from "@foyer/contract";
import { dataUriBytes, designFault, EMPTY_DESIGN, type DesignFault } from "@foyer/contract";
import { translator } from "@foyer/portal/src/i18n/index.js";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { useCan } from "../auth.js";
import { budgetBytes } from "../format.js";
import { t } from "../i18n/portal.js";
import { errorCopy, fieldMessages } from "../problems.js";
import { useSite } from "../site.js";
import { Alert, Info } from "../ui/icons.js";
import { ErrorNotice, Notice, Pill, type Toast, Toasts } from "../ui/primitives.js";
import { LivePreview } from "./portal/LivePreview.js";
/* Static, inside this screen's own lazy chunk. A second `lazy()` edge here made
   Rollup hoist `primitives` and `useMutation` out of the entry into separate chunks
   that are still fetched first — measured at +2.9 kB of entry gzip, the
   hazard `App.tsx` records for the eager screens. */
import { PhoneSignInPanel } from "./portal/PhoneSignIn.js";
import { TimeZonePanel, useSiteToday } from "./portal/TimeZone.js";
import { GuestWifiKey } from "../print/GuestWifiKey.js";
import { BlockList, LANGUAGE_NAME, LookSection, TemplateGallery } from "./portal/PageBuilder.js";
import "./portal/editor.css";

/**
 * The portal an operator can actually change.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **This screen exists because there was no producer for anything a guest sees.**
 * Venue name, logo, terms, languages and sign-in methods were all read-only in the
 * contract and supplied by a fixture — twenty-five operations and not one of them
 * configured the portal. `logoDataUri` had no producer *and* no consumer: null in
 * every fixture, settable by nobody, rendered by nothing.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Three things here are unusual, and each is the point rather than decoration.**
 *
 * *The weight is on screen while you edit it.* Every other product hides page weight
 * from the person creating it. Here the guest pays it on **every single launch** —
 * Android calls `clearCache(true)` each time the sign-in page opens and the iOS
 * window keeps nothing — usually on the worst connection of their day. A budget the
 * operator cannot see is a budget CI enforces on somebody else's behalf, hours later,
 * as a red build with no obvious cause.
 *
 * *The terms editor predicts what a guest will read.* The resolution order is real
 * and non-obvious: a reader whose language has no text gets the venue's other
 * language, marked as that language, and only then Foyer's own wording. Stating the
 * rule would be documentation; stating the *consequence per language* is something
 * an operator can check against what they meant.
 *
 * *There is no translate button.* Terms are legal text, and a machine translation of
 * a legal text is a different legal text. The absence is explained on the screen,
 * because an unexplained absence reads as a missing feature and gets added later by
 * somebody who did not know.
 */

const LANGUAGES: readonly Language[] = ["en", "vi"];
const METHODS: readonly PortalMethod[] = ["voucher", "account", "click-through", "phone"];

/*
 * One formatter for this screen's ceilings **and for the refusal about them**.
 *
 * This was a local `kb` dividing by 1024 while `problems.ts` rendered every server
 * refusal through `format.ts`'s decimal `bytes`, and both said "kB" — so the same
 * 102 400 B ceiling was named **100 kB** by the client's own gate and **102 kB** by
 * the server's, one press apart, for one rule. The reason the base
 * is 1024 and the label is still `kB` is written at `budgetBytes` itself.
 */
const kb = budgetBytes;

/** What a draft looks like before it is saved. Terms are edited per language. */
interface Draft {
  venueName: string;
  networkName: string;
  logoDataUri: string | null;
  languages: Language[];
  methods: PortalMethod[];
  clickThroughMinutes: string;
  terms: Record<string, string>;
  design: PortalDesign;
}

const toDraft = (s: PortalSettings): Draft => ({
  venueName: s.venueName,
  networkName: s.networkName ?? "",
  logoDataUri: s.logoDataUri ?? null,
  languages: [...s.languages],
  methods: [...s.methods],
  clickThroughMinutes: s.clickThroughMinutes == null ? "" : String(s.clickThroughMinutes),
  terms: { ...(s.terms.summary ?? {}) },
  design: structuredClone(s.design ?? EMPTY_DESIGN),
});

type Tab = "page" | "signin" | "terms";
const TABS: readonly Tab[] = ["page", "signin", "terms"];
const TAB_NAME: Record<Tab, string> = {
  get page() {
    return t("portal.tab.page");
  },
  get signin() {
    return t("portal.tab.signin");
  },
  get terms() {
    return t("portal.tab.terms");
  },
};

/*
 * The portal's own default heading, in each language, as the placeholder for the
 * venue's. Read from the portal's catalogue rather than restated: the placeholder
 * is a promise about what a guest sees when the field is left blank.
 */
const DEFAULT_HEADLINE: Record<Language, string> = {
  en: translator("en")("welcome.title"),
  vi: translator("vi")("welcome.title"),
};

export function PortalScreen() {
  const { siteId } = useSite();
  const qc = useQueryClient();
  const canManage = useCan("portal:manage");
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [params, setParams] = useSearchParams();
  const tab: Tab = TABS.includes(params.get("tab") as Tab) ? (params.get("tab") as Tab) : "page";
  const [place, setPlace] = useState<PortalBlockPlacement>("welcome");
  const [editLang, setEditLang] = useState<Language | null>(null);
  const [previewLang, setPreviewLang] = useState<Language | null>(null);
  const [localFault, setLocalFault] = useState<DesignFault | null>(null);
  /* A block's dates are the venue's days, not the browser's. */
  const today = useSiteToday(siteId);

  const query = useQuery({
    queryKey: ["portal-settings", siteId],
    queryFn: () => api.getPortalSettings(siteId),
  });

  /* The draft is seeded from the server and re-seeded when the site changes. Keyed
     on `siteId` and on the payload's own `siteId`, not on the payload: react-query
     hands back a new object for the same site whenever it refetches — the save's own
     invalidation, a refocus, a second console — and reseeding on that is what throws
     away a venue name or a hero photograph somebody is halfway through. `siteId`
     is the screen moving between sites; `settings?.siteId` is a different site's
     settings arriving. Both are read at run time from the render that scheduled the
     effect, so this is a narrow list rather than a stale one.

     What would change it: a field the server may change underneath the operator and
     that they must see land. Nothing in `PortalSettings` is one — the read carries
     the bytes and URLs the console itself uploaded, and `onSuccess` seeds from the
     save's own response. */
  const settings = query.data;
  useEffect(() => {
    if (settings) setDraft(toDraft(settings));
  }, [siteId, settings?.siteId]);

  const save = useMutation({
    mutationFn: (d: Draft) =>
      api.updatePortalSettings(siteId, {
        venueName: d.venueName,
        networkName: d.networkName.trim() ? d.networkName : null,
        logoDataUri: d.logoDataUri,
        languages: d.languages,
        methods: d.methods,
        clickThroughMinutes: d.clickThroughMinutes.trim()
          ? Number(d.clickThroughMinutes)
          : null,
        terms: { summary: d.terms },
        design: d.design,
      }),
    onSuccess: (next) => {
      setDraft(toDraft(next));
      void qc.invalidateQueries({ queryKey: ["portal-settings"] });
      setToasts((c) => [...c, { id: Date.now(), message: t("portal.saved") }]);
    },
  });

  const dirty = Boolean(settings && draft && JSON.stringify(draft) !== JSON.stringify(toDraft(settings)));

  /*
   * An owner who closes the tab after an hour of writing their menu loses it
   * silently otherwise. The browser's own prompt, and only while there is
   * something to lose.
   */
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  /*
   * A failed load and a slow load are not the same screen, and this had only one.
   *
   * `if (!settings || !draft)` is true forever when the fetch failed, so the screen
   * said *Loading…* and went on saying it — the one outcome an operator cannot act
   * on and cannot tell from a slow network.
   */
  if (query.isError) {
    return (
      <div className="page">
        <PageHead title={t("portal.title")} description={t("portal.description")} />
        <ErrorNotice error={query.error} />
      </div>
    );
  }

  if (!settings || !draft) {
    return (
      <div className="page">
        <PageHead title={t("portal.title")} description={t("portal.description")} />
        <p className="muted">{t("common.loading")}</p>
      </div>
    );
  }

  const set = <K extends keyof Draft>(key: K, value: Draft[K]) =>
    setDraft((d) => (d ? { ...d, [key]: value } : d));
  const setDesign = (design: PortalDesign) => {
    setLocalFault(null);
    set("design", design);
  };

  const budgets = settings.budgets ?? {
    logoMaxBytes: 512000,
    imageMaxBytes: 5242880,
    codeBudgetBytes: 51200,
  };
  const imageBytes = settings.heroImage?.bytes ?? 0;
  const fieldErrors = fieldMessages(save.error);
  /* In the venue's own order, so a Vietnamese café writes in Vietnamese first. */
  const languages = draft.languages.filter((l) => LANGUAGES.includes(l));
  const writing: Language = editLang && languages.includes(editLang) ? editLang : (languages[0] ?? "en");

  /* The server's refusal of the design, pinned to the block it names when it names
     one; the console's own check runs first and normally gets there before it. */
  const serverFault = designFaultFrom(save.error, draft.design);
  const fault = localFault ?? serverFault;

  const context: PortalContext = {
    venueName: draft.venueName.trim() || "Foyer",
    networkName: draft.networkName.trim() || null,
    logoDataUri: draft.logoDataUri,
    heroImageUrl: settings.heroImage?.url ?? null,
    methods: draft.methods.length ? draft.methods : ["voucher"],
    languages: draft.languages.length ? draft.languages : ["en"],
    terms: { summary: draft.terms },
    design: draft.design,
  };

  const pressSave = () => {
    if (!dirty || save.isPending) return;
    const found = designFault(draft.design);
    if (found) {
      setLocalFault(found);
      setParams((p) => {
        p.set("tab", "page");
        return p;
      }, { replace: true });
      return;
    }
    save.mutate(draft);
  };

  return (
    <div className="page">
      <PageHead
        title={t("portal.title")}
        description={t("portal.description")}
        actions={
          /* Every write on this screen — the settings, the hero image — declares
             `portal:manage`. Reading them is `portal:read`, which staff have. */
          !canManage ? null : (
          <>
            {dirty ? (
              <button
                type="button"
                className="btn"
                onClick={() => {
                  setLocalFault(null);
                  setDraft(toDraft(settings));
                }}
              >
                {t("portal.discard")}
              </button>
            ) : null}
            {/*
              `disabled` only while it has never been pressed, `aria-disabled` after:
              `disabled` on a focused element blurs it to `<body>`, and the operator
              is standing on this button at the moment the save lands.
            */}
            <button
              type="button"
              className="btn btn-primary"
              disabled={!dirty && save.isIdle}
              aria-disabled={!dirty || save.isPending || undefined}
              onClick={pressSave}
            >
              {t("portal.save")}
            </button>
          </>
          )
        }
      />

      {save.error ? (
        <div className="stack-sm">
          <ErrorNotice error={save.error} title={t("portal.saveFailed.title")} />
        </div>
      ) : localFault ? (
        <div className="stack-sm">
          <Notice level="urgent" title={t("portal.saveFailed.title")}>
            {t("portal.design.fixFirst")}
          </Notice>
        </div>
      ) : null}

      {!canManage ? (
        <div style={{ marginBottom: "1rem" }}>
          <Notice level="info" title={t("portal.readOnly.title")}>
            {t("portal.readOnly.body")}
          </Notice>
        </div>
      ) : null}

      <Tabs
        current={tab}
        onChange={(next) =>
          setParams((p) => {
            p.set("tab", next);
            return p;
          }, { replace: true })
        }
      />

      {/*
        A `<fieldset disabled>` rather than a hidden form: for a staff session the
        values stay legible inside the controls that display them, and every control
        goes inert in one attribute the platform enforces.
      */}
      <fieldset className="portal-fieldset" disabled={!canManage}>
      <div className="portal-grid">
        <div className="portal-forms" role="tabpanel" id={`portal-panel-${tab}`} aria-labelledby={`portal-tab-${tab}`}>
          {tab === "page" ? (
            <>
              <TemplateGallery
                venueName={draft.venueName}
                design={draft.design}
                onApply={(d) => {
                  setDesign(d);
                  setPlace("welcome");
                }}
              />

              {languages.length > 1 ? (
                <div className="writing-in">
                  <span className="writing-in-label" id="writing-in-label">
                    {t("portal.writingIn")}
                  </span>
                  <div className="segmented" role="group" aria-labelledby="writing-in-label">
                    {languages.map((l) => (
                      <button
                        key={l}
                        type="button"
                        className="segment"
                        aria-pressed={writing === l}
                        onClick={() => {
                          // The preview follows: what is being written is what to look at.
                          setEditLang(l);
                          setPreviewLang(l);
                        }}
                      >
                        {LANGUAGE_NAME[l]}
                      </button>
                    ))}
                  </div>
                  <p className="field-hint">{t("portal.writingIn.hint")}</p>
                </div>
              ) : null}

              <Section title={t("portal.section.identity")}>
                <Field
                  id="venue-name"
                  label={t("portal.venueName")}
                  hint={t("portal.venueNameHint")}
                  error={fieldErrors.venueName}
                  value={draft.venueName}
                  onChange={(v) => set("venueName", v)}
                />
                <LogoField
                  value={draft.logoDataUri}
                  max={budgets.logoMaxBytes}
                  onChange={(v) => set("logoDataUri", v)}
                />
              </Section>

              <LookSection
                design={draft.design}
                onChange={setDesign}
                editLang={writing}
                defaultHeadline={DEFAULT_HEADLINE[writing]}
              />

              <BlockList
                design={draft.design}
                onChange={setDesign}
                place={place}
                onPlace={setPlace}
                editLang={writing}
                languages={languages}
                fault={fault}
                today={today}
              />

              <Section title={t("portal.section.image")}>
                <HeroField
                  siteId={siteId}
                  settings={settings}
                  max={budgets.imageMaxBytes}
                  onChanged={() => {
                    void qc.invalidateQueries({ queryKey: ["portal-settings"] });
                  }}
                />
              </Section>
            </>
          ) : null}

          {tab === "signin" ? (
            <>
              <Section title={t("portal.section.methods")} hint={t("portal.methods.hint")}>
                <div className="chip-group">
                  {METHODS.map((m) => (
                    <button
                      key={m}
                      type="button"
                      className="chip"
                      aria-pressed={draft.methods.includes(m)}
                      onClick={() =>
                        set(
                          "methods",
                          draft.methods.includes(m)
                            ? draft.methods.filter((x) => x !== m)
                            : [...draft.methods, m],
                        )
                      }
                    >
                      {t(
                        m === "voucher"
                          ? "portal.methods.voucher"
                          : m === "account"
                            ? "portal.methods.account"
                            : m === "phone"
                              ? "portal.methods.phone"
                              : "portal.methods.click-through",
                      )}
                    </button>
                  ))}
                </div>
                {draft.methods.length === 0 ? (
                  <p className="field-error" role="alert">
                    {t("portal.methods.required")}
                  </p>
                ) : null}
                {fieldErrors.methods ? (
                  <p className="field-error" role="alert">
                    {fieldErrors.methods}
                  </p>
                ) : null}

                {/* The price of the combination, stated where it is chosen. An operator
                    cannot measure the CNA window and will not find out otherwise. */}
                {draft.methods.length >= 3 ? (
                  <Notice level="caution-minor" title={t("portal.methods.tooManyForCna.title")}>
                    {t("portal.methods.tooManyForCna.body")}
                  </Notice>
                ) : null}

                {/*
                  The one setting terms-only access cannot be offered without. A mode
                  with no credential *and* no duration is public internet handed out on
                  one tap, which a venue should decide rather than inherit — so the field
                  appears with the method and the server refuses the pair without it.
                */}
                {draft.methods.includes("click-through") ? (
                  <>
                    <p className="field-hint">{t("portal.methods.clickThroughHint")}</p>
                    <Field
                      id="click-through-minutes"
                      label={t("portal.clickThroughMinutes")}
                      hint={t("portal.clickThroughMinutesHint")}
                      error={
                        draft.clickThroughMinutes.trim()
                          ? fieldErrors.clickThroughMinutes
                          : t("portal.clickThroughMinutesRequired")
                      }
                      value={draft.clickThroughMinutes}
                      onChange={(v) => set("clickThroughMinutes", v)}
                    />
                  </>
                ) : null}
              </Section>

              <Section title={t("portal.section.phone")}>
                <PhoneSignInPanel siteId={siteId} canManage={canManage} />
              </Section>

              <Section title={t("portal.section.network")}>
                <Field
                  id="network-name"
                  label={t("portal.networkName")}
                  hint={t("portal.networkNameHint")}
                  value={draft.networkName}
                  onChange={(v) => set("networkName", v)}
                />
                <GuestWifiKey siteId={siteId} canManage={canManage} />
              </Section>

              <TimeZonePanel siteId={siteId} canManage={canManage} />

              <Section title={t("portal.section.languages")} hint={t("portal.languages.hint")}>
                <div className="chip-group">
                  {LANGUAGES.map((l) => (
                    <button
                      key={l}
                      type="button"
                      className="chip"
                      aria-pressed={draft.languages.includes(l)}
                      onClick={() =>
                        set(
                          "languages",
                          draft.languages.includes(l)
                            ? draft.languages.filter((x) => x !== l)
                            : [...draft.languages, l],
                        )
                      }
                    >
                      {LANGUAGE_NAME[l]}
                    </button>
                  ))}
                </div>
                {draft.languages.length === 0 ? (
                  <p className="field-error" role="alert">
                    {t("portal.languages.required")}
                  </p>
                ) : null}
              </Section>
            </>
          ) : null}

          {tab === "terms" ? <TermsSection draft={draft} onChange={(terms) => set("terms", terms)} /> : null}
        </div>

        <aside className="portal-aside">
          <LivePreview
            context={context}
            languages={languages}
            lang={previewLang && languages.includes(previewLang) ? previewLang : writing}
            onLang={setPreviewLang}
            page={place}
            onPage={setPlace}
            base={settings.portalUrl ?? null}
            liveUrl={settings.portalUrl ? `${settings.portalUrl}/${languages[0] ?? "en"}/` : null}
            budget={budgets.codeBudgetBytes}
            dirty={dirty}
            today={today}
          />
          <Weight imageUsed={imageBytes} imageBudget={budgets.imageMaxBytes} />
        </aside>
      </div>
      </fieldset>

      <Toasts toasts={toasts} onDismiss={(id) => setToasts((c) => c.filter((x) => x.id !== id))} />
    </div>
  );
}

/**
 * Three tabs for three different jobs: the page a guest reads, how they get on,
 * and the legal text. The owner doing the first rarely needs the other two, and
 * the long single form this replaced put the photograph and the terms between them
 * and their menu. The tab is in the URL, so a link can open the one that matters.
 */
function Tabs({ current, onChange }: { current: Tab; onChange: (t: Tab) => void }) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({});
  return (
    <div className="portal-tabs" role="tablist" aria-label={t("portal.tabs.label")}>
      {TABS.map((tab, i) => (
        <button
          key={tab}
          ref={(el) => {
            refs.current[tab] = el;
          }}
          type="button"
          role="tab"
          id={`portal-tab-${tab}`}
          aria-selected={current === tab}
          aria-controls={`portal-panel-${tab}`}
          tabIndex={current === tab ? 0 : -1}
          className="portal-tab"
          onClick={() => onChange(tab)}
          onKeyDown={(e) => {
            const dir = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
            if (!dir) return;
            e.preventDefault();
            const next = TABS[(i + dir + TABS.length) % TABS.length]!;
            onChange(next);
            refs.current[next]?.focus();
          }}
        >
          {TAB_NAME[tab]}
        </button>
      ))}
    </div>
  );
}

/**
 * The block a server refusal of `design` is about, if it said. The mock and the Go
 * server both answer `design` with a sentence that begins "Block N:"; the console's
 * own check normally refuses first, so this is the path for a server that knows a
 * rule this client does not.
 */
function designFaultFrom(error: unknown, design: PortalDesign): DesignFault | null {
  const faults = (error as { problem?: { params?: { fields?: Record<string, string> }; errors?: Record<string, string> } })
    ?.problem;
  const fault = faults?.params?.fields?.design as DesignFault["fault"] | undefined;
  if (!fault) return null;
  const n = /Block (\d+)/.exec(faults?.errors?.design ?? "")?.[1];
  const block = n ? Number(n) - 1 : 0;
  return block < design.blocks.length ? { fault, block } : null;
}

function Section({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="card portal-section">
      <h2 className="section-title">{title}</h2>
      {hint ? <p className="section-lede">{hint}</p> : null}
      {children}
    </section>
  );
}

function Field({
  id,
  label,
  hint,
  error,
  value,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <input
        id={id}
        value={value}
        autoComplete="off"
        aria-invalid={Boolean(error) || undefined}
        /* Both halves or neither. `aria-invalid` alone announces *invalid* and names
           nothing, which is the state all three of the console's marked inputs were
           in — a claim with no referent. */
        aria-describedby={error ? `${id}-error` : hint ? `${id}-hint` : undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      {error ? (
        <p className="field-error" role="alert" id={`${id}-error`}>
          {error}
        </p>
      ) : hint ? (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      ) : null}
    </div>
  );
}

/**
 * The logo, two ways in.
 *
 * Upload inlines the file as a data URI — one fewer round trip on the worst link
 * of the guest's day, paid inside every document. A link keeps the document small
 * and loads the file as a second request against the portal's own address. Either
 * way the preview under the field shows what the guest gets, because a mode whose
 * result the operator cannot see is a mode nobody should pick.
 *
 * Rejected client-side *and* server-side on the upload path. The client check is
 * not for safety — it is so an operator learns the file is too big before a round
 * trip, at the moment they chose it, when the fix (export it smaller) is still in
 * their hands. A link is never refused: the server accepts any same-origin value,
 * so the field says when the file behind it is over the budget instead.
 */
function LogoField({
  value,
  max,
  onChange,
}: {
  value: string | null;
  max: number;
  onChange: (v: string | null) => void;
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  /* Where focus lands when *Remove* removes itself. See the button below. */
  const choose = useRef<HTMLButtonElement>(null);
  const [error, setError] = useState<string | null>(null);
  const [url, setUrl] = useState<string | null>(() =>
    value && !value.startsWith("data:") ? value : null,
  );
  const [urlState, setUrlState] = useState<
    { status: "idle" } | { status: "loading" } | { status: "loaded"; bytes: number } | { status: "failed" }
  >({ status: "idle" });
  const bytes = value ? (dataUriBytes(value) ?? 0) : 0;
  const byUrl = url !== null;

  /* The file behind a link, weighed where the guest stands — not from the admin's
     browser, which may not reach the portal's address at all. */
  useEffect(() => {
    if (url === null || url.trim() === "") {
      setUrlState({ status: "idle" });
      return;
    }
    let live = true;
    setUrlState({ status: "loading" });
    fetch(url.trim(), { method: "HEAD" })
      .then((res) => {
        if (!live) return;
        const length = Number(res.headers.get("content-length") ?? NaN);
        if (!res.ok || !Number.isFinite(length)) setUrlState({ status: "failed" });
        else setUrlState({ status: "loaded", bytes: length });
      })
      .catch(() => {
        if (live) setUrlState({ status: "failed" });
      });
    return () => {
      live = false;
    };
  }, [url]);

  const read = (file: File) => {
    const reader = new FileReader();
    reader.onload = () => {
      const uri = String(reader.result);
      const size = dataUriBytes(uri) ?? 0;
      if (size > max) {
        setError(t("portal.image.tooLarge", kb(size), kb(max)));
        return;
      }
      setError(null);
      setUrl(null);
      onChange(uri);
    };
    reader.readAsDataURL(file);
  };

  const useUrl = (next: string) => {
    setUrl(next);
    setError(null);
    onChange(next.trim() ? next.trim() : null);
  };
  const clear = () => {
    setError(null);
    setUrl(null);
    choose.current?.focus();
    onChange(null);
  };

  return (
    <div className="field">
      <label htmlFor={byUrl ? `${id}-url` : id}>{t("portal.logo")}</label>
      <div className="asset-row">
        {value ? (
          <img className="asset-thumb" src={value} alt="" />
        ) : (
          <span className="asset-thumb asset-thumb-empty" aria-hidden="true" />
        )}
        <div className="asset-actions">
          <input
            ref={input}
            id={id}
            type="file"
            accept="image/png,image/jpeg,image/webp"
            className="visually-hidden"
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) read(file);
              e.target.value = "";
            }}
          />
          {!byUrl ? (
            <button
              ref={choose}
              type="button"
              className="btn btn-sm"
              onClick={() => input.current?.click()}
            >
              {t("portal.logoChoose")}
            </button>
          ) : null}
          {/*
            Removing the asset unmounts the button that removed it, and focus was on
            <body> afterwards — measured on both fields on this screen. It is the
            shape `screens/refocus.ts` answers for a list, and its rule (*the first
            action on the row that took the removed row's place*) has nothing to
            point at here: a settings form has no rows.

            **The rule for a form: the first remaining action in the group the
            removed control belonged to.** It is the same rule with the row read as
            the group — the operator has changed one field, not finished a task, so
            focus stays inside that field; and *first* rather than same-position for
            the reason `refocus.ts` gives, which here means the choose button rather
            than the size beside it. That button is also the only thing left to do to
            this field, and it is the same DOM node throughout: it merely stops
            saying "Replace" and starts saying "Choose".

            Focused before the state change rather than after it, which the list rule
            cannot do — there the landing target is created by the removal, here it
            already exists and outlives it.
          */}
          {value ? (
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              onClick={clear}
            >
              {t("portal.logoRemove")}
            </button>
          ) : null}
          {value && !byUrl ? <span className="muted">{kb(bytes)}</span> : null}
          {!byUrl ? (
            <button type="button" className="btn btn-sm btn-ghost" onClick={() => useUrl("")}>
              {t("portal.logoByUrl")}
            </button>
          ) : (
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              onClick={() => {
                setUrl(null);
                setError(null);
                onChange(null);
              }}
            >
              {t("portal.logoByUpload")}
            </button>
          )}
        </div>
      </div>
      {byUrl ? (
        <div className="field" style={{ marginTop: "0.5rem" }}>
          <label htmlFor={`${id}-url`}>{t("portal.logoUrl")}</label>
          <input
            id={`${id}-url`}
            value={url ?? ""}
            autoComplete="off"
            placeholder={t("portal.logoUrlPlaceholder")}
            onChange={(e) => useUrl(e.target.value)}
          />
          <p className="field-hint">{t("portal.logoUrlHint")}</p>
          {url && url.trim() !== "" ? (
            <div className="asset-row" style={{ marginTop: "0.5rem" }}>
              <img
                className="asset-thumb"
                src={url.trim()}
                alt=""
                onError={() => setUrlState({ status: "failed" })}
                onLoad={(e) => {
                  const img = e.currentTarget;
                  if (img.naturalWidth > 0)
                    setUrlState((s) => (s.status === "loaded" ? s : { status: "loaded", bytes: 0 }));
                }}
              />
              <div className="asset-actions">
                <span className="muted">{t("portal.logoUrlPreview")}</span>
                {urlState.status === "loaded" && urlState.bytes > 0 ? (
                  urlState.bytes > max ? (
                    <span className="field-error" role="alert">
                      {t("portal.logoUrlTooLarge", kb(urlState.bytes), kb(max))}
                    </span>
                  ) : (
                    <span className="muted">{kb(urlState.bytes)}</span>
                  )
                ) : urlState.status === "failed" ? (
                  <span className="field-error" role="alert">
                    {t("portal.logoUrlNotImage")}
                  </span>
                ) : urlState.status === "loading" ? (
                  <span className="muted">…</span>
                ) : null}
              </div>
            </div>
          ) : (
            <p className="field-hint">{t("portal.logoUrlEmpty")}</p>
          )}
        </div>
      ) : null}
      {error ? (
        <p className="field-error" role="alert">
          {error}
        </p>
      ) : (
        <p className="field-hint">{value ? t("portal.logoHint", kb(max)) : !byUrl ? t("portal.logoNone") : null}</p>
      )}
    </div>
  );
}

/**
 * The venue photograph. Uploaded immediately rather than held in the draft.
 *
 * Deliberately not part of Save. A 100 kB payload inside an unsaved form is a
 * payload that vanishes on a mis-click, and the byte cost only becomes real once the
 * server has accepted it — the number beside it has to be the true transfer size,
 * not an estimate of one.
 */
function HeroField({
  siteId,
  settings,
  max,
  onChanged,
}: {
  siteId: string;
  settings: PortalSettings;
  max: number;
  onChanged: () => void;
}) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  /* Where focus lands when *Remove* removes itself — `LogoField` states the rule. */
  const choose = useRef<HTMLButtonElement>(null);
  const [error, setError] = useState<string | null>(null);
  const image = settings.heroImage ?? null;

  const upload = useMutation({
    mutationFn: (file: File) =>
      new Promise<void>((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = async () => {
          const uri = String(reader.result);
          try {
            await api.putHeroImage(siteId, {
              filename: file.name,
              contentType: file.type as "image/jpeg" | "image/png" | "image/webp",
              dataBase64: uri.slice(uri.indexOf(",") + 1),
            });
            resolve();
          } catch (e) {
            reject(e as Error);
          }
        };
        reader.onerror = () => reject(new Error("read failed"));
        reader.readAsDataURL(file);
      }),
    onSuccess: () => {
      setError(null);
      onChanged();
    },
    onError: (e) => setError(errorCopy(e).body),
  });

  const remove = useMutation({
    mutationFn: () => api.deleteHeroImage(siteId),
    /* One of the two asset removals that reported nothing. The operator is standing
       on *Remove* for the whole request — see the focus note below — so the message
       appears where they already are. */
    onError: (e) => setError(errorCopy(e).body),
    /*
     * Focus moves before the refetch is asked for, not after it lands.
     *
     * `LogoField` carries the rule. What differs here is only that the removal is a
     * request: this runs when the server has accepted it and the button is still on
     * screen, so the operator is moved one control to the left and *then* the control
     * they were on disappears. The failure path is untouched and stays that way —
     * `aria-disabled` holds focus on *Remove* for the whole request, so a removal
     * that fails leaves the operator standing on the thing that failed.
     */
    onSuccess: () => {
      choose.current?.focus();
      onChanged();
    },
  });

  return (
    <div className="field">
      <div className="asset-row">
        {image ? (
          <img className="asset-hero" src={image.url} alt="" />
        ) : (
          /*
           * The empty state says what it is, inside itself.
           *
           * An `aria-hidden` dashed rectangle with nothing in it, with the words
           * "No photograph." four elements below in a `field-hint` — past the Choose
           * button and the byte figure — would mislead. A dashed box with no label
           * is the shape of a **drop target**, so the one thing the rectangle would
           * communicate is an interaction that does not exist here; and a caption
           * that far away cannot be read as its caption.
           *
           * No `aria-hidden`, then. The rectangle is the statement, so hiding it
           * from assistive technology would delete the statement — the hint line
           * carries no copy to fall back on.
           */
          <span
            className="asset-hero asset-thumb-empty"
            style={{ display: "grid", placeItems: "center", color: "var(--text-muted)" }}
          >
            {t("portal.image.none")}
          </span>
        )}
      </div>

      <div className="asset-actions">
        {/*
          The input is visually hidden and driven by the button beside it, which is
          the ordinary pattern — but hidden is not unlabelled. Without this the axe
          gate reports `label`, critical, and it was right to: a screen reader user
          reaching the input by any route other than the button gets "file upload"
          and no idea what for.
        */}
        <label className="visually-hidden" htmlFor={id}>
          {t("portal.image.choose")}
        </label>
        <input
          ref={input}
          id={id}
          type="file"
          accept="image/png,image/jpeg,image/webp"
          className="visually-hidden"
          onChange={(e) => {
            const file = e.target.files?.[0];
            if (file) {
              if (!["image/jpeg", "image/png", "image/webp"].includes(file.type)) {
                setError(t("portal.image.wrongType"));
              } else if (file.size > max) {
                /*
                 * Checked before reading the file, so a 4 MB photograph never becomes
                 * a 5.3 MB base64 string in memory to be rejected after.
                 *
                 * ── DECIDED: THE GATE STAYS ─────────────────────────────────────
                 *
                 * This gate compares against the **server's own** ceiling —
                 * `budgets.imageMaxBytes`, sent with the settings precisely so that
                 * two clients do not compute it twice — so it shadows the server's
                 * `/problems/too-large` for every upload an operator starts. Driven:
                 * a 143 360 B photograph produced **0 requests**. The refusal the
                 * contract has a type for cannot fire from the operator's own action.
                 *
                 * That is not a reason to remove either side. Prefer the narrower
                 * reading.
                 * The narrow claim is *the producer is shadowed*, not *the copy is
                 * dead* — and those have opposite repairs.
                 *
                 *   · The render site **exists and works**. Driven on this same field
                 *     with the request enlarged past the ceiling the console had
                 *     already cleared: "41 kB against a 102 kB limit. Nothing was
                 *     uploaded." — the catalogue's sentence, in `role="alert"`. Nobody
                 *     may delete `onError` here as unreachable; this paragraph is why.
                 *   · This gate is a **prediction** of a server-owned limit, never the
                 *     limit. It is right until the two disagree, and they disagree in
                 *     ordinary ways: an operator's tab outliving a deployment that
                 *     lowered the ceiling, or `settings.budgets` absent, where the
                 *     console falls back to numbers compiled into this bundle. The
                 *     server's refusal is what covers the disagreement, so the moment
                 *     it is reachable is the moment it matters most.
                 *   · Removing the gate to "make the type reachable" would buy a test
                 *     and sell the operator a round trip that carries every byte of a
                 *     4 MB photograph before saying no.
                 *
                 * One related defect is fixed too: this sentence and the refusal
                 * named the same ceiling as *100 kB* and *102 kB*, on two bases
                 * both labelled kB. Both go through `budgetBytes` now, and the
                 * reason the base is 1024 is written at the formatter.
                 */
                setError(t("portal.image.tooLarge", kb(file.size), kb(max)));
              } else {
                upload.mutate(file);
              }
            }
            e.target.value = "";
          }}
        />
        {/* `aria-disabled` rather than `disabled`: this is the button the operator
            pressed to open the file picker, so it has focus while the upload runs.
            Measured and written out at `Accounts.tsx`'s row action. */}
        <button
          ref={choose}
          type="button"
          className="btn btn-sm"
          aria-disabled={upload.isPending || undefined}
          onClick={() => {
            if (!upload.isPending) input.current?.click();
          }}
        >
          {image ? t("portal.image.replace") : t("portal.image.choose")}
        </button>
        {/* Same again — and this is the one where it matters most, because a removal
            that *fails* leaves the button on screen with nothing said. Focus staying on
            it is the only thing that keeps the operator where the failure is. The
            success path is the mutation's `onSuccess` above: the button unmounts with
            the photograph, so focus is moved out of it first. */}
        {image ? (
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            aria-disabled={remove.isPending || undefined}
            onClick={() => {
              if (!remove.isPending) remove.mutate();
            }}
          >
            {t("portal.image.remove")}
          </button>
        ) : null}
        {image ? <span className="muted">{kb(image.bytes)}</span> : null}
      </div>

      {error ? (
        <p className="field-error" role="alert">
          {error}
        </p>
      ) : (
        /* Unconditional now: the size limit is what an operator needs whether they
           are about to choose a photograph or about to replace one, and the absence
           itself is stated in the box above rather than here. */
        <p className="field-hint">{t("portal.image.hint", kb(max))}</p>
      )}

      {/* Both sentences are consequences an operator cannot discover by looking. */}
      <p className="field-hint">
        <Info size={12} /> {t("portal.image.whyLinked")}
      </p>
      {image ? (
        <p className="field-hint">
          <Alert size={12} /> {t("portal.image.hiddenOnShort")}
        </p>
      ) : null}
    </div>
  );
}

/**
 * The terms editor, and the part that earns its place is the prediction under each box.
 *
 * The resolution order is real and nobody can hold it in their head: the venue's text
 * for the reader's language, else the venue's text in *any* other language it
 * supplied and marked as that language, else Foyer's own wording. Rather than
 * documenting it, this says what each reader will actually get, recomputed as the
 * boxes are typed in.
 */
function TermsSection({ draft, onChange }: { draft: Draft; onChange: (t: Record<string, string>) => void }) {
  const supplied = draft.languages.filter((l) => (draft.terms[l] ?? "").trim());
  /* Same order the portal resolves in, so the prediction cannot drift from it. */
  const fallback = LANGUAGES.find((l) => (draft.terms[l] ?? "").trim());

  return (
    <Section title={t("portal.section.terms")} hint={t("portal.terms.hint")}>
      {draft.languages.map((lang) => {
        const own = (draft.terms[lang] ?? "").trim();
        return (
          <div className="field" key={lang}>
            <label htmlFor={`terms-${lang}`}>{t("portal.terms.for", LANGUAGE_NAME[lang])}</label>
            <textarea
              id={`terms-${lang}`}
              rows={4}
              value={draft.terms[lang] ?? ""}
              placeholder={t("portal.terms.placeholder")}
              onChange={(e) => onChange({ ...draft.terms, [lang]: e.target.value })}
            />
            {!own ? (
              <p className="field-hint">
                {fallback && fallback !== lang
                  ? t("portal.terms.fallback", LANGUAGE_NAME[lang], LANGUAGE_NAME[fallback])
                  : t("portal.terms.default", LANGUAGE_NAME[lang])}
              </p>
            ) : null}
          </div>
        );
      })}

      <Notice level="info" title={t("portal.terms.consent.title")}>
        {t("portal.terms.consent.body")}
      </Notice>
      <p className="field-hint">{t("portal.terms.noTranslate")}</p>
      {supplied.length === 0 ? null : null}
    </Section>
  );
}

/**
 * What this configuration costs a guest, while the operator is creating it.
 *
 * Two bars rather than one total: code and images fail for different reasons and are
 * fixed by different people, and one number would let each hide inside the other's
 * headroom. Same split as `tools/check-budget.mjs`, which is the gate that will
 * actually refuse the build — the two must agree, and they read the numbers from the
 * same place in the contract.
 */
function Weight({ imageUsed, imageBudget }: { imageUsed: number; imageBudget: number }) {
  /* The page's own weight is measured beside the preview, from the draft; this is
     the one figure the draft cannot know — the photograph, which is uploaded on its
     own and weighed separately. */
  return (
    <section className="card portal-section">
      <h2 className="section-title">{t("portal.section.weight")}</h2>
      <div className="weight-row">
        <Pill
          level={imageUsed > imageBudget ? "urgent" : "neutral"}
          label={t("portal.weight.image", kb(imageUsed), kb(imageBudget))}
        />
      </div>
      <p className="section-lede">{t("portal.weight.why")}</p>
    </section>
  );
}

/**
 * The live portal, at the two sizes that decide whether it works.
 *
 * 900 × 512 is measured: the macOS Captive Network Assistant window minus its
 * chrome, and it **does not grow**. It is the one viewport an operator will never
 * test in and cannot resize, and it is where a venue's terms and Foyer's consent
 * sentence go below the fold. Showing it here costs almost nothing, because the
 * portal is already server-rendered documents.
 *
 * It shows **saved** state and says so. A preview of unsaved edits would need the
 * portal to render from a payload rather than from the API, and a preview that
 * silently lags is worse than one that states its terms.
 */
