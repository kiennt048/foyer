package api

import (
	"context"
	"errors"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Retention periods and data-subject erasure — the operator's side of Law
// 91/2025/QH15 Article 14 and GDPR Article 17.
//
// Both operations are `privacy:manage`, which only administrators hold: an erasure
// cannot be undone, and a retention period set too short deletes history on the
// next sweep. Both are audited **fail-closed** — the entry is written in the
// change's transaction and a change the log cannot record is not made (every other
// handler's `record` swallows, see handlers_audit.go) — and neither audit entry
// carries the identifier it is about.
//
// **No second administrator for a shorter period.** What the field does: PacketFence, authentik, Keycloak, UniFi and Omada let any
// administrator set their log windows and audit the change; none documents a
// four-eyes approval for it. NIST SP 800-53 AU-9(5) names dual authorization as an optional
// control enhancement, and AWS S3 Object Lock / Azure immutable blob
// storage answer the same worry by making the *floor* unmovable instead. Foyer's
// venues often have one administrator, so an approval would be a lock nobody can
// open; it takes the floor instead — held by the database (00051), re-applied by
// the sweep — which also keeps this change's own entry alive for at least the
// audit floor, 30 days.

func (s *Server) GetRetentionPolicy(ctx context.Context, _ gen.GetRetentionPolicyRequestObject) (gen.GetRetentionPolicyResponseObject, error) {
	if s.ports.Privacy == nil {
		return nil, errNotImplemented
	}
	p, err := s.ports.Privacy.RetentionPolicy(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetRetentionPolicy200JSONResponse(p), nil
}

func (s *Server) UpdateRetentionPolicy(ctx context.Context, request gen.UpdateRetentionPolicyRequestObject) (gen.UpdateRetentionPolicyResponseObject, error) {
	if s.ports.Privacy == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil || len(request.Body.Changes) == 0 {
		return nil, validation(map[string]string{"changes": "Required."}, map[string]gen.FieldFault{"changes": gen.FieldFaultRequired})
	}
	before, err := s.ports.Privacy.RetentionPolicy(ctx)
	if err != nil {
		return nil, err
	}
	bounds := map[gen.RetentionCategoryId]gen.RetentionCategoryPolicy{}
	for _, c := range before.Categories {
		bounds[c.Category] = c
	}

	// The bounds are the category's own, read from the same port that enforces
	// them, so the refusal names the figure the store would have refused on.
	errs := map[string]string{}
	faults := map[string]gen.FieldFault{}
	params := map[string]map[string]any{}
	for _, ch := range request.Body.Changes {
		field := "changes." + string(ch.Category)
		b, ok := bounds[ch.Category]
		if !ok {
			// The contract's enum already refuses an unknown category; a category
			// the contract knows and the store does not register is a server
			// defect, and saying "required" about it would be a lie.
			return nil, errors.New("api: a retention category the contract names is not registered")
		}
		if ch.Days == nil {
			continue
		}
		switch d := *ch.Days; {
		case d < b.MinimumDays:
			errs[field], faults[field] = "Too short.", gen.FieldFaultBelowMinimum
			params[field] = map[string]any{"minimumCount": b.MinimumDays}
		case d > b.MaximumDays:
			errs[field], faults[field] = "Too long.", gen.FieldFaultAboveMaximum
			params[field] = map[string]any{"maximumCount": b.MaximumDays}
		}
	}
	if len(faults) > 0 {
		return nil, withFieldParams(validation(errs, faults), params)
	}

	who := callerFrom(ctx)
	if who == nil {
		return nil, notSignedIn()
	}

	// Old and new, per changed category: a period shortened from 90 days to 1 is the
	// change an administrator reading this log later most needs to see. Computed
	// from the request and the bounds read above, because the entry is written in
	// the change's own transaction — before there is an "after" to read.
	changed := map[string]any{}
	for _, ch := range request.Body.Changes {
		prev := bounds[ch.Category]
		to := prev.DefaultDays
		if ch.Days != nil {
			to = *ch.Days
		}
		if to != prev.Days {
			changed[string(ch.Category)] = map[string]any{"from": prev.Days, "to": to}
		}
	}
	after, err := s.ports.Privacy.AmendRetentionPolicy(ctx, request.Body.Changes, who.Operator.Username,
		func() (AuditEntry, error) {
			return s.auditEntry(who, "", gen.AuditKindRetentionPolicyChanged, gen.AuditSubject{
				Kind: gen.AuditSubjectKindRetentionPolicy, Id: "retention-policy",
			}, map[string]any{"changed": changed}), nil
		})
	if err != nil {
		return nil, err
	}
	return gen.UpdateRetentionPolicy200JSONResponse(after), nil
}

func (s *Server) EraseDataSubject(ctx context.Context, request gen.EraseDataSubjectRequestObject) (gen.EraseDataSubjectResponseObject, error) {
	if s.ports.Privacy == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"subject": "Required."}, map[string]gen.FieldFault{"subject": gen.FieldFaultRequired})
	}
	subj := request.Body.Subject
	want := ErasureSubject{Kind: subj.Kind}
	required := func(field string, v *string) (string, error) {
		if v == nil || strings.TrimSpace(*v) == "" {
			return "", validation(map[string]string{field: "Required."}, map[string]gen.FieldFault{field: gen.FieldFaultRequired})
		}
		return strings.TrimSpace(*v), nil
	}
	var err error
	switch subj.Kind {
	case gen.DataSubjectKindMac:
		want.MAC, err = required("subject.mac", subj.Mac)
	case gen.DataSubjectKindAccount:
		want.AccountID, err = required("subject.accountId", subj.AccountId)
	case gen.DataSubjectKindPhone:
		want.Phone, err = required("subject.phone", subj.Phone)
	case gen.DataSubjectKindEmail:
		want.Email, err = required("subject.email", subj.Email)
	}
	if err != nil {
		return nil, err
	}

	who := callerFrom(ctx)
	if who == nil {
		return nil, notSignedIn()
	}
	// The receipt's id is the audit entry's subject id, so the two can be matched,
	// and the entry names the subject masked: an audit log that kept the MAC it
	// records erasing would not have erased it. Built by the port's transaction
	// from its own receipt, and **fail-closed**: an erasure the log cannot
	// record is not made on Foyer's side, the export's rule for the same reason —
	// the action is irreversible.
	id := s.newID("era-")
	receipt, err := s.ports.Privacy.EraseDataSubject(ctx, want, func(r gen.DataSubjectErasure) (AuditEntry, error) {
		removed := map[string]any{}
		for _, t := range r.Removed {
			removed[t.Table] = t.Rows
		}
		params := map[string]any{"subjectKind": string(r.Subject.Kind), "removed": removed, "total": r.Total}
		if len(r.Pseudonymised) > 0 {
			p := map[string]any{}
			for _, t := range r.Pseudonymised {
				p[t.Table] = t.Rows
			}
			params["pseudonymised"] = p
		}
		return s.auditEntry(who, "", gen.AuditKindDataSubjectErased, gen.AuditSubject{
			Kind: gen.AuditSubjectKindDataSubject, Id: id, Name: ptr(r.Subject.Masked),
		}, params), nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrNotAMACAddress):
			return nil, validation(map[string]string{"subject.mac": "Not a MAC address."},
				map[string]gen.FieldFault{"subject.mac": gen.FieldFaultNotAMacAddress})
		case errors.Is(err, ErrNotAPhoneNumber):
			return nil, validation(map[string]string{"subject.phone": "Not a phone number."},
				map[string]gen.FieldFault{"subject.phone": gen.FieldFaultNotAPhoneNumber})
		case errors.Is(err, ErrNotAnEmailAddress):
			return nil, validation(map[string]string{"subject.email": "Not an e-mail address."},
				map[string]gen.FieldFault{"subject.email": gen.FieldFaultNotAnEmailAddress})
		}
		return nil, err
	}
	receipt.Id = id
	if receipt.Pseudonymised == nil {
		receipt.Pseudonymised = []gen.ErasedTable{}
	}
	return gen.EraseDataSubject200JSONResponse(receipt), nil
}
