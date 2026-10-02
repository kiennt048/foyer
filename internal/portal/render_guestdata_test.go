package portal

import (
	"strings"
	"testing"
)

// The guest-data half of the rendered documents.

func dataSettings() *RenderSettings {
	return &RenderSettings{
		VenueName: "Cà Phê Số 7",
		Methods:   []string{"voucher", "account", "click-through"},
		Languages: []string{"en", "vi"},
		Data: &RenderData{
			Fields: []RenderField{{Field: "name"}, {Field: "email", Required: true}},
			// Vietnamese only, so an English reader gets it marked with its language.
			ConsentText:     map[string]string{"vi": "Gửi ưu đãi cho tôi qua email."},
			ConsentVersions: map[string]string{"vi": strings.Repeat("ab", 32)},
		},
	}
}

// TestTheMarketingBoxIsNeverTickedAndNamesTheTextItShows: on every form that signs
// a guest in, the checkbox carries no `checked`, the hidden version is the one of
// the sentence rendered, and a sentence in another language says so.
func TestTheMarketingBoxIsNeverTickedAndNamesTheTextItShows(t *testing.T) {
	for _, path := range []string{"", "voucher/", "account/"} {
		page := RenderPage(RenderInput{Lang: "en", Path: path, Settings: dataSettings()})
		if page == nil {
			t.Fatalf("%q rendered nothing", path)
		}
		b := page.Body
		box := b[strings.Index(b, `name="marketingConsent"`)-40:]
		box = box[:strings.Index(box, "/>")]
		if strings.Contains(box, "checked") {
			t.Errorf("%q: the marketing box is pre-ticked: %s", path, box)
		}
		if !strings.Contains(b, `name="consentVersion" value="`+strings.Repeat("ab", 32)+`"`) {
			t.Errorf("%q: the version of the sentence shown is not beside the box", path)
		}
		if !strings.Contains(b, `<label for="g-consent" lang="vi">Gửi ưu đãi cho tôi qua email.</label>`) {
			t.Errorf("%q: the Vietnamese sentence is not marked as Vietnamese on an English page", path)
		}
		if !strings.Contains(b, `name="email" type="email"`) || !strings.Contains(b, `maxlength="254" autocapitalize="none" spellcheck="false" required`) {
			t.Errorf("%q: the required e-mail field is not required", path)
		}
		if !strings.Contains(b, `(optional)`) {
			t.Errorf("%q: the optional name field does not say it is optional", path)
		}
	}
}

// TestNoSentenceNoBox: consent asked with no text in any language renders no
// checkbox — there is no default promise the portal could make for a venue.
func TestNoSentenceNoBox(t *testing.T) {
	set := dataSettings()
	set.Data.ConsentText, set.Data.ConsentVersions = map[string]string{}, map[string]string{}
	page := RenderPage(RenderInput{Lang: "en", Path: "voucher/", Settings: set})
	if strings.Contains(page.Body, "marketingConsent") {
		t.Error("a checkbox was rendered with no sentence to consent to")
	}
}

// TestTheConnectedPageCarriesTheWayOut: where a venue asks for consent, the guest's
// withdrawal button is on the connected page, and its outcome is said there.
func TestTheConnectedPageCarriesTheWayOut(t *testing.T) {
	page := RenderPage(RenderInput{
		Lang: "vi", Path: "connected/", Settings: dataSettings(), Notice: "consent-withdrawn",
		Status: &RenderStatus{State: string(StateConnected)},
	})
	b := page.Body
	if !strings.Contains(b, `action="/api/v0/portal/marketing-consent/withdraw"`) {
		t.Error("no withdrawal form on the connected page")
	}
	if !strings.Contains(b, `role="status"`) || !strings.Contains(b, catalogueVi["notice.consent-withdrawn"]) {
		t.Error("the withdrawal is not confirmed")
	}
	forged := RenderPage(RenderInput{Lang: "en", Path: "connected/", Settings: dataSettings(), Notice: "<script>"})
	if strings.Contains(forged.Body, `role="status"`) {
		t.Error("an unknown ?n= rendered a notice")
	}
}

// TestThePhoneCodeFormAsksForTheVenuesDetailsButNotThePhoneAgain: the
// code form carries the venue's fields and its unticked box — and not `phone`,
// because the number the code was sent to is the phone the venue collects.
func TestThePhoneCodeFormAsksForTheVenuesDetailsButNotThePhoneAgain(t *testing.T) {
	set := dataSettings()
	set.Methods = append(set.Methods, "phone")
	set.Data.Fields = []RenderField{{Field: "name", Required: true}, {Field: "phone", Required: true}}
	page := RenderPage(RenderInput{Lang: "en", Path: "phone/", PhoneSent: true, Settings: set})
	b := page.Body
	form := b[strings.Index(b, `action="/api/v0/portal/phone/code"`):]
	form = form[:strings.Index(form, "</form>")]
	if !strings.Contains(form, `name="name" type="text"`) || !strings.Contains(form, `name="marketingConsent"`) {
		t.Errorf("the code form does not carry the venue's details: %s", form)
	}
	if strings.Contains(form, `name="phone"`) {
		t.Errorf("the code form asks for the phone number the code just proved: %s", form)
	}
}
