package store_test

import (
	"context"
	"testing"
)

// TestTheZoneTimestampIsTheZonesOwn: `getSiteTimeZone.updatedAt` is when the zone
// changed. Were it `sites.updated_at`, a rename would move it and a site nobody
// had set a zone on would still report a time.
func TestTheZoneTimestampIsTheZonesOwn(t *testing.T) {
	requireDocker(t)
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	zone, changed, err := st.Sites().TimeZone(ctx, site.ID)
	fatalf(t, err, "reading the zone")
	if zone != "Asia/Ho_Chi_Minh" || changed != nil {
		t.Fatalf("a new site: zone %q changed %v, want the default and no change time", zone, changed)
	}

	fatalf(t, st.Sites().Rename(ctx, site.ID, "Renamed Venue"), "renaming")
	if _, changed, _ = st.Sites().TimeZone(ctx, site.ID); changed != nil {
		t.Fatalf("a rename set the zone's change time to %v", changed)
	}

	fatalf(t, st.Sites().SetTimeZone(ctx, site.ID, "Asia/Bangkok"), "setting the zone")
	_, first, err := st.Sites().TimeZone(ctx, site.ID)
	fatalf(t, err, "reading the zone")
	if first == nil {
		t.Fatal("changing the zone recorded no change time")
	}

	fatalf(t, st.Sites().SetTimeZone(ctx, site.ID, "Asia/Bangkok"), "storing the same zone")
	fatalf(t, st.Sites().Rename(ctx, site.ID, "Renamed Again"), "renaming")
	_, again, err := st.Sites().TimeZone(ctx, site.ID)
	fatalf(t, err, "reading the zone")
	if again == nil || !again.Equal(*first) {
		t.Errorf("storing the zone it already had, and a rename, moved the change time from %v to %v", first, again)
	}
}
