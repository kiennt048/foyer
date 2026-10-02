package jobs

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// The maintenance container's door.
//
// ── why there is a second process ────────────────────────────────────────────
//
// When everything ran in one container, taking over the control plane's container
// meant holding every database login, the retention role's — which may remove audit
// entries and FreeRADIUS's rows — and the stale-session reaper's among them. The
// control plane is the process that parses every console request and every guest's
// portal form; the jobs that need those logins are a timer. PacketFence splits the
// same way — `pfcron` (formerly `pfmon`) runs its maintenance tasks as its own
// service, out of the processes that serve the portal and the admin — and so does
// this now: the same image, `foyer maintenance`, in a container that alone is given
// the reaper's and the retention role's secrets.
//
// ── the two things the control plane still needs from it ─────────────────────
//
//   - **Erasure.** `eraseDataSubject` is an administrator's request answered with
//     a receipt, and half of it runs as foyer_retention. The control plane asks
//     this door for that half, naming the subject by kind and value only; this side
//     rebuilds the subject from its own parsers and its own read of the account, so
//     a caller cannot hand it a pre-built `store.Subject` that erases more than the
//     subject it names. What a compromised control plane can still do is ask for a
//     subject's erasure — which the console already lets an administrator do, and
//     which this side logs — but not run a statement of its choosing, and not
//     touch `audit_log` at all.
//   - **"The settings changed."** An operator's maintenance edit is in force at
//     once (internal/jobs invariant 7): the control plane tells this door, which
//     re-reads the row and wakes the scheduler. A lost notification only delays the
//     edit to the next cycle, which re-reads the row anyway.
//
// **Authenticated by a bearer token** both containers are given as a docker
// secret, compared in constant time, on the internal `backplane` network that
// publishes nothing. mTLS was the alternative and would add a CA and a rotation to
// defend a door on a network with four members; the token is what keeps the other
// two (PostgreSQL has no client; FreeRADIUS does not need this) from using it.

const (
	// MaintenanceErasePath and MaintenanceSettingsPath are the door's two
	// operations.
	MaintenanceErasePath    = "/erase"
	MaintenanceSettingsPath = "/settings-changed"

	// minTokenBytes refuses a token short enough to guess: first-boot writes 48 hex
	// characters (192 bits).
	minTokenBytes = 32
	// maxRemoteBody bounds a request; a subject is a kind and one value.
	maxRemoteBody = 4 << 10
)

// ErrWeakToken is a maintenance token too short to be a secret.
var ErrWeakToken = errors.New("jobs: the maintenance token is shorter than 32 bytes")

type eraseRequest struct {
	Kind  store.SubjectKind `json:"kind"`
	Value string            `json:"value"`
}

type eraseResponse struct {
	Removed map[string]int64 `json:"removed,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// ServerConfig is what the door runs against.
type ServerConfig struct {
	Token     string
	App       *store.Store
	Retention store.RetentionEraser
	// SettingsChanged re-reads the maintenance row and wakes the scheduler.
	SettingsChanged func(context.Context)
	Logger          *slog.Logger
}

// NewServer returns the door's handler, or ErrWeakToken.
func NewServer(c ServerConfig) (http.Handler, error) {
	if len(c.Token) < minTokenBytes {
		return nil, ErrWeakToken
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+MaintenanceErasePath, func(w http.ResponseWriter, r *http.Request) {
		var req eraseRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxRemoteBody)).Decode(&req); err != nil {
			writeRemote(w, http.StatusBadRequest, eraseResponse{Error: "not an erasure request"})
			return
		}
		s, err := subjectFor(r.Context(), c.App, req)
		if err != nil {
			writeRemote(w, http.StatusUnprocessableEntity, eraseResponse{Error: err.Error()})
			return
		}
		if c.Retention == nil {
			writeRemote(w, http.StatusServiceUnavailable, eraseResponse{Error: "the retention connection is not open"})
			return
		}
		removed, err := c.Retention.EraseRetained(r.Context(), s)
		if err != nil {
			logger.Error("an erasure's retention half failed", "subject", s.Masked(), "error", err.Error())
			writeRemote(w, http.StatusInternalServerError, eraseResponse{Error: err.Error()})
			return
		}
		logger.Info("erased a data subject's retained rows at the control plane's request",
			"subject", s.Masked(), "kind", string(s.Kind), "removed", removed)
		writeRemote(w, http.StatusOK, eraseResponse{Removed: removed})
	})
	mux.HandleFunc("POST "+MaintenanceSettingsPath, func(w http.ResponseWriter, r *http.Request) {
		if c.SettingsChanged != nil {
			c.SettingsChanged(r.Context())
		}
		w.WriteHeader(http.StatusNoContent)
	})
	want := []byte("Bearer " + c.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The one unauthenticated answer: the container's healthcheck, which says
		// nothing but that the door is open.
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok\n")
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			writeRemote(w, http.StatusUnauthorized, eraseResponse{Error: "unauthorized"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRemoteBody)
		mux.ServeHTTP(w, r)
	}), nil
}

// subjectFor rebuilds the subject on this side, from the kind and one value.
func subjectFor(ctx context.Context, app *store.Store, req eraseRequest) (store.Subject, error) {
	switch req.Kind {
	case store.SubjectMAC:
		return store.MACSubject(req.Value)
	case store.SubjectPhone:
		return store.PhoneSubject(req.Value)
	case store.SubjectAccount:
		if app == nil {
			return store.Subject{}, errors.New("no application connection to read the account")
		}
		return app.AccountSubject(ctx, req.Value)
	}
	return store.Subject{}, fmt.Errorf("unknown subject kind %q", req.Kind)
}

func writeRemote(w http.ResponseWriter, status int, body eraseResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// RemoteClient is the control plane's side of the door: a `store.RetentionEraser`
// and the settings notification, over HTTP.
type RemoteClient struct {
	base   string
	token  string
	client *http.Client
}

// NewRemoteClient returns a client for the door at baseURL (`http://host:port`).
func NewRemoteClient(baseURL, token string) (*RemoteClient, error) {
	if len(token) < minTokenBytes {
		return nil, ErrWeakToken
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, fmt.Errorf("jobs: the maintenance URL %q is not http(s)", baseURL)
	}
	return &RemoteClient{base: strings.TrimRight(baseURL, "/"), token: token,
		client: &http.Client{Timeout: 5 * time.Minute}}, nil
}

// EraseRetained asks the maintenance container for the retention half of an
// erasure. The subject travels as its kind and one value, never as a built
// Subject (see above).
func (c *RemoteClient) EraseRetained(ctx context.Context, s store.Subject) (map[string]int64, error) {
	req := eraseRequest{Kind: s.Kind}
	switch s.Kind {
	case store.SubjectMAC:
		req.Value = s.MAC
	case store.SubjectAccount:
		req.Value = s.AccountID
	case store.SubjectPhone:
		if len(s.PhoneDigits) == 0 {
			return nil, errors.New("jobs: a phone subject with no number")
		}
		// The international digits reproduce the same subject on the other side:
		// store.PhoneSubject derives the national form from them.
		req.Value = "+" + s.PhoneDigits[0]
	default:
		return nil, fmt.Errorf("jobs: unknown subject kind %q", s.Kind)
	}
	body, _ := json.Marshal(req)
	var out eraseResponse
	status, err := c.post(ctx, MaintenanceErasePath, body, &out)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("jobs: the maintenance container refused the erasure (%d): %s", status, out.Error)
	}
	if out.Removed == nil {
		out.Removed = map[string]int64{}
	}
	return out.Removed, nil
}

// SettingsChanged tells the maintenance container an operator saved its settings.
// A failure is logged, not returned: the next cycle re-reads the row anyway.
func (c *RemoteClient) SettingsChanged(ctx context.Context, logger *slog.Logger) {
	status, err := c.post(ctx, MaintenanceSettingsPath, nil, nil)
	if err == nil && status != http.StatusNoContent {
		err = fmt.Errorf("status %d", status)
	}
	if err != nil && logger != nil {
		logger.Warn("the maintenance container was not told about a settings change; it takes effect on its next cycle",
			"error", err.Error())
	}
}

func (c *RemoteClient) post(ctx context.Context, path string, body []byte, out *eraseResponse) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("jobs: reaching the maintenance container: %w", err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
	}
	return resp.StatusCode, nil
}
