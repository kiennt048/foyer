package raddb

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func snapshot(clients ...ClientRow) Snapshot {
	cfg := DefaultConfig()
	cfg.PostgresPassword = "pg-password-for-this-test-only"
	return Snapshot{Config: cfg, Clients: clients}
}

func ap(ip string) ClientRow {
	return ClientRow{NASName: ip, Shortname: "ap-" + ip, Type: "other", Secret: "secret-for-" + ip}
}

// TestClassifyPinsEachClass is the tested contract docs/rules/radius.md asks
// for: the operator is shown which of the three they are about to trigger, so
// each of the three has to be produced by something.
func TestClassifyPinsEachClass(t *testing.T) {
	cases := []struct {
		name    string
		running Snapshot
		wanted  Snapshot
		want    Class
		because string
	}{
		{
			name:    "nothing changed",
			running: snapshot(ap("192.168.1.10")),
			wanted:  snapshot(ap("192.168.1.10")),
			want:    ClassNone,
		},
		{
			name:    "an access point is registered",
			running: snapshot(ap("192.168.1.10")),
			wanted:  snapshot(ap("192.168.1.10"), ap("192.168.1.11")),
			want:    ClassSQLOnly,
			because: "192.168.1.11",
		},
		{
			name:    "the first access point on an empty site",
			running: snapshot(),
			wanted:  snapshot(ap("10.20.30.40")),
			want:    ClassSQLOnly,
		},
		{
			name:    "a client that is a range rather than an address",
			running: snapshot(),
			wanted:  snapshot(ap("192.168.7.0/24")),
			want:    ClassRestart,
			because: "is a range",
		},
		{
			name:    "an access point outside every dynamic network",
			running: snapshot(),
			wanted:  snapshot(ap("203.0.113.7")),
			want:    ClassRestart,
			because: "startup load",
		},
		{
			name:    "a secret is rotated on a client the server loaded at startup",
			running: snapshot(ap("192.168.1.10")),
			wanted: func() Snapshot {
				s := snapshot(ap("192.168.1.10"))
				s.Clients[0].Secret = "rotated"
				return s
			}(),
			want:    ClassRestart,
			because: "does not re-read",
		},
		{
			name:    "an access point is removed",
			running: snapshot(ap("192.168.1.10"), ap("192.168.1.11")),
			wanted:  snapshot(ap("192.168.1.10")),
			want:    ClassRestart,
			because: "until it restarts",
		},
		{
			name:    "the policy API moves",
			running: snapshot(ap("192.168.1.10")),
			wanted: func() Snapshot {
				s := snapshot(ap("192.168.1.10"))
				s.Config.PolicyAPIURL = "https://elsewhere/api/v0/aaa/authorize"
				return s
			}(),
			want:    ClassRestart,
			because: "PolicyAPIURL",
		},
		{
			name:    "the interim interval changes",
			running: snapshot(),
			wanted: func() Snapshot {
				s := snapshot()
				s.Config.DefaultInterimIntervalSeconds = 600
				return s
			}(),
			want:    ClassRestart,
			because: "DefaultInterimIntervalSeconds",
		},
		{
			name:    "EAP is turned on",
			running: snapshot(),
			wanted: func() Snapshot {
				s := snapshot()
				s.Config.EAP = EAPConfig{Enabled: true, DefaultEAPType: "tls", CertDir: "/c", ServerCertFile: "/c/s", PrivateKeyFile: "/c/k", CAFile: "/c/ca"}
				return s
			}(),
			want:    ClassRestart,
			because: "EAP",
		},
		{
			name:    "a restart and a database write at once is a restart",
			running: snapshot(ap("192.168.1.10")),
			wanted: func() Snapshot {
				s := snapshot(ap("192.168.1.10"), ap("192.168.1.11"))
				s.Config.AuthPort = 11812
				return s
			}(),
			want:    ClassRestart,
			because: "AuthPort",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.running, tc.wanted)
			if got.Class != tc.want {
				t.Fatalf("Classify = %s, want %s\nreasons: %v", got.Class, tc.want, got.Reasons)
			}
			if tc.because != "" {
				joined := strings.Join(got.Reasons, "\n")
				if !strings.Contains(joined, tc.because) {
					t.Errorf("reasons do not mention %q:\n%s", tc.because, joined)
				}
			}
			if tc.want == ClassNone && len(got.Reasons) != 0 {
				t.Errorf("a no-op has reasons: %v", got.Reasons)
			}
			if tc.want != ClassNone && len(got.Reasons) == 0 {
				t.Errorf("a %s change gives the operator no reason", got.Class)
			}
		})
	}
}

// TestSQLOnlyCarriesItsDelay: "no restart" is not the same as "immediately".
// A client definition FreeRADIUS has already cached is re-read when the cache
// entry expires, so a rotation or a removal inside a dynamic network is honest
// only if it says how long it takes.
func TestSQLOnlyCarriesItsDelay(t *testing.T) {
	running := snapshot()
	running.Config.DynamicClientLifetimeSeconds = 120
	wanted := snapshot(ap("192.168.1.11"))
	wanted.Config.DynamicClientLifetimeSeconds = 120

	got := Classify(running, wanted)
	if got.Class != ClassSQLOnly {
		t.Fatalf("Classify = %s, want sql-only", got.Class)
	}
	if got.EffectiveWithin != 2*time.Minute {
		t.Errorf("EffectiveWithin = %s, want 2m0s", got.EffectiveWithin)
	}
	if !strings.Contains(got.String(), "2m0s") {
		t.Errorf("String() hides the delay: %q", got.String())
	}

	none := Classify(running, running)
	if none.EffectiveWithin != 0 {
		t.Errorf("a no-op claims a delay of %s", none.EffectiveWithin)
	}
}

// TestClassifyDoesNotCallAWideningOfTheNetworkSetFree: adding a dynamic
// network is a generated-tree change, so it cannot be sql-only even though the
// thing it enables is.
func TestClassifyDoesNotCallAWideningOfTheNetworkSetFree(t *testing.T) {
	running := snapshot()
	wanted := snapshot(ap("203.0.113.7"))
	wanted.Config.DynamicClientNetworks = append(wanted.Config.DynamicClientNetworks,
		DynamicNetwork{Name: "branch", CIDR: "203.0.113.0/24"})

	got := Classify(running, wanted)
	if got.Class != ClassRestart {
		t.Fatalf("Classify = %s, want restart; the network list is generated into clients.conf", got.Class)
	}
}

// TestDeletingADiscoveredClientIsNotNothingToDo: a client row deleted between
// restarts is still a restart.
//
// Its subject is the measurement, not the constant. `docs/protocol-reality.md`
// records, against FreeRADIUS 3.2.10: *"Immediately after the row is deleted and
// the sync has run, the running server still answers. The definition was
// discovered, so the server holds it cached. After a restart it stops."*
//
// So the scenario is built the way the measurement was: an access point that was
// **not** in the startup snapshot — it was registered after the server came up and
// admitted by discovery — and whose row is now gone. A classifier that sees it in
// neither map answers `none`, which tells an operator a deletion took effect
// while the server goes on authenticating that client.
//
// The assertion is that the answer is not `none` and that its reason names the
// client, because "the running server still answers" is a fact about a device the
// operator can point at. Which class it is, is argued separately below.
func TestDeletingADiscoveredClientIsNotNothingToDo(t *testing.T) {
	discovered := ap("192.168.1.11")

	// The server started with one client and never read the second: it was
	// written to the table afterwards and admitted by discovery.
	running := snapshot(ap("192.168.1.10"))
	wanted := snapshot(ap("192.168.1.10"))

	got := Classify(running, wanted, TableBefore([]ClientRow{ap("192.168.1.10"), discovered}))

	if got.Class == ClassNone {
		t.Fatalf("deleting a discovered client classified %s: the row is gone and the running server "+
			"is still answering it, which is what protocol-reality.md measured", got.Class)
	}
	joined := strings.Join(got.Reasons, "\n")
	if !strings.Contains(joined, discovered.NASName) {
		t.Errorf("no reason names the client that was removed:\n%s", joined)
	}
}

// TestADeletionIsARestartWhicheverWayTheClientWasAdmitted pins the class a
// deletion needs, and pins the two reasons apart.
//
// Both admission paths end at `restart`, and they get there for different reasons
// that an operator has to be able to tell apart in a support ticket:
//
//   - a client the server **loaded at startup** is in its client list and stays
//     there until it restarts;
//   - a client the server **discovered** is answered from a cached copy, and how
//     long that copy lasts is UNVERIFIED — protocol-reality.md says the expiry was
//     never waited out.
//
// The second is why this is not `sql-only`. `sql-only` promises the change is
// honoured without a restart within `EffectiveWithin`, and the only figure on offer
// is `DynamicClientLifetimeSeconds`, which is what Foyer writes into the tree
// rather than anything measured on a running server. So the test also asserts that
// no delay is claimed: naming an unmeasured delay on a revocation tells an
// operator something that is not true.
func TestADeletionIsARestartWhicheverWayTheClientWasAdmitted(t *testing.T) {
	loaded := ap("192.168.1.10")
	discovered := ap("192.168.1.11")

	t.Run("startup-loaded", func(t *testing.T) {
		got := Classify(snapshot(loaded), snapshot(), TableBefore([]ClientRow{loaded}))
		if got.Class != ClassRestart {
			t.Fatalf("Classify = %s, want restart: %v", got.Class, got.Reasons)
		}
		if !strings.Contains(strings.Join(got.Reasons, "\n"), "until it restarts") {
			t.Errorf("reasons do not name the startup client list: %v", got.Reasons)
		}
	})

	t.Run("discovered after startup", func(t *testing.T) {
		got := Classify(snapshot(loaded), snapshot(loaded), TableBefore([]ClientRow{loaded, discovered}))
		if got.Class != ClassRestart {
			t.Fatalf("Classify = %s, want restart: %v", got.Class, got.Reasons)
		}
		joined := strings.Join(got.Reasons, "\n")
		if !strings.Contains(joined, "cached") || !strings.Contains(joined, "not a revocation") {
			t.Errorf("reasons do not say the server keeps answering it: %v", got.Reasons)
		}
		if strings.Contains(joined, "until it restarts") {
			t.Errorf("the discovered case borrowed the startup client-list reason: %v", got.Reasons)
		}

		// No bound, and no number. The console copy for this deliberately names
		// neither, and a classification that quoted the configured lifetime would
		// undercut it with a figure nobody has watched elapse.
		if got.EffectiveWithin != 0 {
			t.Errorf("EffectiveWithin = %s: a deletion is not honoured within a measured window", got.EffectiveWithin)
		}
		if strings.Contains(got.String(), "effective within") {
			t.Errorf("String() promises a window: %q", got.String())
		}
		lifetime := fmt.Sprintf("%d", DefaultConfig().DynamicClientLifetimeSeconds)
		if strings.Contains(joined, lifetime) {
			t.Errorf("a reason quotes the configured lifetime %s, which protocol-reality.md marks UNVERIFIED: %v", lifetime, got.Reasons)
		}
	})
}

// TestDeletingARowTheRunningServerCouldNeverHaveHeldIsNothingToDo keeps `none`
// worth something.
//
// The fix above must not turn every deletion into a restart. A row written after
// startup that the running server could neither load nor discover — outside every
// dynamic network, or a range, which discovery never matches — authenticated
// nothing while it existed. Deleting it changes nothing about the running server
// and says so by staying silent.
//
// The admission test is asked of the **running** configuration, not the wanted one:
// discovery is a property of the tree that is loaded. Where the two differ the
// answer is `restart` anyway, from the tree comparison.
func TestDeletingARowTheRunningServerCouldNeverHaveHeldIsNothingToDo(t *testing.T) {
	cases := []struct {
		name    string
		removed ClientRow
	}{
		{"outside every dynamic network", ap("203.0.113.7")},
		{"a range, which discovery never matches", ap("192.168.7.0/24")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			running := snapshot(ap("192.168.1.10"))
			wanted := snapshot(ap("192.168.1.10"))
			got := Classify(running, wanted, TableBefore([]ClientRow{ap("192.168.1.10"), tc.removed}))
			if got.Class != ClassNone {
				t.Fatalf("Classify = %s, want none: %v", got.Class, got.Reasons)
			}
			if len(got.Reasons) != 0 {
				t.Errorf("a no-op has reasons: %v", got.Reasons)
			}
		})
	}
}

// TestTableBeforeIsNamesOnlyAndSurvivesRedaction is the trap this option could
// become.
//
// `sync-nas` classifies two already-redacted snapshots, because a secret never
// equals its digest and comparing a live snapshot against a redacted one answers
// `restart` for every client with reasons that look entirely plausible. A caller
// holding the rows it just removed has them in the clear. If TableBefore ever read
// a secret, that caller would have to remember to redact them and the failure would
// be silent — so the contract is that it reads names, and this is what says so.
func TestTableBeforeIsNamesOnlyAndSurvivesRedaction(t *testing.T) {
	loaded := ap("192.168.1.10")
	discovered := ap("192.168.1.11")

	f := StartupSnapshot{Salt: []byte("a-salt-for-this-test"), Redacted: RedactSnapshot(snapshot(loaded), []byte("a-salt-for-this-test"))}

	clear := f.Classify(snapshot(loaded), TableBefore([]ClientRow{loaded, discovered}))
	sealed := f.Classify(snapshot(loaded), TableBefore(RedactSnapshot(snapshot(loaded, discovered), f.Salt).Clients))

	if clear.Class != ClassRestart {
		t.Fatalf("cleartext rows classified %s, want restart: %v", clear.Class, clear.Reasons)
	}
	if clear.Class != sealed.Class || strings.Join(clear.Reasons, "\n") != strings.Join(sealed.Reasons, "\n") {
		t.Fatalf("redacting the rows changed the answer:\n cleartext %s %v\n redacted  %s %v",
			clear.Class, clear.Reasons, sealed.Class, sealed.Reasons)
	}
}

// TestTableBeforeDoesNotDisturbTheOtherClasses: a caller may pass the whole table
// rather than only what it removed, and the rows that are still there must not
// re-enter the comparison through the third set.
func TestTableBeforeDoesNotDisturbTheOtherClasses(t *testing.T) {
	loaded := ap("192.168.1.10")
	added := ap("192.168.1.11")

	same := Classify(snapshot(loaded), snapshot(loaded), TableBefore([]ClientRow{loaded}))
	if same.Class != ClassNone || len(same.Reasons) != 0 {
		t.Fatalf("an unchanged table classified %s: %v", same.Class, same.Reasons)
	}

	grew := Classify(snapshot(loaded), snapshot(loaded, added), TableBefore([]ClientRow{loaded}))
	if grew.Class != ClassSQLOnly {
		t.Fatalf("registering an access point classified %s: %v", grew.Class, grew.Reasons)
	}

	// An empty previous table is a claim, not a shrug: it says the table held
	// nothing, so nothing was removed. Not supplying one at all is the shrug, and
	// the two must not be the same value.
	none := Classify(snapshot(loaded), snapshot(loaded), TableBefore(nil))
	if none.Class != ClassNone {
		t.Fatalf("an empty previous table classified %s: %v", none.Class, none.Reasons)
	}
}
