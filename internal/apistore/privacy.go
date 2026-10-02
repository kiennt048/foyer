package apistore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

var _ api.PrivacyStore = (*Adapter)(nil)

// errNoRetention is what erasure answers when the control plane could not open its
// foyer_retention connection. `/problems/internal` with no text reaches the client;
// the reason is in the log line cmd/foyer wrote at start-up and repeats every cycle.
var errNoRetention = errors.New("apistore: the retention connection is not open, so nothing can be erased")

// RetentionPolicy is every registered category with its period in force.
//
// A category the store registers and the contract does not name is refused rather
// than sent: the contract's enum is closed, and the mock validates every response
// against it, so sending one would be a response the schema forbids.
func (a *Adapter) RetentionPolicy(ctx context.Context) (gen.RetentionPolicy, error) {
	ps, err := a.st.Privacy().RetentionPolicies(ctx)
	if err != nil {
		return gen.RetentionPolicy{}, out(err)
	}
	outp := gen.RetentionPolicy{Categories: make([]gen.RetentionCategoryPolicy, 0, len(ps))}
	for _, p := range ps {
		id := gen.RetentionCategoryId(p.Category.ID)
		if !id.Valid() {
			return gen.RetentionPolicy{}, fmt.Errorf("apistore: retention category %q is registered in the store and absent from the contract", p.Category.ID)
		}
		c := gen.RetentionCategoryPolicy{
			Category: id, Days: p.Days, DefaultDays: p.Category.DefaultDays,
			MinimumDays: p.Category.MinDays, MaximumDays: p.Category.MaxDays,
			IsDefault: p.IsDefault, Tables: append([]string{}, p.Category.Tables...),
		}
		if sw := p.LastSweep; sw != nil {
			c.LastSweep = &gen.RetentionSweep{At: sw.At.UTC(), Removed: int(sw.Removed), Failed: sw.Failed}
			if sw.Cutoff != nil {
				t := sw.Cutoff.UTC()
				c.LastSweep.Cutoff = &t
			}
		}
		outp.Categories = append(outp.Categories, c)
	}
	return outp, nil
}

func (a *Adapter) AmendRetentionPolicy(ctx context.Context, changes []gen.RetentionPolicyChange, actor string,
	record func() (api.AuditEntry, error)) (gen.RetentionPolicy, error) {
	chs := make([]store.RetentionChange, 0, len(changes))
	for _, c := range changes {
		chs = append(chs, store.RetentionChange{Category: string(c.Category), Days: c.Days})
	}
	if err := a.st.Privacy().SetRetentionRecorded(ctx, chs, actor, func() (*store.AuditEntry, error) {
		e, err := record()
		if err != nil {
			return nil, err
		}
		return a.storeAuditEntry(e), nil
	}); err != nil {
		return gen.RetentionPolicy{}, out(err)
	}
	return a.RetentionPolicy(ctx)
}

func (a *Adapter) EraseDataSubject(ctx context.Context, want api.ErasureSubject,
	record func(gen.DataSubjectErasure) (api.AuditEntry, error)) (gen.DataSubjectErasure, error) {
	var subj store.Subject
	var err error
	kind := want.Kind
	switch kind {
	case gen.DataSubjectKindMac:
		subj, err = store.MACSubject(want.MAC)
		if errors.Is(err, store.ErrInvalidSubject) {
			return gen.DataSubjectErasure{}, api.ErrNotAMACAddress
		}
	case gen.DataSubjectKindAccount:
		subj, err = a.st.AccountSubject(ctx, want.AccountID)
	case gen.DataSubjectKindPhone:
		subj, err = store.PhoneSubject(want.Phone)
		if errors.Is(err, store.ErrInvalidSubject) {
			return gen.DataSubjectErasure{}, api.ErrNotAPhoneNumber
		}
	case gen.DataSubjectKindEmail:
		subj, err = store.EmailSubject(want.Email)
		if errors.Is(err, store.ErrInvalidSubject) {
			return gen.DataSubjectErasure{}, api.ErrNotAnEmailAddress
		}
	default:
		err = fmt.Errorf("apistore: unknown data subject kind %q", kind)
	}
	if err != nil {
		return gen.DataSubjectErasure{}, out(err)
	}
	if a.retention == nil {
		return gen.DataSubjectErasure{}, errNoRetention
	}
	var receipt gen.DataSubjectErasure
	_, err = store.EraseRecorded(ctx, a.st, a.retention, subj, func(rep store.ErasureReport) (*store.AuditEntry, error) {
		receipt = receiptOf(kind, subj, rep)
		e, err := record(receipt)
		if err != nil {
			return nil, err
		}
		return a.storeAuditEntry(e), nil
	})
	if err != nil {
		// The FreeRADIUS side may be gone and Foyer's is not: said once, at ERROR,
		// with the subject masked, so the window where one side is erased and the
		// other is not is never silent.
		slog.Error("data-subject erasure failed; Foyer's tables were left as they were, the FreeRADIUS tables may already be erased — run it again",
			"subject", subj.Masked(), "error", err.Error())
		return gen.DataSubjectErasure{}, out(err)
	}
	return receipt, nil
}

func receiptOf(kind gen.DataSubjectKind, subj store.Subject, rep store.ErasureReport) gen.DataSubjectErasure {
	receipt := gen.DataSubjectErasure{At: rep.At, Total: int(rep.Total),
		Removed: make([]gen.ErasedTable, 0, len(rep.Removed)), Pseudonymised: make([]gen.ErasedTable, 0, len(rep.Pseudonymised))}
	receipt.Subject.Kind = kind
	receipt.Subject.Masked = subj.Masked()
	for _, r := range rep.Removed {
		receipt.Removed = append(receipt.Removed, gen.ErasedTable{Table: r.Table, Rows: int(r.Rows)})
	}
	for _, r := range rep.Pseudonymised {
		receipt.Pseudonymised = append(receipt.Pseudonymised, gen.ErasedTable{Table: r.Table, Rows: int(r.Rows)})
	}
	return receipt
}
