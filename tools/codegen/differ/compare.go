package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Comparing two servers' answers, and being explicit about what "the same" means.
//
// Three classes of difference, and keeping them apart is the whole value of the
// harness:
//
//  1. **Volatile** — a generated id, a timestamp, a freshly minted token. These
//     cannot match and must not be compared. They are normalised away.
//  2. **Server-specific by the contract's own rules** — `Problem.title` and
//     `Problem.detail`. The contract says both are "the server's wording" and a
//     fallback only, that a client with copy for the `type` must prefer its own,
//     and that composing them in the server's language and rendering them
//     verbatim "is how the console ended up showing Vietnamese to an
//     English-speaking operator". Comparing two servers on a field the contract
//     defines as per-server would be comparing noise.
//  3. **Everything else** — compared exactly. A difference here is a finding.
//
// Nothing else is relaxed. In particular `type`, `status`, `params`, `errors`
// and every business field are compared byte for byte after normalisation, and
// a step that expects a divergence has to say so in the scenario with a reason.
//
// ── every difference carries the key it is about ─────────────────────────────
//
// A `diff` names the response key it concerns, which is what lets a scenario step
// declare a divergence **scoped to the fields it is about** rather than to the whole
// answer. That distinction is what makes the adapter mode worth running: `Session`
// diverges on `plan` and `voucherId` for reasons the adapter states in place, and a
// whole-step declaration covering those would also have covered a wrong byte counter
// on the same row. A declaration that swallows everything is the shape of a check
// that is not one.

// rfc3339ish matches the date-time strings this contract emits. Anything that
// parses is replaced, because a timestamp minted by two processes seconds apart
// can never match and a harness that compared them would be permanently red.
var rfc3339ish = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)

const (
	placeholderTime     = "<timestamp>"
	placeholderVolatile = "<volatile>"
	placeholderProse    = "<server wording>"
)

// The three pseudo-keys, for the parts of an answer that are not body fields. A
// divergence may name them exactly as it names a JSON property.
const (
	keyStatus      = "status"
	keyContentType = "content-type"
	keyLocation    = "location"
	keyBody        = "body"
)

// diff is one difference, with the key it is about.
//
// `Key` is the leaf property name — `plan`, `nextCursor`, `status` — and `Path` is
// where it was found, which is what makes a finding actionable at 500 vouchers:
// "items[3].plan.enforcedBy" says which decision differs.
type diff struct {
	Key  string
	Path string
	Text string
}

func (d diff) String() string { return d.Text }

// nullsDropped records every field name where one server wrote an explicit
// `null` and the other omitted the key. See `dropNulls` — this exists so the
// equivalence is reported rather than applied silently.
var nullsDropped = map[string]int{}

// dropNulls removes every key whose value is `null`.
//
// **Why this is an equivalence and not a loosened assertion.** The mock writes
// an explicit `null` for an optional nullable property; `oapi-codegen` generates
// `*T` with `omitempty` for the same property, so the Go server can only omit
// it. The contract declares these as `type: [string, "null"]` *and* optional, so
// both encodings conform, and `openapi-typescript` gives the admin console
// `field?: T | null` — a client that handles one handles the other.
//
// **The case this would hide, and why it does not arise.** A *required* nullable
// property must be present and may be null; omitting one would be a violation
// this rule would swallow. There are two — `Metric.value` and `Page.nextCursor`
// — and `oapi-codegen` emits both without `omitempty` precisely because they are
// required, so neither can be omitted by construction. `requiredNullables`
// below asserts it on every list response anyway, because "by construction" is
// the kind of claim that stops being true after a codegen upgrade.
func dropNulls(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				nullsDropped[k]++
				continue
			}
			out[k] = dropNulls(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = dropNulls(val)
		}
		return out
	default:
		return v
	}
}

// requiredNullables asserts that a list response carries `nextCursor` as a key,
// whatever its value. `Page` declares it `required`, so an absent one is a
// contract violation — and it is the one thing `dropNulls` above could hide.
func requiredNullables(label string, body []byte) []diff {
	v, err := decode(body)
	if err != nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	if _, isList := m["items"]; !isList {
		return nil
	}
	if _, ok := m["nextCursor"]; !ok {
		return []diff{{
			Key:  "nextCursor",
			Path: "nextCursor",
			Text: label + " omits `nextCursor`, which `Page` declares required",
		}}
	}
	return nil
}

// normalise walks a decoded JSON value and blanks what cannot be compared.
//
// `volatile` names keys to blank wherever they appear in this response. It is
// per-step rather than global on purpose: `id` is volatile in the answer to a
// create and is the most important field in the answer to a read, and a global
// rule would silently stop comparing it everywhere.
func normalise(v any, volatile map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if volatile[k] {
				out[k] = placeholderVolatile
				continue
			}
			out[k] = normalise(val, volatile)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalise(val, volatile)
		}
		return out
	case string:
		if rfc3339ish.MatchString(t) {
			if _, err := time.Parse(time.RFC3339, t); err == nil {
				return placeholderTime
			}
		}
		return t
	default:
		return v
	}
}

// stripServerWording removes `title` and `detail` from a problem document.
//
// See class 2 above. It applies only where the body *is* a problem document —
// identified by a `type` that starts `/problems/`, which is what
// `Problem.type`'s enum guarantees — so a business object with a `title` member
// (`Alarm` has an optional one, for the same fallback reason) keeps its own only
// when it is not a refusal.
func stripServerWording(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	kind, ok := m["type"].(string)
	if !ok || !strings.HasPrefix(kind, "/problems/") {
		return v
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		if k == "title" || k == "detail" {
			continue
		}
		// `errors` is field name → human message, and the contract says the
		// keys are "the load-bearing half — they are what tells a client which
		// inputs to mark — and they stay. The values are prose and follow the
		// same fallback rule as `detail`." So the keys are compared and the
		// prose is not.
		if k == "errors" {
			if fields, ok := val.(map[string]any); ok {
				blanked := make(map[string]any, len(fields))
				for name := range fields {
					blanked[name] = placeholderProse
				}
				out[k] = blanked
				continue
			}
		}
		out[k] = val
	}
	return out
}

// answer is one server's reply, reduced to what a comparison is about.
type answer struct {
	Status      int
	ContentType string
	Location    string
	Body        []byte
}

// compareOpts carries what one mode relaxes, so the relaxation is a value that can
// be printed rather than a branch buried in the walk.
type compareOpts struct {
	// SortItems compares an array of identified objects as a set rather than as a
	// sequence. **The contract defines no order for any list operation**, and the
	// two servers genuinely choose different ones — the oracle answers in fixture
	// order, `internal/store` orders by id or by `created_at DESC`. Comparing the
	// sequences would turn one contract gap into several hundred diffs and hide
	// every real one behind them. The gap itself is reported separately, and
	// `orderDiffered` records whether it actually bit on this answer.
	SortItems bool
}

// compare returns the differences between two answers, or nothing.
func compare(mock, goSrv answer, volatile map[string]bool, opts compareOpts) (diffs []diff, orderDiffered bool) {
	if mock.Status != goSrv.Status {
		diffs = append(diffs, diff{Key: keyStatus, Path: keyStatus,
			Text: fmt.Sprintf("status: mock %d, go %d", mock.Status, goSrv.Status)})
	}
	if mediaType(mock.ContentType) != mediaType(goSrv.ContentType) {
		diffs = append(diffs, diff{Key: keyContentType, Path: keyContentType,
			Text: fmt.Sprintf("content-type: mock %q, go %q",
				mediaType(mock.ContentType), mediaType(goSrv.ContentType))})
	}
	// `Location` matters on the portal's `303`s and nowhere else, but a
	// difference in one is exactly the kind of thing that ships a portal that
	// authenticates a guest and then throws them off the network.
	//
	// **It is only compared because the client does not follow redirects.** An
	// `http.Client` with no `CheckRedirect` follows a `303`, the answer carries no
	// `Location` at all, and this comparison runs on two empty strings on every
	// step. See `harnessHTTPClient`.
	if canonicalLocation(mock.Location) != canonicalLocation(goSrv.Location) {
		diffs = append(diffs, diff{Key: keyLocation, Path: keyLocation,
			Text: fmt.Sprintf("location: mock %q, go %q", mock.Location, goSrv.Location)})
	}

	mv, mErr := decode(mock.Body)
	gv, gErr := decode(goSrv.Body)
	// **A CSV export is compared cell by cell, not as bytes.** The `reports`
	// exports are documents a spreadsheet opens, and a byte comparison would
	// report one difference — "the body" — for a wrong byte counter in row 40 and
	// for a timestamp minted a second apart alike, which is a declaration that
	// swallows everything. Parsed into one object per row keyed by the header, the
	// same normalisation, volatile keys and key-scoped declarations apply to a
	// column as to a JSON property. The bytes themselves are held equal elsewhere:
	// `internal/export`'s golden file, read by both writers' tests.
	if mediaType(mock.ContentType) == "text/csv" && mediaType(goSrv.ContentType) == "text/csv" {
		mv, mErr = decodeCSV(mock.Body)
		gv, gErr = decodeCSV(goSrv.Body)
	}
	switch {
	case mErr != nil && gErr != nil:
		if !equalBytes(mock.Body, goSrv.Body) {
			diffs = append(diffs, diff{Key: keyBody, Path: keyBody,
				Text: fmt.Sprintf("body (not JSON): mock %q, go %q",
					truncate(mock.Body), truncate(goSrv.Body))})
		}
		return diffs, false
	case mErr != nil:
		diffs = append(diffs, diff{Key: keyBody, Path: keyBody,
			Text: fmt.Sprintf("mock body is not JSON: %q", truncate(mock.Body))})
		return diffs, false
	case gErr != nil:
		diffs = append(diffs, diff{Key: keyBody, Path: keyBody,
			Text: fmt.Sprintf("go body is not JSON: %q", truncate(goSrv.Body))})
		return diffs, false
	}

	diffs = append(diffs, requiredNullables("mock", mock.Body)...)
	diffs = append(diffs, requiredNullables("go", goSrv.Body)...)

	mn := normalise(dropNulls(stripServerWording(mv)), volatile)
	gn := normalise(dropNulls(stripServerWording(gv)), volatile)
	if opts.SortItems {
		var a, b bool
		mn, a = sortIdentified(mn)
		gn, b = sortIdentified(gn)
		orderDiffered = a || b
	}
	if !reflect.DeepEqual(mn, gn) {
		diffs = append(diffs, describe("", mn, gn)...)
	}
	return diffs, orderDiffered
}

// sortIdentified orders every array of identified objects, and reports whether that
// changed anything — which, once both sides have been through it, is the same
// question as "did the two servers order this differently".
//
// Only arrays whose elements all carry one of the identifier keys are touched.
// `checks` on a probe result has none, and its order is the server's narrative
// rather than an arbitrary choice, so it stays compared as a sequence.
func sortIdentified(v any) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		moved := false
		for k, val := range t {
			sorted, m := sortIdentified(val)
			out[k] = sorted
			moved = moved || m
		}
		return out, moved

	case []any:
		items := make([]any, 0, len(t))
		moved := false
		for _, it := range t {
			sorted, m := sortIdentified(it)
			items = append(items, sorted)
			moved = moved || m
		}
		key := identifierFor(items)
		if key == "" {
			return items, moved
		}
		before := keysOf(items, key)
		sort.SliceStable(items, func(i, j int) bool {
			return textAt(items[i], key) < textAt(items[j], key)
		})
		return items, moved || !reflect.DeepEqual(before, keysOf(items, key))

	default:
		return v, false
	}
}

// identifierKeys is the ordered list of properties this contract uses to name a row.
// `nasId` is there for `WalledGarden.drivers`, which is a list of devices carrying
// no `id` of its own.
var identifierKeys = []string{"id", "nasId"}

func identifierFor(items []any) string {
	if len(items) < 2 {
		return ""
	}
	for _, key := range identifierKeys {
		ok := true
		for _, it := range items {
			m, isMap := it.(map[string]any)
			if !isMap {
				return ""
			}
			if _, has := m[key].(string); !has {
				ok = false
				break
			}
		}
		if ok {
			return key
		}
	}
	return ""
}

func keysOf(items []any, key string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, textAt(it, key))
	}
	return out
}

func textAt(v any, key string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// describe walks two normalised values and names each difference by path and key.
func describe(path string, a, b any) []diff {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return []diff{{Key: leafKey(path), Path: at(path),
				Text: fmt.Sprintf("%s: mock is an object, go is %s", at(path), kindOf(b))}}
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		var names []string
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)

		var out []diff
		for _, k := range names {
			ax, aok := av[k]
			bx, bok := bv[k]
			switch {
			case aok && !bok:
				out = append(out, diff{Key: k, Path: at(path + "." + k),
					Text: fmt.Sprintf("%s.%s: only the mock has it (%v)", at(path), k, brief(ax))})
			case !aok && bok:
				out = append(out, diff{Key: k, Path: at(path + "." + k),
					Text: fmt.Sprintf("%s.%s: only go has it (%v)", at(path), k, brief(bx))})
			default:
				out = append(out, describe(path+"."+k, ax, bx)...)
			}
		}
		return out

	case []any:
		bv, ok := b.([]any)
		if !ok {
			return []diff{{Key: leafKey(path), Path: at(path),
				Text: fmt.Sprintf("%s: mock is an array, go is %s", at(path), kindOf(b))}}
		}
		if len(av) != len(bv) {
			return []diff{{Key: leafKey(path), Path: at(path),
				Text: fmt.Sprintf("%s: mock has %d, go has %d", at(path), len(av), len(bv))}}
		}
		var out []diff
		for i := range av {
			out = append(out, describe(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i])...)
		}
		return out

	default:
		if !reflect.DeepEqual(a, b) {
			return []diff{{Key: leafKey(path), Path: at(path),
				Text: fmt.Sprintf("%s: mock %v, go %v", at(path), brief(a), brief(b))}}
		}
		return nil
	}
}

// leafKey is the last property name in a path, with any array index stripped. It is
// what a scenario's declared divergence names.
func leafKey(path string) string {
	p := strings.TrimPrefix(path, ".")
	if i := strings.LastIndex(p, "."); i >= 0 {
		p = p[i+1:]
	}
	if i := strings.IndexByte(p, '['); i >= 0 {
		p = p[:i]
	}
	if p == "" {
		return keyBody
	}
	return p
}

func at(path string) string {
	if path == "" {
		return "(root)"
	}
	return strings.TrimPrefix(path, ".")
}

func kindOf(v any) string {
	switch v.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func brief(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return truncate(b)
}

func truncate(b []byte) string {
	const max = 160
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

func decode(b []byte) (any, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// decodeCSV turns an export into `{"header": [...], "rows": [{column: cell}]}`.
//
// The header is kept as its own member so a column added, dropped or reordered on
// one side is a difference in its own right rather than something inferred from
// the rows. A missing byte-order mark is a difference too — the contract declares
// one — and is reported as a `bom` member rather than silently stripped.
func decodeCSV(b []byte) (any, error) {
	text := string(b)
	bom := strings.HasPrefix(text, "\uFEFF")
	recs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\uFEFF"))).ReadAll()
	if err != nil {
		return nil, err
	}
	out := map[string]any{"bom": bom}
	if len(recs) == 0 {
		return out, nil
	}
	header := make([]any, len(recs[0]))
	for i, h := range recs[0] {
		header[i] = h
	}
	rows := make([]any, 0, len(recs)-1)
	for _, rec := range recs[1:] {
		row := map[string]any{}
		for i, h := range recs[0] {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	out["header"], out["rows"] = header, rows
	return out, nil
}

func equalBytes(a, b []byte) bool { return string(a) == string(b) }

func mediaType(v string) string {
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(strings.ToLower(v))
}

// canonicalLocation sorts a redirect's query so that two servers building the
// same URL from a map are not reported as disagreeing about it.
//
// **Only the ordering, and nothing else.** The origin, the path, the parameter
// names and their values are all left exactly as each server wrote them, because
// each of those is a difference that matters: an absolute `Location` crosses an
// origin, a different path is a different page, and a different reason code is a
// different sentence to the guest. What carries no meaning at all is which of
// `?e=not-found&code=ABC` and `?code=ABC&e=not-found` a server happened to emit —
// `url.Values.Encode` sorts by key and a hand-built string does not, and RFC 3986
// gives the order no significance.
//
// A value this cannot parse is returned untouched, so an unparseable `Location`
// still compares as the bytes it is rather than silently becoming equal to
// another unparseable one.
func canonicalLocation(raw string) string {
	if raw == "" || !strings.Contains(raw, "?") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = u.Query().Encode()
	return u.String()
}
