import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Table, type Column } from "./table.js";

/**
 * The pairing `Column`'s type cannot express, and the width nothing here can reach.
 *
 * `narrow: "keep" | "drop"` is required, so an *undecided* column is already a
 * compile error and needs no test. What the type cannot say is that a column with
 * `header: ""` exists only to hold controls — Probe and Delete on a controller,
 * Revoke on a voucher code, Enable and Delete on an account, Delete on a garden
 * entry — and that dropping one is not a deferral. It puts `<dt></dt>` in the
 * disclosure list, and it removes from a phone the only control that can put a live
 * guest off the network.
 *
 * **The second test is the one that makes the first mean anything.** A `toThrow`
 * assertion passes for a `Table` that throws on every input, which is exactly the
 * shape a careless guard takes; the identical table with the column marked `keep`
 * must render, and render the control.
 *
 * `renderToStaticMarkup` is the wide layout by construction — `useNarrow` reads
 * `useSyncExternalStore`, whose server snapshot is `false`, and this package has no
 * DOM and no `matchMedia` to give it a different answer. So the narrow half of the
 * column-dropping rule is asserted in `web/e2e` at a real 390px viewport, and what is asserted here is
 * the half a server render *can* see and a browser review cannot: that above the
 * breakpoint the disclosure is **absent** rather than hidden, and that dropping a
 * column changed nothing about the wide table it was declared for.
 */

interface Row {
  id: string;
  name: string;
}

const ROWS: Row[] = [{ id: "nas-reception", name: "Reception AP" }];

const NAME: Column<Row> = {
  key: "name",
  narrow: "keep",
  header: "Name",
  cell: (r) => r.name,
};

const SEEN: Column<Row> = {
  key: "lastSeen",
  narrow: "drop",
  header: "Last seen",
  cell: () => "40 days ago",
};

/** An actions column: no header, and a control inside it. */
const actions = (narrow: "keep" | "drop"): Column<Row> => ({
  key: "actions",
  narrow,
  header: "",
  align: "end",
  width: "1%",
  cell: () => (
    <button type="button" className="btn btn-sm btn-ghost">
      Revoke
    </button>
  ),
});

const render = (columns: Column<Row>[]) =>
  renderToStaticMarkup(<Table columns={columns} rows={ROWS} rowKey={(r) => r.id} />);

describe("a column of controls may not be dropped", () => {
  it("refuses an empty header paired with narrow: drop, and names the column", () => {
    expect(() => render([NAME, actions("drop")])).toThrow(/"actions"/);
  });

  it("renders the same table when that column is kept", () => {
    // The discriminator. Without it the assertion above is satisfied by a `Table`
    // that throws unconditionally, which is the failure mode a guard actually has.
    expect(render([NAME, actions("keep")])).toContain("Revoke");
  });
});

/**
 * A query that has never answered is not an empty collection.
 *
 * React Query's `isLoading` is `isPending && isFetching`, so a query at
 * `fetchStatus: "paused"` — offline, request queued, never run — arrives here as
 * *not loading, no error, no rows*, and `Table` rendered its never-had-data state
 * for it. Driven offline against the running console on three screens: *"No
 * sessions."*, *"No accounts."* with an **Add account** button, *"No voucher
 * batches."* with **Create batch** — with `aria-busy` false over all of them. It is
 * pre-existing on all eleven list screens; the auth work found it as *"No operators.
 * Nobody can sign in to this console."*
 *
 * The discriminator is `updatedAt`, which is `dataUpdatedAt` and is 0 until a query
 * has succeeded once. These four cases are the whole truth table, and the fourth is
 * the one that keeps the fix from being "never show the empty state".
 */
describe("an unanswered query is not an empty collection", () => {
  const table = (props: { updatedAt?: number; isLoading?: boolean; error?: unknown }) =>
    renderToStaticMarkup(
      <Table columns={[NAME]} rows={[]} rowKey={(r) => r.id} empty={<span>NO ROWS</span>} {...props} />,
    );

  it("renders the skeleton, not the empty state, for a query that has never answered", () => {
    const markup = table({ updatedAt: 0 });
    expect(markup, "the paused query still renders the never-had-data state").not.toContain(
      "NO ROWS",
    );
    expect(markup).toContain('aria-busy="true"');
  });

  it("renders the empty state once the query has answered with nothing", () => {
    // The discriminator. Without it the case above is satisfied by a `Table` that
    // never renders an empty state at all, which is a different defect of the same
    // size: a collection that is genuinely empty would show a spinner for ever.
    const markup = table({ updatedAt: Date.now() });
    expect(markup).toContain("NO ROWS");
    expect(markup).toContain('aria-busy="false"');
  });

  it("lets an error win over both", () => {
    const markup = table({ updatedAt: 0, error: new Error("boom") });
    expect(markup).not.toContain("NO ROWS");
    expect(markup, "a failed query is also unanswered, and the error is the truer state").
      toContain('aria-busy="false"');
  });

  it("keeps the static behaviour for a caller that passes no updatedAt", () => {
    /*
     * The gallery renders all four fetch states from static arrays and passes no
     * query, so "never answered" is unknowable there and must not be guessed. All
     * eleven real lists pass `dataUpdatedAt`.
     */
    expect(table({})).toContain("NO ROWS");
  });
});

describe("above the breakpoint the disclosure does not exist", () => {
  const MARKUP = render([NAME, SEEN]);

  it("renders every column, dropped ones included", () => {
    // Guards the two below: markup that failed to render would satisfy both.
    expect(MARKUP).toContain("Reception AP");
    expect(MARKUP).toContain("Last seen");
    expect(MARKUP).toContain("40 days ago");
  });

  it("adds no control and no definition list", () => {
    /*
     * Absent, not hidden and not disabled. A button left in the document at every
     * width is a tab stop with nothing under it and a name a screen reader reads out
     * on every row — the same mistake as disabling a control instead of removing it,
     * made where a sighted reviewer never sees it.
     */
    expect(MARKUP).not.toContain("row-disclosure");
    expect(MARKUP).not.toContain("<dl");
  });
});

/**
 * A checkbox on a row nothing can be done to.
 *
 * The same rule as the actions column one control along, and it fails in a way a
 * reviewer does not see: the row *looks* selected, the bar counts it, and the batch
 * quietly acts on a smaller set than the number it just showed. Two of seven garden
 * entries are required and have no delete control at all; a revoked voucher has no
 * revoke control.
 *
 * The second and third assertions are what make the first mean anything — a `Table`
 * that rendered no checkboxes at all would satisfy an absence check on its own, and
 * a select-all box wired to every row rather than to the pickable ones sits
 * permanently indeterminate over a set that is entirely selected.
 */
describe("a row with no action behind it gets no checkbox", () => {
  const ROWS3: Row[] = [
    { id: "a", name: "Alpha" },
    { id: "b", name: "Bravo" },
    { id: "c", name: "Charlie" },
  ];
  const selection = (checked: string[], selectable?: (r: Row) => boolean) => ({
    checked: new Set(checked),
    onToggle: () => {},
    onToggleAll: () => {},
    selectable,
    rowLabel: (r: Row) => `Select ${r.name}`,
    allLabel: "Select every row on screen",
  });
  const render3 = (checked: string[], selectable?: (r: Row) => boolean) =>
    renderToStaticMarkup(
      <Table
        columns={[NAME]}
        rows={ROWS3}
        rowKey={(r) => r.id}
        selection={selection(checked, selectable)}
      />,
    );

  it("renders one per row when every row is selectable", () => {
    const markup = render3([]);
    expect(markup.match(/data-role="row-select"/g)).toHaveLength(4); // 3 rows + select-all
    expect(markup).toContain("Select Charlie");
  });

  it("leaves out the box, and only the box, for a row that answers false", () => {
    const markup = render3([], (r) => r.id !== "c");
    expect(markup.match(/data-role="row-select"/g)).toHaveLength(3);
    expect(markup, "the label goes with the control it named").not.toContain("Select Charlie");
    expect(markup, "the row is still on screen, and still readable").toContain("Charlie");
    expect(markup.match(/check-col/g)?.length, "the cell stays so the column does not shift").toBe(4);
  });

  it("reads select-all against the pickable rows, not every row", () => {
    /* Both selectable rows ticked. Counting the third would leave this box
       indeterminate over a set the operator has entirely selected. */
    const head = (markup: string) => markup.slice(0, markup.indexOf("<tbody"));
    /* `indeterminate` is a property set through a ref and cannot be rendered on the
       server, so `checked` is the whole observable state here — which is exactly the
       state a reading over every row gets wrong: two of three ticked leaves it unchecked. */
    expect(head(render3(["a", "b"], (r) => r.id !== "c")), "all pickable rows ticked").toContain(
      "checked",
    );
    expect(head(render3(["a"], (r) => r.id !== "c")), "one of two is not all of them").not.toContain(
      "checked",
    );
  });
});
