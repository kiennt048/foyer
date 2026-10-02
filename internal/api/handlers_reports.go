package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/export"
	"github.com/kiennt048/foyer/internal/portal"
)

// The `reports` operations: three CSV exports and the trends behind the
// dashboard chart.
//
// ── what the field does, and where this follows it ───────────────────────────
//
// daloRADIUS — the closest thing to this product in the open-source RADIUS world —
// filters its accounting reports on `acctstarttime` and offers each listing as a
// CSV download, and it records report queries in its own action log. Foyer does
// the same three things: the session export is ranged on `startedAt`, every
// export is a CSV, and every export is an audit entry. It diverges in two places,
// both deliberate:
//
//   - **The document is streamed, never assembled.** daloRADIUS renders a page and
//     then serialises it; `radacct` is the one table in this schema that only grows,
//     and this process lives in a 320 MiB container. `ReportReader` takes a visitor
//     for that reason and this file never holds more than one row.
//   - **The audit entry is written before the first row, and a failure to write it
//     refuses the export.** Everywhere else `record` is best-effort, because the
//     change has already happened and failing the response would lie about it
//     (invariant 10). Here nothing has happened yet: the data has not left, so the
//     honest order is *record, then send*, and an export the log cannot see is an
//     export that does not happen.
//
// ── the columns are the contract's ───────────────────────────────────────────
//
// Each export operation declares `x-csv-columns`. `readReports` binds that list to
// the value functions below at construction, and **refuses to start** if either
// side has a column the other lacks — the same standing `portalBudgets` gives a
// figure the contract declares (invariant 11). `web/mock` reads the same list, so
// the two servers' headers are one header.

// reports is what `New` reads out of the contract for these operations.
type reports struct {
	sessions dataset[Session]
	vouchers dataset[Voucher]
	guests   dataset[portal.Grant]
	consents dataset[gen.MarketingConsent]

	// maxDays is `x-max-range-days` per export operation.
	maxDays map[string]int
	// trendDays is `getTrends`'s `x-max-range-days`, per granularity.
	trendDays map[gen.TrendGranularity]int
}

// column is one named cell and how a row produces it.
type column[T any] struct {
	name  string
	value func(T) string
}

// dataset is one export's columns, in the contract's order.
type dataset[T any] struct {
	name    string
	columns []column[T]
}

func (d dataset[T]) header() []string {
	out := make([]string, len(d.columns))
	for i, c := range d.columns {
		out[i] = c.name
	}
	return out
}

func (d dataset[T]) row(v T) []string {
	out := make([]string, len(d.columns))
	for i, c := range d.columns {
		out[i] = c.value(v)
	}
	return out
}

// sessionCells is how a `Session` becomes a row. The names are checked against
// `exportSessions`'s `x-csv-columns`, in both directions, by `readReports`.
var sessionCells = map[string]func(Session) string{
	"sessionId":     func(s Session) string { return s.Id },
	"acctSessionId": func(s Session) string { return export.Text(s.AcctSessionId) },
	"state":         func(s Session) string { return string(s.State) },
	"startedAt":     func(s Session) string { return export.At(s.StartedAt) },
	"endedAt":       func(s Session) string { return export.Time(s.EndedAt) },
	"lastUpdateAt":  func(s Session) string { return export.Time(s.LastUpdateAt) },
	// Signed on purpose: a skewed NAS clock makes it negative, and the contract
	// calls that the anomaly worth seeing. `export.Neutralise` leaves a plain
	// decimal alone for exactly this column.
	"sessionSeconds": func(s Session) string { return export.Int(s.SessionSeconds) },
	"identityKind": func(s Session) string {
		if s.IdentityKind == nil {
			return ""
		}
		return string(*s.IdentityKind)
	},
	// **A voucher code never leaves in full**, and for a voucher session the NAS
	// put the code in User-Name. Masked here rather than in the store, because it
	// is a rule about a document leaving the product and not about the row.
	"username": func(s Session) string {
		if s.IdentityKind != nil && *s.IdentityKind == gen.SessionIdentityKindVoucher {
			return export.CodeHint(export.Text(s.Username))
		}
		return export.Text(s.Username)
	},
	"callingStationId":  func(s Session) string { return s.CallingStationId },
	"macIsRandomised":   func(s Session) string { return export.Bool(s.MacIsRandomised) },
	"ipAddress":         func(s Session) string { return export.Text(s.IpAddress) },
	"nasId":             func(s Session) string { return s.NasId },
	"nasName":           func(s Session) string { return export.Text(s.NasName) },
	"apName":            func(s Session) string { return export.Text(s.ApName) },
	"ssid":              func(s Session) string { return export.Text(s.Ssid) },
	"inputOctets":       func(s Session) string { return export.Int(s.InputOctets) },
	"outputOctets":      func(s Session) string { return export.Int(s.OutputOctets) },
	"inputOctetsTotal":  func(s Session) string { return export.Int(s.InputOctetsTotal) },
	"outputOctetsTotal": func(s Session) string { return export.Int(s.OutputOctetsTotal) },
	"countersWrapped":   func(s Session) string { return strconv.FormatBool(s.CountersWrapped) },
	"terminateCause":    func(s Session) string { return export.Text(s.TerminateCause) },
	// The figures above are samples, and a spreadsheet does not change that.
	"freshnessState":           func(s Session) string { return string(s.Freshness.State) },
	"freshnessSampledAt":       func(s Session) string { return export.Time(s.Freshness.SampledAt) },
	"freshnessIntervalSeconds": func(s Session) string { return export.Int(s.Freshness.IntervalSeconds) },
}

// voucherCells never carries `code`. See `exportVouchers` in the contract.
var voucherCells = map[string]func(Voucher) string{
	"voucherId":   func(v Voucher) string { return v.Id },
	"batchId":     func(v Voucher) string { return v.BatchId },
	"codeHint":    func(v Voucher) string { return export.CodeHint(v.Code) },
	"state":       func(v Voucher) string { return string(v.State) },
	"createdAt":   func(v Voucher) string { return export.Time(v.CreatedAt) },
	"firstUsedAt": func(v Voucher) string { return export.Time(v.FirstUsedAt) },
	"expiresAt":   func(v Voucher) string { return export.Time(v.ExpiresAt) },
	"revokedAt":   func(v Voucher) string { return export.Time(v.RevokedAt) },
}

var guestCells = map[string]func(portal.Grant) string{
	"grantId":   func(g portal.Grant) string { return g.ID },
	"grantedAt": func(g portal.Grant) string { return export.At(g.GrantedAt) },
	"method":    func(g portal.Grant) string { return string(g.Method) },
	"clientMac": func(g portal.Grant) string { return g.ClientMAC },
	"macIsRandomised": func(g portal.Grant) string {
		return strconv.FormatBool(export.LocallyAdministered(g.ClientMAC))
	},
	// Only an account grant has a username; a voucher grant's "username" would be
	// its code, which is the value `exportVouchers` refuses to send.
	"accountUsername": func(g portal.Grant) string {
		if g.Method != portal.MethodAccount {
			return ""
		}
		return g.Username
	},
	"voucherId": func(g portal.Grant) string { return g.VoucherID },
	"expiresAt": func(g portal.Grant) string { return export.Time(g.ExpiresAt) },
	"revokedAt": func(g portal.Grant) string { return export.Time(g.RevokedAt) },
}

// readReports binds the contract's declarations to this file.
func readReports() (reports, error) {
	spec, err := gen.GetSwagger()
	if err != nil {
		return reports{}, fmt.Errorf("api: reading the embedded contract: %w", err)
	}
	extensions := map[string]map[string]any{}
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			extensions[op.OperationID] = op.Extensions
		}
	}

	var out reports
	var errs []error
	out.sessions, err = bind(extensions, "ExportSessions", "sessions", sessionCells)
	errs = append(errs, err)
	out.vouchers, err = bind(extensions, "ExportVouchers", "vouchers", voucherCells)
	errs = append(errs, err)
	out.guests, err = bind(extensions, "ExportGuests", "guests", guestCells)
	errs = append(errs, err)
	out.consents, err = bind(extensions, "ExportMarketingConsents", "marketing-consents", consentCells)
	errs = append(errs, err)

	out.maxDays = map[string]int{}
	for _, op := range []string{"ExportSessions", "ExportVouchers", "ExportGuests", "ExportMarketingConsents"} {
		n, ok := wholeNumber(extensions[op]["x-max-range-days"])
		if !ok {
			errs = append(errs, fmt.Errorf("api: %s declares no x-max-range-days", op))
			continue
		}
		out.maxDays[op] = n
	}
	out.trendDays = map[gen.TrendGranularity]int{}
	byGranularity, _ := extensions["GetTrends"]["x-max-range-days"].(map[string]any)
	for _, g := range []gen.TrendGranularity{gen.Day, gen.Week} {
		n, ok := wholeNumber(byGranularity[string(g)])
		if !ok {
			errs = append(errs, fmt.Errorf("api: getTrends declares no x-max-range-days for %s", g))
			continue
		}
		out.trendDays[g] = n
	}
	return out, errors.Join(errs...)
}

func bind[T any](ext map[string]map[string]any, op, name string, cells map[string]func(T) string) (dataset[T], error) {
	raw, ok := ext[op]["x-csv-columns"].([]any)
	if !ok || len(raw) == 0 {
		return dataset[T]{}, fmt.Errorf("api: %s declares no x-csv-columns", op)
	}
	d := dataset[T]{name: name}
	declared := map[string]bool{}
	var faults []string
	for _, r := range raw {
		col, _ := r.(string)
		declared[col] = true
		value, ok := cells[col]
		if !ok {
			faults = append(faults, fmt.Sprintf("%q is declared and has no value here", col))
			continue
		}
		d.columns = append(d.columns, column[T]{name: col, value: value})
	}
	for col := range cells {
		if !declared[col] {
			faults = append(faults, fmt.Sprintf("%q has a value here and is not declared", col))
		}
	}
	if len(faults) > 0 {
		return dataset[T]{}, fmt.Errorf("api: %s's x-csv-columns and internal/api disagree: %s",
			op, strings.Join(faults, "; "))
	}
	return d, nil
}

func wholeNumber(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), n == float64(int(n)) && n > 0
	case int:
		return n, n > 0
	}
	return 0, false
}

// ── the three exports ────────────────────────────────────────────────────────

func (s *Server) ExportSessions(ctx context.Context, request gen.ExportSessionsRequestObject) (gen.ExportSessionsResponseObject, error) {
	p := request.Params
	r, err := s.exportRange("ExportSessions", p.From, p.To)
	if err != nil {
		return nil, err
	}
	stream, err := begin(ctx, s, string(p.SiteId), r, s.reports.sessions,
		func(ctx context.Context, visit func(Session) error) error {
			return s.ports.Reports.SessionsStarted(ctx, string(p.SiteId), r, visit)
		})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (s *Server) ExportVouchers(ctx context.Context, request gen.ExportVouchersRequestObject) (gen.ExportVouchersResponseObject, error) {
	p := request.Params
	r, err := s.exportRange("ExportVouchers", p.From, p.To)
	if err != nil {
		return nil, err
	}
	stream, err := begin(ctx, s, string(p.SiteId), r, s.reports.vouchers,
		func(ctx context.Context, visit func(Voucher) error) error {
			return s.ports.Reports.VoucherActivity(ctx, string(p.SiteId), r, visit)
		})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (s *Server) ExportGuests(ctx context.Context, request gen.ExportGuestsRequestObject) (gen.ExportGuestsResponseObject, error) {
	p := request.Params
	r, err := s.exportRange("ExportGuests", p.From, p.To)
	if err != nil {
		return nil, err
	}
	stream, err := begin(ctx, s, string(p.SiteId), r, s.reports.guests,
		func(ctx context.Context, visit func(portal.Grant) error) error {
			return s.ports.Reports.GuestGrants(ctx, string(p.SiteId), r, visit)
		})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// exportRange refuses an unbounded, inverted or over-long range.
func (s *Server) exportRange(op string, from, to time.Time) (Range, error) {
	if !to.After(from) {
		return Range{}, rangeInverted()
	}
	limit := s.reports.maxDays[op]
	if to.Sub(from) > time.Duration(limit)*24*time.Hour {
		return Range{}, rangeTooLong(limit)
	}
	return Range{From: from, To: to}, nil
}

func rangeInverted() *apiError {
	return validation(
		map[string]string{"to": "Must be after the start of the range."},
		map[string]gen.FieldFault{"to": gen.FieldFaultRangeInverted},
	)
}

func rangeTooLong(maxDays int) *apiError {
	e := validation(
		map[string]string{"to": fmt.Sprintf("At most %d days.", maxDays)},
		map[string]gen.FieldFault{"to": gen.FieldFaultRangeTooLong},
	)
	e.Params = map[string]any{"maxDays": maxDays}
	e.FieldParams = map[string]map[string]any{"to": {"maxDays": maxDays}}
	return e
}

// begin checks the site, records the export and returns the stream.
//
// **The order is the guarantee.** The audit entry is written before a byte of the
// document exists, and if it cannot be written the export is refused — see the top
// of this file for why this read, and only this one, is recorded fail-closed.
func begin[T any](ctx context.Context, s *Server, siteID string, r Range, d dataset[T],
	scan func(context.Context, func(T) error) error) (*csvStream, error) {
	if s.ports.Reports == nil || s.ports.Audit == nil {
		// Both, because an export with nowhere to record it is refused rather than
		// sent quietly.
		return nil, errNotImplemented
	}
	site, err := s.siteNamed(ctx, siteID)
	if err != nil {
		return nil, err
	}
	who := callerFrom(ctx)
	if who == nil {
		return nil, notSignedIn()
	}
	if err := s.ports.Audit.Append(ctx, AuditEntry{
		Id:            s.newID("aud-"),
		At:            s.now(),
		ActorId:       who.Operator.Id,
		ActorUsername: who.Operator.Username,
		Kind:          gen.AuditKindDataExported,
		SiteId:        ptr(site.Id),
		Subject:       &gen.AuditSubject{Kind: gen.AuditSubjectKindSite, Id: site.Id, Name: ptr(site.Name)},
		Params: &map[string]any{
			"dataset": d.name,
			"from":    r.From.UTC().Format(time.RFC3339),
			"to":      r.To.UTC().Format(time.RFC3339),
		},
	}); err != nil {
		return nil, err
	}
	return &csvStream{
		ctx:      ctx,
		filename: exportFilename(d.name, site.Id, r),
		header:   d.header(),
		rows: func(ctx context.Context, emit func([]string) error) error {
			return scan(ctx, func(v T) error { return emit(d.row(v)) })
		},
	}, nil
}

// siteNamed is the site, or `404` for one that does not exist.
//
// An export of an unknown site would otherwise be a header and no rows, which reads
// as *nothing happened there* about a venue that is not there.
func (s *Server) siteNamed(ctx context.Context, siteID string) (Site, error) {
	if s.ports.Sites == nil {
		return Site{}, errNotImplemented
	}
	sites, err := s.ports.Sites.Sites(ctx)
	if err != nil {
		return Site{}, err
	}
	for _, site := range sites {
		if site.Id == siteID {
			return site, nil
		}
	}
	return Site{}, ErrNotFound
}

// exportFilename is built from the site id and two dates and nothing else.
//
// Nothing an operator or a guest typed reaches a `Content-Disposition` header, and
// the site id is filtered to the characters a filename can hold without quoting —
// it is minted by the server, and the filter is what keeps that true of a header
// if it ever stops being true of the id.
func exportFilename(dataset, siteID string, r Range) string {
	clean := strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			return c
		}
		return -1
	}, siteID)
	return fmt.Sprintf("foyer-%s-%s-%s-%s.csv", dataset, clean,
		r.From.UTC().Format("20060102"), r.To.UTC().Format("20060102"))
}

// csvStream is the response of all three export operations.
//
// It implements the generated `Visit…Response` methods itself rather than handing
// the generated type an `io.Reader`, because the generated one writes `200` before
// it reads a byte of the body — so a query that fails on its first row would
// arrive as a `200` with an empty document, which reads as *nothing happened*.
// Here the status is written on the first byte that is actually ready:
//
//   - **a failure before any byte** returns the error, and the caller writes the
//     contract's problem document with its real status;
//   - **a failure after the first byte aborts the connection** (`http.ErrAbortHandler`).
//     A chunked body that ends without its terminator is a failed download in every
//     client; a body that ends cleanly with rows missing is a complete-looking file
//     that is quietly short, and a spreadsheet cannot tell the difference.
type csvStream struct {
	ctx      context.Context
	filename string
	header   []string
	rows     func(ctx context.Context, emit func([]string) error) error
}

func (c *csvStream) VisitExportSessionsResponse(w http.ResponseWriter) error { return c.stream(w) }
func (c *csvStream) VisitExportVouchersResponse(w http.ResponseWriter) error { return c.stream(w) }
func (c *csvStream) VisitExportGuestsResponse(w http.ResponseWriter) error   { return c.stream(w) }

// exportWriteWindow is how long one chunk may take to leave. The admin listener's
// `WriteTimeout` bounds the whole response at 30 s, which a quarter of `radacct`
// on a busy venue can exceed; the deadline is moved forward per chunk instead, so a
// stalled reader is still cut off and a slow one is not.
const exportWriteWindow = 30 * time.Second

func (c *csvStream) stream(w http.ResponseWriter) error {
	out := &lazyStatus{w: w, rc: http.NewResponseController(w), filename: c.filename}
	cw := export.NewWriter(out)
	err := cw.Header(c.header)
	if err == nil {
		err = c.rows(c.ctx, cw.Row)
	}
	if err == nil {
		err = cw.Flush()
	}
	switch {
	case err == nil:
		return nil
	case !out.started:
		return err
	default:
		panic(http.ErrAbortHandler)
	}
}

// lazyStatus writes the headers and `200` on the first byte, not before.
type lazyStatus struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	filename string
	started  bool
}

func (l *lazyStatus) Write(p []byte) (int, error) {
	if !l.started {
		h := l.w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+l.filename+`"`)
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		l.w.WriteHeader(http.StatusOK)
		l.started = true
	}
	// Not every writer supports a deadline (a test recorder does not); one that
	// does not is bounded by the listener's own timeout instead.
	_ = l.rc.SetWriteDeadline(time.Now().Add(exportWriteWindow))
	return l.w.Write(p)
}

// ── trends ───────────────────────────────────────────────────────────────────

// GetTrends answers per-bucket totals.
//
// The buckets are decided here and handed to the port, so every implementation
// counts over the same boundaries: the site's calendar day (`getSiteTimeZone`) —
// `vouchersRedeemedToday`'s day — and a Monday-start week. `from` is floored and `to` ceiled to a boundary.
func (s *Server) GetTrends(ctx context.Context, request gen.GetTrendsRequestObject) (gen.GetTrendsResponseObject, error) {
	if s.ports.Reports == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	g := gen.Day
	if p.Granularity != nil {
		g = *p.Granularity
	}
	if g != gen.Day && g != gen.Week {
		// The generated binder does not check an enum, so this is the answer it
		// gives every other unreadable query parameter (`surface.go`), not a new
		// one: one fault, one document.
		return nil, validation(
			map[string]string{"query": "Missing or unreadable."},
			map[string]gen.FieldFault{"query": gen.FieldFaultRequired},
		)
	}
	if !p.To.After(p.From) {
		return nil, rangeInverted()
	}
	buckets := trendBuckets(p.From, p.To, g, s.siteLocation(ctx, string(p.SiteId)))
	limit := s.reports.trendDays[g]
	step := 1
	if g == gen.Week {
		step = 7
	}
	if len(buckets)*step > limit {
		return nil, rangeTooLong(limit)
	}
	if _, err := s.siteNamed(ctx, string(p.SiteId)); err != nil {
		return nil, err
	}
	counted, err := s.ports.Reports.Trends(ctx, string(p.SiteId), buckets)
	if err != nil {
		return nil, err
	}
	if len(counted) != len(buckets) {
		// A port that answered a different number of buckets has answered a
		// different question, and a chart would draw it against the wrong days.
		return nil, fmt.Errorf("api: trends answered %d buckets for %d asked", len(counted), len(buckets))
	}
	for i := range counted {
		counted[i].Start, counted[i].End = buckets[i].From, buckets[i].To
	}
	return gen.GetTrends200JSONResponse(gen.Trends{Granularity: g, Buckets: counted}), nil
}

// trendBuckets floors `from` and ceils `to` to local bucket boundaries.
//
// `AddDate`, never `Add(24h)`: a day is not 24 hours where a clock moves, and the
// default zone (Asia/Ho_Chi_Minh) does not move its clock but others do.
func trendBuckets(from, to time.Time, g gen.TrendGranularity, loc *time.Location) []Range {
	start := floorDay(from.In(loc))
	days := 1
	if g == gen.Week {
		days = 7
		start = start.AddDate(0, 0, -((int(start.Weekday()) + 6) % 7))
	}
	var out []Range
	for b := start; b.Before(to); b = b.AddDate(0, 0, days) {
		out = append(out, Range{From: b, To: b.AddDate(0, 0, days)})
		if len(out) > 1000 {
			// Unreachable behind the range check the caller makes next; a bound
			// here keeps a caller that forgot it from building an unbounded slice.
			break
		}
	}
	return out
}

func floorDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
