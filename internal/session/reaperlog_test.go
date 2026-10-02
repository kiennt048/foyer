package session_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/session"
)

// TestTheReaperLogCarriesNoMAC: the reaper logs every session it closes — a count
// and a bug that closed live sessions would otherwise be the same line — and no
// line may carry the guest's MAC in full. A container log is a
// copy of personal data no erasure reaches and every support bundle carries, so the
// line names the device the way the audit log names an erased one.
func TestTheReaperLogCarriesNoMAC(t *testing.T) {
	requireDocker(t)
	owner := ownerPool(t)
	rp := reaperPool(t)
	clearRadacct(t, owner)
	insertSession(t, owner, "stale", 45*time.Minute, nil)

	for _, dry := range []bool{true, false} {
		var buf bytes.Buffer
		r := session.NewReaper(rp, session.ReaperConfig{DryRun: dry}, slog.New(slog.NewTextHandler(&buf, nil)))
		closed, err := r.Reap(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(closed) != 1 {
			t.Fatalf("dryRun=%v: %d sessions, want the stale one", dry, len(closed))
		}
		out := buf.String()
		if strings.Contains(out, "02:11:22:33:44:55") || strings.Contains(out, "02:11:22:33") {
			t.Errorf("dryRun=%v: the log carries the guest's MAC:\n%s", dry, out)
		}
		if !strings.Contains(out, "••:••:••:••:44:55") {
			t.Errorf("dryRun=%v: the log no longer names the device at all:\n%s", dry, out)
		}
	}
}
