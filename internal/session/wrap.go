package session

import "time"

const (
	// MaxOctet32 is 2^32 (4 GiB), the wrap boundary for 32-bit RADIUS octet counters.
	MaxOctet32 = 4294967296
)

// SessionState represents the lifecycle status of a network session.
type SessionState string

const (
	StateOnline   SessionState = "online"
	StateOrphaned SessionState = "orphaned"
	StateUnknown  SessionState = "unknown"
	StateEnded    SessionState = "ended"
)

// Session represents a RADIUS accounting session with reconstructed totals.
type Session struct {
	ID                string       `json:"id"`
	UniqueSessionID   string       `json:"uniqueSessionId"`
	UserName          string       `json:"userName"`
	CallingStationID  string       `json:"callingStationId"` // MAC
	CalledStationID   string       `json:"calledStationId"`  // AP MAC/SSID
	NASIPAddress      string       `json:"nasIpAddress"`
	FramedIPAddress   string       `json:"framedIpAddress,omitempty"`
	State             SessionState `json:"state"`
	StartTime         time.Time    `json:"startTime"`
	LastUpdateTime    time.Time    `json:"lastUpdateTime"`
	StopTime          *time.Time   `json:"stopTime,omitempty"`
	DurationSeconds   int64        `json:"durationSeconds"`
	TerminateCause    string       `json:"terminateCause,omitempty"`
	InputOctets       int64        `json:"inputOctets"`
	OutputOctets      int64        `json:"outputOctets"`
	InputOctetsTotal  int64        `json:"inputOctetsTotal"`
	OutputOctetsTotal int64        `json:"outputOctetsTotal"`
	CountersWrapped   bool         `json:"countersWrapped"`
}

// WrapTracker maintains observed counter progression to detect wraps and compute 64-bit totals.
type WrapTracker struct {
	lastInputOctets  int64
	lastOutputOctets int64
	inputWraps       int64
	outputWraps      int64
	hasWrapped       bool
}

// NewWrapTracker creates a new tracker for a session.
func NewWrapTracker() *WrapTracker {
	return &WrapTracker{}
}

// Update records new 32-bit raw octet counters and computes reconstructed 64-bit values.
func (t *WrapTracker) Update(currentInput, currentOutput int64) (inputTotal, outputTotal int64, wrapped bool) {
	// Detect input wrap: current counter is strictly less than previous counter without session restart
	if t.lastInputOctets > 0 && currentInput < t.lastInputOctets {
		t.inputWraps++
		t.hasWrapped = true
	}
	t.lastInputOctets = currentInput

	// Detect output wrap
	if t.lastOutputOctets > 0 && currentOutput < t.lastOutputOctets {
		t.outputWraps++
		t.hasWrapped = true
	}
	t.lastOutputOctets = currentOutput

	inputTotal = (t.inputWraps * MaxOctet32) + currentInput
	outputTotal = (t.outputWraps * MaxOctet32) + currentOutput

	return inputTotal, outputTotal, t.hasWrapped
}

// ComputeTotals converts raw radacct octet counters and optional gigawords into 64-bit integers.
func ComputeTotals(inputOctets, outputOctets, inputGigawords, outputGigawords int64) (int64, int64) {
	totalInput := (inputGigawords << 32) | (inputOctets & 0xFFFFFFFF)
	totalOutput := (outputGigawords << 32) | (outputOctets & 0xFFFFFFFF)
	return totalInput, totalOutput
}
