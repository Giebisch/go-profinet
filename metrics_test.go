// SPDX-License-Identifier: MIT

package profinet

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	b := Backoff{Min: 100 * time.Millisecond, Max: time.Second}
	for _, c := range []struct {
		attempt int
		want    time.Duration
	}{{0, 100 * time.Millisecond}, {1, 100 * time.Millisecond}, {2, 200 * time.Millisecond}, {4, 800 * time.Millisecond}, {5, time.Second}, {1000, time.Second}} {
		if got := b.Delay(c.attempt, 0.9); got != c.want {
			t.Errorf("Delay(%d) = %s, want %s", c.attempt, got, c.want)
		}
	}
	b.Jitter = 0.5
	if got := b.Delay(2, 0); got != 200*time.Millisecond {
		t.Errorf("jitter r=0: %s", got)
	}
	if got := b.Delay(2, 0.5); got != 150*time.Millisecond {
		t.Errorf("jitter r=0.5: %s", got)
	}
	b.Jitter = 7 // clamped to 1
	if got := b.Delay(2, 0.5); got != 100*time.Millisecond {
		t.Errorf("jitter clamped: %s", got)
	}
	if got := (Backoff{}).Delay(3, 0.5); got != 0 {
		t.Errorf("zero backoff: %s", got)
	}
}

func TestNopMetrics(t *testing.T) {
	var m Metrics = NopMetrics{}
	m.OnStateChange("s", "a", "b", nil)
	m.OnConnect("s", time.Millisecond, nil)
	m.OnAbort("s", ErrAborted)
	m.OnRecord("s", "read", 0x10, time.Millisecond, nil)
	m.OnAlarm("s", "Diagnosis", false)
	m.OnRetry("s", 1, time.Second, ErrTimeout)
}
