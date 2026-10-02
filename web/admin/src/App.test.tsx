import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import { PageHead } from "./App.jsx";

/**
 * The page heading is the console's one route-change focus target, so its *shape* is
 * pinned here and its behaviour in `web/e2e`.
 *
 * **`@foyer/admin` has no jsdom**, so focus is not observable in this suite at all —
 * `renderToStaticMarkup` never runs an effect. What is observable is whether the
 * heading could be focused if something tried, and that is exactly the half that
 * regresses silently: `heading.current?.focus()` on an element with no `tabindex` is
 * a no-op that throws nothing, logs nothing and leaves the operator on `<body>` —
 * which is the defect it was written to fix.
 */
describe("the page heading", () => {
  const markup = renderToStaticMarkup(
    <MemoryRouter>
      <PageHead title="Vouchers" description="a description" />
    </MemoryRouter>,
  );

  it("is programmatically focusable without becoming a tab stop", () => {
    expect(markup).toContain('<h1 class="page-title" tabindex="-1">Vouchers</h1>');
  });

  /**
   * The decision recorded, not just the code.
   *
   * Focus landing on a `tabindex="-1"` heading is itself the announcement — a screen
   * reader reads "Vouchers, heading level 1". A live region beside it would say the
   * same words twice, and `PageHead` is on every screen, so it would also say them
   * where nothing navigated: `/access-points/<id>` and `/vouchers/<batch>` title
   * themselves from a fetch that lands long after the route change.
   */
  it("is not also a live region", () => {
    expect(markup).not.toMatch(/aria-live|role="status"|role="alert"/);
  });
});
