package config

import (
	"strings"
	"testing"
)

const goodToken = "0123456789abcdef0123456789abcdef0123456789abcdef"

// TestAControlPlaneToldOfTheMaintenanceContainerHoldsNoMaintenanceLogin is the
// maintenance split as a startup refusal: with FOYER_MAINTENANCE_URL set the jobs run elsewhere,
// and a control plane also handed the reaper's or the retention role's URL has
// undone the split without anything saying so.
func TestAControlPlaneToldOfTheMaintenanceContainerHoldsNoMaintenanceLogin(t *testing.T) {
	base := Config{MaintenanceURL: "http://maintenance:8090", MaintenanceToken: SecretValue{Value: goodToken}}
	if err := base.MaintenanceSplitError(); err != nil {
		t.Fatalf("a clean split was refused: %v", err)
	}
	for name, c := range map[string]Config{
		"the reaper's login too":    {MaintenanceURL: base.MaintenanceURL, MaintenanceToken: base.MaintenanceToken, ReaperDatabase: SecretValue{Value: "postgres://foyer_reaper:x@db/foyer"}},
		"the retention login too":   {MaintenanceURL: base.MaintenanceURL, MaintenanceToken: base.MaintenanceToken, RetentionDatabase: SecretValue{Value: "postgres://foyer_retention:x@db/foyer"}},
		"no token to present":       {MaintenanceURL: base.MaintenanceURL},
		"an unreadable reaper file": {MaintenanceURL: base.MaintenanceURL, MaintenanceToken: base.MaintenanceToken, ReaperDatabase: SecretValue{Err: errUnreadable}},
	} {
		if err := c.MaintenanceSplitError(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Without the URL nothing changes: the lab and a developer run the jobs in-process.
	if err := (Config{ReaperDatabase: SecretValue{Value: "postgres://foyer_reaper:x@db/foyer"}}).MaintenanceSplitError(); err != nil {
		t.Fatalf("an in-process deployment was refused: %v", err)
	}
}

// TestTheMaintenanceContainerRefusesTheKEK: it unseals nothing, so it is not given
// the key that unseals everything; and it answers nobody without a real token.
func TestTheMaintenanceContainerRefusesTheKEK(t *testing.T) {
	ok := Config{DatabaseURL: "postgres://foyer_app:9f2c1e7ab04d@db:5432/foyer",
		MaintenanceToken: SecretValue{Value: goodToken}, MaintenanceAddr: "0.0.0.0:8090"}
	if err := ok.MaintenanceProcessError(); err != nil {
		t.Fatalf("a correct maintenance container was refused: %v", err)
	}
	withKEK := ok
	withKEK.KEK = "k1:AAAA"
	if err := withKEK.MaintenanceProcessError(); err == nil || !strings.Contains(err.Error(), "FOYER_KEK") {
		t.Fatalf("a maintenance container given the KEK started: %v", err)
	}
	short := ok
	short.MaintenanceToken = SecretValue{Value: "short"}
	if err := short.MaintenanceProcessError(); err == nil {
		t.Fatal("a maintenance container with a guessable token started")
	}
}

type unreadable struct{}

func (unreadable) Error() string { return "unreadable" }

var errUnreadable = unreadable{}
