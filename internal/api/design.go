package api

import (
	"strconv"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
)

// The portal page design: the rules beyond the schema, and the guest narrowing.
//
// The authority is the TypeScript twin every other implementation reads,
// `web/contract/src/design.ts` (`designFault`, `isLink`, `narrowDesign`). The
// order of the checks, the link patterns and the date rule are that file's, not
// this one's — a divergence here is the mock and the server refusing different
// designs, which the console cannot explain to anybody.

// isLink is the renderer's allowlist, not a second copy of it: the rule that
// refuses a link on the way in is the same function that re-checks it where it
// becomes an `href`, so the two cannot drift apart.
func isLink(u string) bool { return portal.IsLink(u) }

func isLinkKind(k gen.PortalBlockKind) bool { return k == gen.Button || k == gen.Social }

// designFault is the first semantic failure, in design.ts's order, or ok=false.
// First failure wins because that is what the mock answers too, and a console
// that highlights one block cannot highlight the second one differently.
func designFault(d gen.PortalDesign) (fault gen.FieldFault, block int, ok bool) {
	seen := map[string]bool{}
	for i, b := range d.Blocks {
		if seen[b.Id] {
			return gen.FieldFaultDuplicate, i, true
		}
		seen[b.Id] = true
	}
	for i, b := range d.Blocks {
		if isLinkKind(b.Kind) && b.Placement == gen.PortalBlockPlacementWelcome {
			return gen.FieldFaultLinkBeforeSignIn, i, true
		}
	}
	// Links are checked only on blocks a guest could see. A hidden block is a
	// draft, and a template's "Review us" button starts hidden with no link in it
	// — refusing that would refuse the template. It cannot reach a guest: hidden
	// blocks are narrowed out before anything renders, and the renderer checks
	// every link again where it becomes an `href`.
	for i, b := range d.Blocks {
		if b.Visible && b.Kind == gen.Button && portal.TrimText(derefStr(b.Url)) == "" {
			return gen.FieldFaultRequired, i, true
		}
	}
	for i, b := range d.Blocks {
		if !b.Visible {
			continue
		}
		if b.Kind == gen.Button && !isLink(derefStr(b.Url)) {
			return gen.FieldFaultNotALink, i, true
		}
		if b.Links != nil {
			for _, l := range *b.Links {
				if !isLink(l.Url) {
					return gen.FieldFaultNotALink, i, true
				}
			}
		}
	}
	for i, b := range d.Blocks {
		if b.StartsOn != nil && b.EndsOn != nil && dateOf(b.EndsOn) < dateOf(b.StartsOn) {
			return gen.FieldFaultEndsBeforeStart, i, true
		}
	}
	return "", 0, false
}

// designFaultProse is the sentence beside the fault. The console renders the
// fault, not this; it is for whoever reads the problem document by hand. The
// block is counted from one because that is how an owner counts them.
func designFaultProse(f gen.FieldFault, block int) string {
	n := "Block " + strconv.Itoa(block+1) + ": "
	switch f {
	case gen.FieldFaultDuplicate:
		return n + "another block has the same id."
	case gen.FieldFaultLinkBeforeSignIn:
		return n + "links only work after sign-in."
	case gen.FieldFaultRequired:
		return n + "a button needs a link."
	case gen.FieldFaultNotALink:
		return n + "not a web address or phone number."
	case gen.FieldFaultEndsBeforeStart:
		return n + "ends before it starts."
	default:
		return n + "cannot be saved."
	}
}

// dateOf is `YYYY-MM-DD`, which orders as a string exactly as it orders as a
// date — the property the TypeScript comparison relies on too.
func dateOf(d interface{ String() string }) string { return d.String() }

// A block's dates are compared against the **site's** calendar day
// (`Server.siteToday`, `sites.time_zone`): a campaign that starts on the 1st is
// written once by an owner who means *their* 1st — not UTC's, and not whatever
// `TZ` the server process happened to start with.

// isLive: visible, and inside its dates, inclusive at both ends.
func isLive(b gen.PortalBlock, today string) bool {
	if !b.Visible {
		return false
	}
	if b.StartsOn != nil && dateOf(b.StartsOn) > today {
		return false
	}
	if b.EndsOn != nil && dateOf(b.EndsOn) < today {
		return false
	}
	return true
}

// narrowDesign is the design a guest may read. Hidden and out-of-date blocks are
// not sent at all — not merely not rendered — so a promotion an owner switched off
// is not readable pre-authentication by anybody who asks for the context. Accent
// and headline pass through.
func narrowDesign(d gen.PortalDesign, today string) gen.PortalDesign {
	out := normaliseDesign(d)
	live := make([]gen.PortalBlock, 0, len(out.Blocks))
	for _, b := range out.Blocks {
		if isLive(b, today) {
			live = append(live, b)
		}
	}
	out.Blocks = live
	return out
}

// normaliseDesign makes a stored design wire-shaped: `blocks` is required and an
// array, never null. A venue that never designed a page — a row from before
// migration 00018, a fake's zero value — answers the empty design, which renders
// exactly the page Foyer rendered before designs existed.
func normaliseDesign(d gen.PortalDesign) gen.PortalDesign {
	if d.Blocks == nil {
		d.Blocks = []gen.PortalBlock{}
	}
	return d
}

// renderDesign converts an already narrowed design into the renderer's own
// types. The renderer does not import the generated package — it is the twin of
// the TypeScript renderer, not of the HTTP surface — so this is the seam.
func renderDesign(d gen.PortalDesign) *portal.Design {
	out := &portal.Design{
		Accent:   derefStr(d.Accent),
		Headline: map[string]string{},
	}
	if d.Headline.En != nil {
		out.Headline["en"] = *d.Headline.En
	}
	if d.Headline.Vi != nil {
		out.Headline["vi"] = *d.Headline.Vi
	}
	for _, b := range d.Blocks {
		rb := portal.Block{
			ID:        b.Id,
			Kind:      string(b.Kind),
			Placement: string(b.Placement),
			Code:      derefStr(b.Code),
			URL:       derefStr(b.Url),
			Content:   map[string]portal.BlockText{},
		}
		if b.Links != nil {
			for _, l := range *b.Links {
				rb.Links = append(rb.Links, portal.SocialLink{Network: string(l.Network), URL: l.Url})
			}
		}
		if b.Content.En != nil {
			rb.Content["en"] = blockText(*b.Content.En)
		}
		if b.Content.Vi != nil {
			rb.Content["vi"] = blockText(*b.Content.Vi)
		}
		out.Blocks = append(out.Blocks, rb)
	}
	return out
}

func blockText(t gen.PortalBlockText) portal.BlockText {
	out := portal.BlockText{Title: derefStr(t.Title), Body: derefStr(t.Body), Label: derefStr(t.Label)}
	if t.Items != nil {
		for _, it := range *t.Items {
			out.Items = append(out.Items, portal.BlockItem{Label: it.Label, Value: derefStr(it.Value)})
		}
	}
	return out
}
