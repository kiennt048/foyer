# internal/export — CONTRACT

The CSV writer behind the `reports` operations, and the one place a cell is made
safe to open in a spreadsheet.

## Invariants

1. **Every cell a `Writer` emits has been through `Neutralise`**, header included.
   There is no method that writes a raw cell, so a column added later is safe by
   construction. A cell whose **first significant rune** is `=`, `+`, `-`, `@`,
   TAB, CR, LF, the full-width `＝ ＋ － ＠`, the small-form `﹦ ﹢ ﹣ ﹫` or the minus
   sign `−` gains a leading `'` (OWASP, *CSV Injection*). "Significant" skips
   leading `unicode.IsSpace` runes (NBSP, NEL, ideographic space) and category Cf
   (BOM, ZWSP, bidi marks), because a spreadsheet looks past them too; TAB, CR and
   LF end the skip, being triggers themselves.

2. **The one exception is a plain decimal** — `^-?[0-9]+(\.[0-9]+)?$`. It cannot be
   a formula, and a negative `sessionSeconds` is the anomaly the contract wants
   sortable. `-1+1`, `+5` and `-2e3` are not plain decimals and are quoted.

3. **The document is RFC 4180 with CRLF, opened by a UTF-8 byte-order mark, and
   every field is double-quoted** — header and empty cells included, `"` doubled.
   Quoting only what `encoding/csv` thinks needs it would let a bare `a;=cmd|…` reach
   a spreadsheet whose list separator is `;`, which splits it and evaluates the second
   half. `TestEveryFieldIsQuotedSoNoSeparatorOpensACell` reads the document
   with `,`, `;` and TAB. An embedded `\n` is written `\r\n` and a `\r` is dropped —
   the spelling of `encoding/csv`'s CRLF mode, and `web/mock` reproduces it.

4. **Nothing reaches the underlying writer until a buffer's worth is ready.** The
   byte-order mark and the header are buffered with the first rows, so a caller
   streaming to an HTTP response can still answer a failure before the first flush
   with a status. `internal/api`'s `csvStream` depends on this.

5. **The bytes are pinned** by `testdata/golden.csv`, generated from
   `testdata/vectors.json`. `web/mock/src/reports.test.ts` reads the same two files
   and must produce the same bytes, so neither writer can drift alone.
   `go test ./internal/export -run Golden -update` rewrites the golden after a
   deliberate change — and the TypeScript test then says whether the mock agrees.

6. **A voucher code never leaves in full.** `CodeHint` is the last four characters
   behind an ellipsis, and a code of four or fewer characters yields nothing.

7. **`LocallyAdministered` is a declared copy of `store.MACIsLocallyAdministered`**,
   because `internal/api` may not import the store. `TestLocallyAdministeredAgreesWithTheStore`
   holds the two together.

8. **An absent value is an empty cell, never a zero.** Timestamps are RFC 3339 in
   UTC at second precision; booleans are `true` / `false`.
