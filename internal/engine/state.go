package engine

import (
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
)

// State is the monitor state from SPEC.md section 6.1.
type State string

// Monitor states.
const (
	Pending State = "PENDING"
	Up      State = "UP"
	Down    State = "DOWN"
	Paused  State = "PAUSED"
	// Deleted appears only in the event of a monitor delete. No status has
	// it.
	Deleted State = "DELETED"
)

// failuresBeforeDown is the fixed number of failed checks in a row before a
// monitor goes DOWN.
const failuresBeforeDown = 2

// Alert says which alert a transition triggers.
type Alert int

// Alert kinds.
const (
	AlertNone Alert = iota
	AlertDown
	AlertUp
	AlertCert // the TLS certificate expires within certWarnBefore
)

// transition applies one result to a state and a count of failures in a
// row. It returns the new state, the new count and the alert to send.
//
// Rules (SPEC.md section 6.2):
//   - PENDING to UP on the first success. No alert.
//   - PENDING or UP to DOWN on the second failure in a row. DOWN alert.
//   - DOWN to UP on the first success. UP alert.
func transition(cur State, fails int, ok bool) (State, int, Alert) {
	if ok {
		if cur == Down {
			return Up, 0, AlertUp
		}
		return Up, 0, AlertNone
	}
	fails++
	if cur == Down {
		return Down, fails, AlertNone
	}
	if fails >= failuresBeforeDown {
		return Down, fails, AlertDown
	}
	return cur, fails, AlertNone
}

// Status is the live view of one monitor.
type Status struct {
	State      State
	Fails      int
	Last       check.Result
	LastAt     time.Time
	CertExpiry time.Time
	CertWarned time.Time // expiry of the certificate the warning was sent for
	LastPush   time.Time // push monitors only
}

// Event is published on the hub after each result or state change.
type Event struct {
	MonitorID int64
	Prev      State
	State     State
	Result    check.Result
	At        time.Time
	Alert     Alert
}
