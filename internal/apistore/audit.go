package apistore

import (
	"context"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// AuditLog — append and read, and never anything else.
//
// The database is what makes that true rather than this file: `audit_log` is
// granted `SELECT, INSERT` and no `UPDATE` or `DELETE`, and the row-level policies
// exist only for those two commands. A log an administrator can edit is not a log.

// Audit lists entries.
//
// **Two of the three filters have no repository equivalent and are applied here.**
// `AuditFilter` takes a site, a kind and a free-text query; `listAudit` narrows on
// `actorId` and on a half-open time window. Both are filtered after the read, which
// costs a repository page per rejected row on a busy log. **Reported**:
// `store.AuditFilter` wants `ActorID`, `From` and `To` — the columns are indexed
// for the ordering already, so this belongs in SQL.
//
// `from` is inclusive and `to` is exclusive, which is what the contract says and is
// what lets two adjacent windows tile without double-counting an entry.
//
// **The site is left empty here on purpose and that is not the same as ignoring it.**
// `listAudit` declares no `siteId` parameter — the console's audit screen filters by
// actor and by date and deliberately not by venue, because half the entries have
// none and the header's switcher would hide exactly the ones an administrator opens
// the screen for. `AuditFilter.SiteID` is therefore reachable only from the store,
// which is where a row's own scope is asserted. It answers a venue with **zero
// rows** unless `internal/api` writes the site on each entry; the column and its
// index have been there since migration `00003`.
func (a *Adapter) Audit(ctx context.Context, q api.AuditQuery) (api.Page[gen.AuditEntry], error) {
	return fill(q.Window,
		func(cursor string, limit int) (store.Page[store.AuditEntry], error) {
			return a.st.Audit().List(ctx, store.AuditFilter{
				ListOptions: listOptions("", "", cursor, limit),
			})
		},
		func(e store.AuditEntry) (gen.AuditEntry, bool) {
			if q.ActorID != "" && e.ActorID != q.ActorID {
				return gen.AuditEntry{}, false
			}
			if q.From != nil && e.At.Before(*q.From) {
				return gen.AuditEntry{}, false
			}
			if q.To != nil && !e.At.Before(*q.To) {
				return gen.AuditEntry{}, false
			}
			return auditOut(e), true
		},
	)
}

// Append writes one immutable record.
//
// `actorUsername` is denormalised by the caller at the moment the entry is written,
// which is why `Operator.username` is immutable: an entry has to outlive the
// operator it names, and a log that renders "deleted operator" where the name
// should be is a log that fails exactly when it is being read for a reason.
func (a *Adapter) Append(ctx context.Context, e gen.AuditEntry) error {
	return out(a.st.Audit().Append(ctx, a.storeAuditEntry(e)))
}

// storeAuditEntry is the entry as the store writes it, with the time and id filled
// in the way Append fills them — for the privacy operations, which hand it to the
// store to insert inside their own transaction.
func (a *Adapter) storeAuditEntry(e gen.AuditEntry) *store.AuditEntry {
	entry := auditIn(e)
	if entry.At.IsZero() {
		entry.At = a.now()
	}
	if entry.ID == "" {
		entry.ID = a.newID("aud-")
	}
	return &entry
}
