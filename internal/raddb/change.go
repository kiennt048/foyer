package raddb

import (
	"fmt"
	"sort"
	"time"
)

// Class is what applying a change costs. It is shown to the operator before
// they commit, because the three cost the same to type and do not cost the same
// to the site.
type Class string

const (
	// ClassNone: nothing to apply. Neither the generated tree nor the client
	// table differs from what the server is running.
	ClassNone Class = "none"

	// ClassSQLOnly: a database write, no restart, no packet lost. This is what
	// registering a new access point must be.
	ClassSQLOnly Class = "sql-only"

	// ClassRestart: the generated tree changes, or a client the running server
	// loaded at startup changes. Restarting drops every in-flight EAP
	// conversation and the in-memory TLS session cache with it, so every
	// 802.1X supplicant on site re-authenticates. SIGHUP is not an escape:
	// it does not reliably reload clients, listeners or virtual servers in
	// 3.2.x.
	ClassRestart Class = "restart"
)

// ClientRow is one row of the SQL client table, in the six columns rlm_sql
// actually reads. It is deliberately not the Foyer object model: an access
// point is a Foyer noun, and this is what FreeRADIUS sees.
type ClientRow struct {
	NASName   string // IP address; the client identifier
	Shortname string
	Type      string
	Secret    string
	Server    string // virtual server, usually empty
}

// Snapshot is the pair of things a running FreeRADIUS depends on: the
// generated tree, and the client rows it read at startup.
//
// The distinction between "the rows it read at startup" and "the rows in the
// table now" is the whole of the classification. A row the running server has
// never seen can be discovered without a restart. A row it loaded at startup
// is in its client list already, and changing the table does not change that
// copy.
type Snapshot struct {
	Config Config
	// Clients is the client table as it stood. In the running snapshot these
	// are the rows the server loaded at startup.
	Clients []ClientRow
}

// Change is the answer, with the reasons that produced it.
type Change struct {
	Class Class
	// Reasons is one line per thing that differs, in a stable order.
	Reasons []string
	// EffectiveWithin is how long a ClassSQLOnly change can take to be
	// honoured. Discovery is immediate for an address the server has not seen;
	// a definition it has already cached is only re-read when the cache entry
	// expires. Zero for the other classes.
	EffectiveWithin time.Duration
}

// String renders the class and the delay, if there is one.
func (c Change) String() string {
	if c.EffectiveWithin > 0 {
		return fmt.Sprintf("%s (effective within %s)", c.Class, c.EffectiveWithin)
	}
	return string(c.Class)
}

// ClassifyOption supplies a fact Classify cannot derive from its two snapshots.
//
// Variadic rather than a third parameter because "not supplied" and "supplied and
// empty" are opposite answers here, and the same nil-versus-empty trap already has
// a paragraph on ReadStartupSnapshot. A caller that knows nothing about the table's
// previous state passes nothing; a caller that knows it removed nothing passes an
// empty slice and gets that read back.
type ClassifyOption func(*classifyInputs)

type classifyInputs struct {
	before []ClientRow
}

// TableBefore tells Classify what the client table held before this change.
//
// **Without it a deletion can be invisible.** Classify compares two sets — the rows
// the running server loaded at startup, and the rows wanted now. A row written to
// the table *after* startup and deleted before the next restart is in neither, so
// it produces no reason and the answer is `none`: the operator is told the deletion
// took effect. Measured on 3.2.10 (`docs/protocol-reality.md`, "Removing a client
// is not revoking it"), it has not — the running server discovered that client from
// the table and keeps answering it from the definition it cached.
//
// Only rows absent from the wanted set are consulted, so a caller that knows
// exactly which rows it removed may pass those alone rather than the whole table.
//
// **Names only.** Nothing here reads a secret, which is what makes it safe to pass
// live rows into a comparison between two redacted snapshots. Redacting them would
// change no answer; leaving them unredacted must therefore not be read as a licence
// to compare a secret against a digest anywhere else in this file.
func TableBefore(rows []ClientRow) ClassifyOption {
	return func(in *classifyInputs) { in.before = rows }
}

// Classify compares what the server is running against what is wanted.
//
// running.Clients are the rows the running server loaded at startup; the
// generated tree it is running is running.Config.
func Classify(running, wanted Snapshot, opts ...ClassifyOption) Change {
	var in classifyInputs
	for _, o := range opts {
		o(&in)
	}

	var restart, sqlOnly []string

	if !configsEqual(running.Config, wanted.Config) {
		for _, d := range configDiffs(running.Config, wanted.Config) {
			restart = append(restart, d)
		}
	}

	oldByName := indexClients(running.Clients)
	newByName := indexClients(wanted.Clients)
	beforeByName := indexClients(in.before)

	// Three sets, not two, and the third is the whole point. A name in the
	// startup snapshot or in the wanted table is one the two-set comparison can
	// see. A name that is only in the table's *previous* state is a row that
	// appeared and went again between two restarts, and it is invisible to the
	// other two by construction.
	names := map[string]bool{}
	for n := range oldByName {
		names[n] = true
	}
	for n := range newByName {
		names[n] = true
	}
	for n := range beforeByName {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	for _, name := range sorted {
		before, wasLoaded := oldByName[name]
		after, wantedNow := newByName[name]

		switch {
		case wasLoaded && wantedNow && before == after:
			// unchanged

		case !wasLoaded && wantedNow:
			// A client the running server has never seen. Discovery handles it
			// if it is one address inside a dynamic network; otherwise the only
			// way in is the startup load, which means a restart.
			switch {
			case !isSingleAddress(name):
				// A range covering a fleet — Aruba's dynamic-radius-proxy shape.
				// Discovery keys on an exact match against Packet-Src-IP-Address,
				// so a range is never what the lookup finds, whatever networks
				// are configured.
				restart = append(restart, fmt.Sprintf("client %s is a range, not an address; discovery matches one source address at a time, so only the startup load can admit it", name))
			case inDynamicNetwork(wanted.Config, name):
				sqlOnly = append(sqlOnly, fmt.Sprintf("client %s added; discovered from the client table", name))
			default:
				restart = append(restart, fmt.Sprintf("client %s added outside every dynamic network; only the startup load can admit it", name))
			}

		case wasLoaded && !wantedNow:
			restart = append(restart, fmt.Sprintf("client %s removed; the running server holds it in its client list until it restarts", name))

		case !wasLoaded && !wantedNow:
			// Reachable only when TableBefore named it: a row that was in the
			// client table, is not any more, and that the running server never
			// read at startup.
			//
			// Whether the server ever held it turns on the same admission test as
			// an addition, asked of the configuration the *running* server has
			// rather than the wanted one — discovery is a property of the tree
			// that is loaded, not of the tree being proposed. A row the running
			// server could neither load nor discover authenticated nothing, so
			// deleting it really is nothing to do and says nothing here.
			if !isSingleAddress(name) || !inDynamicNetwork(running.Config, name) {
				break
			}
			// It was discovered, so the running server answers it from the copy
			// it cached and the deleted row is not a revocation.
			//
			// `restart` and not `sql-only`, deliberately, and the reason is in
			// what the two classes promise. `sql-only` means the change is
			// honoured without a restart, within `EffectiveWithin`. The only
			// figure available for that here is `DynamicClientLifetimeSeconds`,
			// which is what Foyer *writes into the tree* and not something anyone
			// has watched elapse — protocol-reality.md marks the expiry
			// UNVERIFIED for exactly this case. Quoting it would turn a
			// configured intention into a promise about a running server, on a
			// revocation, which is the direction that leaves a decommissioned
			// access point authenticating guests. `restart` is the only bound
			// that was measured: "After a restart it stops."
			restart = append(restart, fmt.Sprintf(
				"client %s removed; the running server discovered it from the client table and answers it from the copy it cached, so deleting the row is not a revocation. How long that copy lasts has not been measured, and a restart is the only bound this can stand behind", name))

		default:
			restart = append(restart, fmt.Sprintf("client %s changed; the running server loaded it at startup and does not re-read it", name))
		}
	}

	switch {
	case len(restart) > 0:
		return Change{Class: ClassRestart, Reasons: append(restart, sqlOnly...)}
	case len(sqlOnly) > 0:
		return Change{
			Class:           ClassSQLOnly,
			Reasons:         sqlOnly,
			EffectiveWithin: time.Duration(wanted.Config.DynamicClientLifetimeSeconds) * time.Second,
		}
	default:
		return Change{Class: ClassNone}
	}
}

func indexClients(rows []ClientRow) map[string]ClientRow {
	m := make(map[string]ClientRow, len(rows))
	for _, r := range rows {
		m[r.NASName] = r
	}
	return m
}

// configsEqual compares the generated-tree inputs. Config holds only
// comparable fields except the network slice, which is compared element-wise.
func configsEqual(a, b Config) bool {
	return len(configDiffs(a, b)) == 0
}

func configDiffs(a, b Config) []string {
	var diffs []string
	add := func(field string, x, y any) {
		if x != y {
			diffs = append(diffs, fmt.Sprintf("%s changed; it is generated into the configuration tree", field))
		}
	}

	add("ListenIP", a.ListenIP, b.ListenIP)
	add("AuthPort", a.AuthPort, b.AuthPort)
	add("AcctPort", a.AcctPort, b.AcctPort)
	add("StatusPort", a.StatusPort, b.StatusPort)
	add("PostgresHost", a.PostgresHost, b.PostgresHost)
	add("PostgresPort", a.PostgresPort, b.PostgresPort)
	add("PostgresDB", a.PostgresDB, b.PostgresDB)
	add("PostgresUser", a.PostgresUser, b.PostgresUser)
	add("PostgresPassword", a.PostgresPassword, b.PostgresPassword)
	add("PostgresPasswordEnv", a.PostgresPasswordEnv, b.PostgresPasswordEnv)
	add("PolicyAPIURL", a.PolicyAPIURL, b.PolicyAPIURL)
	add("PolicyAPITimeoutSeconds", a.PolicyAPITimeoutSeconds, b.PolicyAPITimeoutSeconds)
	add("DefaultInterimIntervalSeconds", a.DefaultInterimIntervalSeconds, b.DefaultInterimIntervalSeconds)
	add("ClientTable", a.ClientTable, b.ClientTable)
	add("DynamicClientLifetimeSeconds", a.DynamicClientLifetimeSeconds, b.DynamicClientLifetimeSeconds)
	add("EAP", a.EAP, b.EAP)

	if len(a.DynamicClientNetworks) != len(b.DynamicClientNetworks) {
		diffs = append(diffs, "DynamicClientNetworks changed; it is generated into the configuration tree")
	} else {
		for i := range a.DynamicClientNetworks {
			if a.DynamicClientNetworks[i] != b.DynamicClientNetworks[i] {
				diffs = append(diffs, "DynamicClientNetworks changed; it is generated into the configuration tree")
				break
			}
		}
	}

	return diffs
}
