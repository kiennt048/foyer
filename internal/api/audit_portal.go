package api

// What the portal's audit entries say about a change to a venue's page.
//
// ── why this file exists ─────────────────────────────────────────────────────
//
// `network-settings-changed` and the radius half of the same kind record
// `{"changed": {field: {"from": …, "to": …}}}`, and so do the maintenance
// settings. The portal's two writers recorded *that* something happened and
// nothing about what. That is the shape a wrong entry takes, and
// `docs/rules/database.md` is explicit about the consequence: entries are
// append-only, an erasure pseudonymises them but never deletes one, and **a
// wrong entry is corrected by a later one**. A row that cannot say whether the
// venue name, the accent colour, the logo or the whole block list moved is a row
// nobody can correct, because a correction is itself an entry and has to name
// what it corrects.
//
// So the same grammar, over the same thing the other writers diff: the settings
// in force before the write and the settings in force after it.
//
// ── why the difference, and not the fields the request mentioned ─────────────
//
// `UpdatePortalSettings` is a PATCH, and its body is a set of pointers where a
// nil means "not supplied". Deciding what changed from *which pointers were
// non-nil* records every save as a change to everything it mentioned — including
// the console's own habit of sending the whole form. Deciding it from the
// difference records a change only where the row moved, and an empty difference
// writes no entry at all.
//
// ── the two classes of value, and why they are treated differently ───────────
//
// A **short** value is echoed. A venue name, a language list, a click-through
// window: a reader three months later needs to see that the name went from
// "Café mới" to something else, and a digest would not tell them what.
//
// A **long** value is not, and this is the tension: `audit_log`
// is append-only, so anything written into it is a copy nobody can edit later.
// `terms.summary` is venue-authored legal text a venue may withdraw, and a
// `logoDataUri` is a data URI of up to 100 kB. A full copy of either is a
// liability with no owner. Past `auditValueMax` the value is replaced by its
// **byte length and a truncated SHA-256**, which is what the rest of the product
// already does with a value it must not reproduce: `internal/redact` keeps a
// guest's last two MAC octets rather than the address, and
// `foyer_audit_pseudonymise` replaces an exact identifier with an HMAC. Enough

// to prove the text moved, to match two entries that carry the same text, and to
// show a reader that the length is a length and not a truncation — and not the
// text.
//
// **The design is a third case, and it is neither.** A whole layout dumped into a
// row is the liability described above, so the design is not echoed; but a bare
// block count ("2 became 1") cannot tell an operator which block went, which is
// the only question the page builder's log exists to answer. It is reduced to a
// **roster** — id, kind, placement, visibility, in order, plus the accent and the
// headline — which is bounded (twelve blocks, `PortalDesign.Blocks`) and says
// what moved without reproducing the page. The text a block carries is not in
// the roster: it is marketing copy the venue can edit, and the log's only
// `UPDATE` path is a text redaction that would then rewrite the entry.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// auditValueMax is the longest value the portal's entries echo whole, in bytes.
//
// **Two hundred, and chosen rather than derived.** It is above every short value
// the product has — a venue name, a language list, a same-origin asset path —
// and below the shortest one whose loss would hurt: a headline a guest reads on
// every portal launch. It is a boundary on what the *log* may hold, not a limit
// on what the venue may store, so it is not the contract's `x-max-decoded-bytes`
// and nothing refuses a value for being over it.
const auditValueMax = 200

// auditDigest replaces a value too long to echo. The field names say what the
// number is, so an entry is readable without this comment.
type auditDigest struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// auditValue is one field's value as an audit entry may hold it.
func auditValue(v any) any {
	if s, ok := v.(string); ok {
		return auditString(s)
	}
	// A slice, a number, a bool: measured in its JSON form, which is the form
	// the entry stores it in anyway.
	b, err := json.Marshal(v)
	if err != nil {
		return "(not recorded)"
	}
	if len(b) <= auditValueMax {
		return v
	}
	return digestOf(len(b), b)
}

func auditString(s string) any {
	if len(s) <= auditValueMax {
		return s
	}
	return digestOf(len(s), []byte(s))
}

// digestOf is 128 bits of SHA-256 rendered as hex. Sixteen characters is enough
// to say "these two entries carry the same text" and to notice that two do not;
// the remaining 256 bits would only make a longer row.
func digestOf(n int, b []byte) auditDigest {
	sum := sha256.Sum256(b)
	return auditDigest{Bytes: n, SHA256: hex.EncodeToString(sum[:8])}
}

// auditChange is one field's `from`/`to`, the shape `transportChanges` and the
// maintenance settings already write.
func auditChange(before, after any) map[string]any {
	return map[string]any{"from": auditValue(before), "to": auditValue(after)}
}

// portalAudit is a site's portal settings reduced to what an entry may hold,
// taken as a copy **before** the write so that a store which amends its own
// value in place cannot retroactively change what the `from` side says.
type portalAudit struct {
	venueName           string
	networkName         string
	logoDataURI         string
	clickThroughMinutes *int
	languages           []string
	methods             []string
	design              map[string]any
	terms               map[string]string
}

func snapshotPortalAudit(set gen.PortalSettings) portalAudit {
	return portalAudit{
		venueName:           set.VenueName,
		networkName:         deref(set.NetworkName),
		logoDataURI:         deref(set.LogoDataUri),
		clickThroughMinutes: set.ClickThroughMinutes,
		languages:           toStrings(set.Languages),
		methods:             toStrings(set.Methods),
		design:              designRoster(set.Design),
		terms:               maps.Clone(set.Terms.Summary),
	}
}

// settingsChanges is `changed` for `portal-settings-changed`: everything on the
// page except the terms, which have a row of their own.
func settingsChanges(before, after portalAudit) map[string]any {
	out := map[string]any{}
	put := func(field string, was, now any) { out[field] = auditChange(was, now) }
	if before.venueName != after.venueName {
		put("venueName", before.venueName, after.venueName)
	}
	if before.networkName != after.networkName {
		put("networkName", before.networkName, after.networkName)
	}
	if !slices.Equal(before.languages, after.languages) {
		put("languages", before.languages, after.languages)
	}
	if !slices.Equal(before.methods, after.methods) {
		put("methods", before.methods, after.methods)
	}
	if !equalInts(before.clickThroughMinutes, after.clickThroughMinutes) {
		put("clickThroughMinutes", before.clickThroughMinutes, after.clickThroughMinutes)
	}
	// A logo is the field where redaction earns its place: the value is a data
	// URI of up to a hundred kilobytes, and a same-origin path — which is what
	// an upload becomes — is short enough to read, so the reader can see which
	// file was replaced.
	if before.logoDataURI != after.logoDataURI {
		put("logoDataUri", before.logoDataURI, after.logoDataURI)
	}
	if !sameDesign(before.design, after.design) {
		// Not `put`: the roster is already bounded, and `auditValue` measures
		// it at its JSON length — a twelve-block page is over the cap, and
		// digesting it would throw away the block names the entry exists to
		// carry. Its own short leaves are capped where `designRoster` sets them.
		out["design"] = map[string]any{"from": before.design, "to": after.design}
	}
	return out
}

// termsChanges is `changed` for `portal-terms-changed`, per language. One venue
// publishes the terms in every language it has a page in, and "the Vietnamese
// terms were replaced" is a different entry from "the English ones were".
func termsChanges(before, after portalAudit) map[string]any {
	out := map[string]any{}
	for lang, was := range before.terms {
		if now, ok := after.terms[lang]; !ok || now != was {
			from := was
			if !ok {
				from = ""
			}
			out["terms.summary."+lang] = map[string]any{"from": auditString(from), "to": auditString(now)}
		}
	}
	for lang, now := range after.terms {
		if _, ok := before.terms[lang]; !ok {
			out["terms.summary."+lang] = map[string]any{"from": auditString(""), "to": auditString(now)}
		}
	}
	return out
}

// designRoster is the page as a reader can name it: the blocks in the order they
// are shown, each by id, kind, placement and visibility, and the two short
// leaves of the design above them. **Not the content** — see the file comment.
// sameDesign compares two rosters by their JSON form. `maps.Equal` is the
// obvious call and panics here — the roster holds a slice of blocks, which is
// not comparable — and `reflect.DeepEqual` would compare the cap and length of
// two slices built by two separate loops, which is the wrong question. The JSON
// form is the form the entry stores, and `encoding/json` sorts a map's keys, so
// two rosters that read alike marshal alike.
func sameDesign(a, b map[string]any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}

func designRoster(d gen.PortalDesign) map[string]any {
	blocks := make([]map[string]any, 0, len(d.Blocks))
	for _, b := range d.Blocks {
		blocks = append(blocks, map[string]any{
			"id": b.Id, "kind": string(b.Kind),
			"placement": string(b.Placement), "visible": b.Visible,
		})
	}
	return map[string]any{
		"accent":   auditString(deref(d.Accent)),
		"headline": auditValue(map[string]string{"en": deref(d.Headline.En), "vi": deref(d.Headline.Vi)}),
		"blocks":   blocks,
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// `gen.Language` and `gen.PortalMethod` are both `~string`, so one conversion
// serves both lists and neither needs its own loop.

// equalInts compares two optional integers, where nil and 0 are different states
// on the wire and the entry has to be able to say so.
func equalInts(a, b *int) bool {
	switch {
	case a == nil || b == nil:
		return a == b
	default:
		return *a == *b
	}
}

func toStrings[T ~string](in []T) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, string(v))
	}
	return out
}

func methodsToStrings(in []gen.PortalMethod) []string { return toStrings(in) }
