package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/jobs"
	"github.com/kiennt048/foyer/internal/store"
)

// TestTheMaintenanceProcessRunsTheJobsAndTheDoor: `foyer maintenance`
// opens the maintenance pools, starts the scheduler, stops it, and serves the door —
// read from the syntax tree, as TestRunStartsAndStopsTheMaintenance reads run().
func TestTheMaintenanceProcessRunsTheJobsAndTheDoor(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "maintprocess.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "runMaintenance" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("maintprocess.go has no runMaintenance")
	}
	calls, deferred := map[string]bool{}, map[string]bool{}
	name := func(e ast.Expr) string {
		switch f := e.(type) {
		case *ast.Ident:
			return f.Name
		case *ast.SelectorExpr:
			if x, ok := f.X.(*ast.Ident); ok {
				return x.Name + "." + f.Sel.Name
			}
		}
		return ""
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.DeferStmt:
			deferred[name(v.Call.Fun)] = true
		case *ast.CallExpr:
			calls[name(v.Fun)] = true
		}
		return true
	})
	for _, c := range []string{"cfg.MaintenanceProcessError", "openMaintenance", "maint.Start", "maintenanceDoor", "s.serve"} {
		if !calls[c] {
			t.Errorf("runMaintenance does not call %s", c)
		}
	}
	if !deferred["maint.Stop"] {
		t.Error("runMaintenance does not defer maint.Stop()")
	}
}

// TestMaintenanceForChoosesByTheURL: with FOYER_MAINTENANCE_URL the control plane
// opens no maintenance pool and hands the adapter the door's client; without it the
// jobs are this process's and the settings callback reaches its own scheduler.
func TestMaintenanceForChoosesByTheURL(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef"
	ctx := context.Background()

	maint, eraser, changed, err := maintenanceFor(ctx, config.Config{
		MaintenanceURL: "http://maintenance:8090", MaintenanceToken: config.SecretValue{Value: tok},
		// Would be opened if the remote branch were not taken; it cannot be.
		ReaperDatabase: config.SecretValue{Value: "postgres://nobody@127.0.0.1:1/x?connect_timeout=1"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if maint != nil {
		t.Fatal("with FOYER_MAINTENANCE_URL set the control plane built its own scheduler and pools")
	}
	if _, ok := eraser.(*jobs.RemoteClient); !ok || changed == nil {
		t.Fatalf("with FOYER_MAINTENANCE_URL set the eraser is %T, want the maintenance door's client", eraser)
	}

	maint, eraser, changed, err = maintenanceFor(ctx, config.Config{
		DatabaseURL: "postgres://nobody@127.0.0.1:1/x?connect_timeout=1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer maint.Stop()
	if maint == nil || eraser != nil {
		t.Fatalf("in-process: maint=%v eraser=%T; want a scheduler and no eraser (the retention pool cannot open here)", maint != nil, eraser)
	}
	v := store.DefaultMaintenance()
	v.IntervalSeconds = 7200
	changed(v)
	if got := maint.settings.Current().IntervalSeconds; got != 7200 {
		t.Fatalf("the settings callback did not reach this process's scheduler: interval %d", got)
	}
}

// TestTheDoorWithNoRetentionConnectionSaysSo: a maintenance container whose retention
// pool did not open answers an erasure with that reason, never a panic on a nil
// connection — the typed-nil case the wiring guards against.
func TestTheDoorWithNoRetentionConnectionSaysSo(t *testing.T) {
	m := &maintenance{settings: jobs.NewSettings(nil, nil)}
	m.sched = buildScheduler(jobs.Deps{Settings: m.settings}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef"
	h, err := maintenanceDoor(config.Config{MaintenanceToken: config.SecretValue{Value: tok}}, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, jobs.MaintenanceErasePath, strings.NewReader(`{"kind":"mac","value":"aa:bb:cc:dd:ee:ff"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "retention connection") {
		t.Fatalf("an erasure with no retention connection answered %d %s", rec.Code, rec.Body.String())
	}
}
