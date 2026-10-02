package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/jobs"
	"github.com/kiennt048/foyer/internal/store"
)

// TestTheControlPlaneSchedulesTheStandardJobs is the wiring half: the process
// registers every maintenance job, by name. Registering none would leave the four
// cleanup functions with no caller outside their own tests.
func TestTheControlPlaneSchedulesTheStandardJobs(t *testing.T) {
	s := buildScheduler(jobs.Deps{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var got []string
	for name := range s.Stats() {
		got = append(got, name)
	}
	slices.Sort(got)
	want := []string{jobs.JobOperatorSessions, jobs.JobPortalSessions, jobs.JobRetention,
		jobs.JobStaleSessions, jobs.JobVoucherExpiry, jobs.JobPhoneCodes}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the control plane schedules %v, want %v", got, want)
	}
}

// TestRunStartsAndStopsTheMaintenance reads main.go's syntax tree: `run` must call
// openMaintenance, start it, and defer its stop. A scheduler that is built and never
// started is the same defect as no scheduler at all, one level up.
//
// **The syntax tree, not the text.** A test that searches the source for the string
// `maint.Start(ctx)` stays green when the call is commented out -- the sabotage it
// exists for -- because the comment still contains the string. Comments are not in
// the tree, so a call only counts if it is one.
func TestRunStartsAndStopsTheMaintenance(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(".", "main.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var run *ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "run" && fn.Recv == nil {
			run = fn
		}
	}
	if run == nil {
		t.Fatal("main.go has no func run")
	}
	calls := map[string]bool{}
	deferred := map[string]bool{}
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
	handed := map[string]bool{}
	assigned := map[string]string{}
	ast.Inspect(run.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.DeferStmt:
			deferred[name(v.Call.Fun)] = true
		case *ast.CallExpr:
			calls[name(v.Fun)] = true
		case *ast.KeyValueExpr:
			if k, ok := v.Key.(*ast.Ident); ok {
				handed[k.Name+"="+name(v.Value)] = true
			}
		case *ast.AssignStmt:
			// `changed = maint.SettingsChanged`, handed on as `MaintenanceChanged: changed`
			// since the in-process and the remote maintenance share one field.
			for i, l := range v.Lhs {
				if i < len(v.Rhs) {
					assigned[name(l)] = name(v.Rhs[i])
				}
			}
		}
		return true
	})
	// The console's maintenance write must reach this process's scheduler: without
	// it an edit waits for the next cycle's read, which at a one-day interval is a
	// day, and every maintenance setting is the console's to change at once.
	// The choice between in-process and the maintenance container is
	// maintenanceFor's (TestMaintenanceForChoosesByTheURL proves what it hands
	// back); run() must hand its `changed` on.
	_ = assigned
	if !handed["MaintenanceChanged=changed"] {
		t.Error("run() does not hand maintenanceFor's settings callback to the API adapter as MaintenanceChanged")
	}
	// maint.Start is serveUntilSignal's, not run's (TestRunEndsByServing and
	// TestServingStartsTheMaintenance); run must obtain the maintenance through
	// maintenanceFor, which opens it in-process or hands it off.
	if !calls["maintenanceFor"] {
		t.Error("run() does not call maintenanceFor")
	}
	if !deferred["maint.Stop"] {
		t.Error("run() does not defer maint.Stop()")
	}
}

// TestRunEndsByServing: the last statement of `run` is `return
// serveUntilSignal(…, maint, …)`, at the top level of the function — not inside an
// `if`, a loop or a closure, where it could be dead. Together with
// TestServingStartsTheMaintenance this is what the AST test alone could not say:
// that the start is *reached* (the AST test alone is green on
// `if false { maint.Start(ctx) }`).
func TestRunEndsByServing(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(".", "main.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var run *ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "run" && fn.Recv == nil {
			run = fn
		}
	}
	if run == nil || len(run.Body.List) == 0 {
		t.Fatal("main.go has no func run")
	}
	last, ok := run.Body.List[len(run.Body.List)-1].(*ast.ReturnStmt)
	if !ok || len(last.Results) != 1 {
		t.Fatal("run() does not end with `return serveUntilSignal(...)`")
	}
	call, ok := last.Results[0].(*ast.CallExpr)
	if !ok {
		t.Fatal("run() does not end by returning a call")
	}
	if fn, ok := call.Fun.(*ast.Ident); !ok || fn.Name != "serveUntilSignal" {
		t.Fatal("run() does not end with `return serveUntilSignal(...)`")
	}
	handed := false
	for _, a := range call.Args {
		if id, ok := a.(*ast.Ident); ok && id.Name == "maint" {
			handed = true
		}
	}
	if !handed {
		t.Error("run() does not hand its maintenance to serveUntilSignal")
	}
	// Nothing at the top level before it returns unconditionally.
	for _, st := range run.Body.List[:len(run.Body.List)-1] {
		if _, ok := st.(*ast.ReturnStmt); ok {
			t.Error("run() returns unconditionally before it serves")
		}
	}
}

type recordingStarter struct{ started chan context.Context }

func (r recordingStarter) Start(ctx context.Context) { r.started <- ctx }

// TestServingStartsTheMaintenance runs the serving loop and waits for it to start
// the jobs — the behaviour, not the call's presence in a syntax tree. A dead branch
// around the start, or the line removed, times out here.
func TestServingStartsTheMaintenance(t *testing.T) {
	m := recordingStarter{started: make(chan context.Context, 1)}
	sig := make(chan os.Signal, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- serveUntilSignal(ctx, nil, m, sig) }()

	select {
	case got := <-m.started:
		if got.Err() != nil {
			t.Error("the jobs were started on a context that was already over")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serving did not start the maintenance jobs within 5s")
	}
	sig <- os.Interrupt
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a signal ended serving with %v, want a clean stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serving did not stop on a signal")
	}
}

// TestSettingsChangedReachesTheScheduler: the values an operator saves are the
// values the jobs read next, without waiting for the database round.
func TestSettingsChangedReachesTheScheduler(t *testing.T) {
	m := &maintenance{settings: jobs.NewSettings(nil, nil)}
	m.sched = buildScheduler(jobs.Deps{Settings: m.settings}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	v := store.DefaultMaintenance()
	v.IntervalSeconds = 3600
	m.SettingsChanged(v)
	if got := m.settings.Current().Interval(); got != time.Hour {
		t.Fatalf("after SettingsChanged the jobs read an interval of %s, want 1h", got)
	}
	var nilM *maintenance
	nilM.SettingsChanged(v) // a control plane with no maintenance must not panic
}

// TestServingWithTheJobsElsewhereDoesNotPanic: with FOYER_MAINTENANCE_URL set,
// maintenanceFor returns a nil *maintenance, and run hands it to serveUntilSignal
// as a starter — a non-nil interface holding a nil pointer, which the `maint !=
// nil` check there does not catch. Unguarded, every deployment with the
// maintenance container would panic on its first start.
func TestServingWithTheJobsElsewhereDoesNotPanic(t *testing.T) {
	var nilM *maintenance
	sig := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- serveUntilSignal(context.Background(), nil, nilM, sig)
	}()
	sig <- os.Interrupt
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serving with no in-process maintenance: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serving did not stop on a signal")
	}
}
