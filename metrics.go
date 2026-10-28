// SPDX-License-Identifier: MIT

package profinet

import "time"

// Metrics is an optional callback interface for observing the application
// relationships of a controller or a device: their state changes,
// connects, aborts, record accesses, alarms and reconnection attempts. A
// nil Metrics disables collection. The counters of each component
// (OBSERVABILITY.md) complement it.
//
// station names the peer: on a controller the device's NameOfStation, on a
// device the controller's (CMInitiatorStationName). States are the names
// the controller's State and the device's ARState print.
//
// The methods are called synchronously on the protocol path, possibly from
// several goroutines at once; implementations must be safe for concurrent
// use and must not block (increment a counter, send on a buffered
// channel). Embed [NopMetrics] to implement only the callbacks you need
// and to stay compatible when methods are added.
type Metrics interface {
	// OnStateChange is called for every state change of a controller's
	// device lifecycle or of a device's AR. err is the failure that
	// caused it, if any.
	OnStateChange(station, from, to string, err error)

	// OnConnect is called when a Connect completed: on a controller when
	// the device answered (or the call failed), on a device when it
	// answered a controller. d is the time the Connect took; err is nil
	// when the AR was established.
	OnConnect(station string, d time.Duration, err error)

	// OnAbort is called when an AR ended without a release: a watchdog,
	// an abort by the peer, a CM activity timeout, a lost link. err
	// matches [ErrAborted] or [ErrTimeout].
	OnAbort(station string, err error)

	// OnRecord is called after a record access: on a controller a
	// ReadRecord, WriteRecord or WriteMultiple call, on a device a request
	// it served. op is "read" or "write"; d the time it took; err nil when
	// it succeeded.
	OnRecord(station, op string, index uint16, d time.Duration, err error)

	// OnAlarm is called for every alarm a controller received or a device
	// sent; typ is the alarm type's name, high its priority.
	OnAlarm(station, typ string, high bool)

	// OnRetry is called when a controller schedules the next connection
	// attempt to a device after a failure: attempt counts the attempts
	// since the device last ran, wait is the backoff before it.
	OnRetry(station string, attempt int, wait time.Duration, err error)
}

// NopMetrics is a [Metrics] that does nothing. Embed it in your own type.
type NopMetrics struct{}

// OnStateChange does nothing.
func (NopMetrics) OnStateChange(string, string, string, error) {}

// OnConnect does nothing.
func (NopMetrics) OnConnect(string, time.Duration, error) {}

// OnAbort does nothing.
func (NopMetrics) OnAbort(string, error) {}

// OnRecord does nothing.
func (NopMetrics) OnRecord(string, string, uint16, time.Duration, error) {}

// OnAlarm does nothing.
func (NopMetrics) OnAlarm(string, string, bool) {}

// OnRetry does nothing.
func (NopMetrics) OnRetry(string, int, time.Duration, error) {}

// Backoff is an exponential backoff: the wait doubles from Min up to Max,
// and Jitter (0 to 1) spreads it randomly downward by up to that
// fraction, so that devices that failed together do not retry together.
type Backoff struct {
	Min, Max time.Duration
	Jitter   float64
}

// Delay returns the wait before the given attempt (1 for the first retry)
// with r, a random number in [0, 1), applied to the jitter.
func (b Backoff) Delay(attempt int, r float64) time.Duration {
	d := b.Min
	for i := 1; i < attempt && d < b.Max; i++ {
		d *= 2
	}
	d = min(d, b.Max)
	if j := min(max(b.Jitter, 0), 1); j > 0 {
		d -= time.Duration(float64(d) * j * r)
	}
	return d
}
