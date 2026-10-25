// SPDX-License-Identifier: MIT

package rt

import (
	"container/heap"
	"sync"
	"time"
)

// TaskStats are the timing measurements of one scheduled task.
type TaskStats struct {
	// Cycles is the number of times the task ran.
	Cycles uint64
	// Missed is the number of deadlines skipped because the scheduler was
	// late by more than a period.
	Missed uint64
	// MaxLateness is the largest delay between a deadline and the run;
	// MeanLateness the average. Lateness is the scheduling jitter.
	MaxLateness  time.Duration
	MeanLateness time.Duration
	// MinInterval and MaxInterval bound the time between two runs.
	MinInterval, MaxInterval time.Duration
}

// Task is a periodic task of a [Scheduler].
type Task struct {
	period time.Duration
	fn     func(now time.Time)
	next   time.Time
	index  int

	mu      sync.Mutex
	stats   TaskStats
	sumLate time.Duration
	last    time.Time
	removed bool
}

// Period returns the task's period.
func (t *Task) Period() time.Duration { return t.period }

// Stats returns a snapshot of the task's measurements.
func (t *Task) Stats() TaskStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stats
	if s.Cycles > 0 {
		s.MeanLateness = t.sumLate / time.Duration(s.Cycles)
	}
	return s
}

func (t *Task) record(now time.Time, late time.Duration, missed uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stats.Cycles++
	t.stats.Missed += missed
	t.sumLate += late
	if late > t.stats.MaxLateness {
		t.stats.MaxLateness = late
	}
	if !t.last.IsZero() {
		iv := now.Sub(t.last)
		if t.stats.MinInterval == 0 || iv < t.stats.MinInterval {
			t.stats.MinInterval = iv
		}
		if iv > t.stats.MaxInterval {
			t.stats.MaxInterval = iv
		}
	}
	t.last = now
}

type taskHeap []*Task

func (h taskHeap) Len() int           { return len(h) }
func (h taskHeap) Less(i, j int) bool { return h[i].next.Before(h[j].next) }
func (h taskHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *taskHeap) Push(x any)        { t := x.(*Task); t.index = len(*h); *h = append(*h, t) }
func (h *taskHeap) Pop() any {
	old := *h
	t := old[len(old)-1]
	*h = old[:len(old)-1]
	t.index = -1
	return t
}

// Scheduler runs periodic tasks on one goroutine, on deadlines derived
// from the monotonic clock (start + n × period, so that lateness does not
// accumulate as drift). A run that is late by more than a period skips
// the missed deadlines and counts them. Tasks must not block: they encode
// and send a frame, or check a watchdog.
//
// A Scheduler is safe for concurrent use.
type Scheduler struct {
	mu     sync.Mutex
	tasks  taskHeap
	wake   chan struct{}
	done   chan struct{}
	exited chan struct{}
	once   sync.Once
}

// NewScheduler starts a scheduler.
func NewScheduler() *Scheduler {
	s := &Scheduler{wake: make(chan struct{}, 1), done: make(chan struct{}), exited: make(chan struct{})}
	go s.run()
	return s
}

// Add schedules fn every period, first after one period.
func (s *Scheduler) Add(period time.Duration, fn func(now time.Time)) *Task {
	if period <= 0 {
		period = time.Millisecond
	}
	t := &Task{period: period, fn: fn, next: time.Now().Add(period)}
	s.mu.Lock()
	heap.Push(&s.tasks, t)
	s.mu.Unlock()
	s.poke()
	return t
}

// Remove unschedules t. It does not wait for a run in progress, so it can
// be called from the task itself; a run that was starting while Remove was
// called may still happen once.
func (s *Scheduler) Remove(t *Task) {
	s.mu.Lock()
	if t.index >= 0 && t.index < len(s.tasks) && s.tasks[t.index] == t {
		heap.Remove(&s.tasks, t.index)
	}
	s.mu.Unlock()
	t.mu.Lock()
	t.removed = true
	t.mu.Unlock()
}

// Close stops the scheduler.
func (s *Scheduler) Close() {
	s.once.Do(func() { close(s.done) })
	<-s.exited
}

func (s *Scheduler) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) run() {
	defer close(s.exited)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		s.mu.Lock()
		var wait time.Duration = time.Hour
		if len(s.tasks) > 0 {
			wait = time.Until(s.tasks[0].next)
		}
		s.mu.Unlock()
		if wait > 0 {
			timer.Reset(wait)
			select {
			case <-timer.C:
			case <-s.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				continue
			case <-s.done:
				return
			}
		}
		select {
		case <-s.done:
			return
		default:
		}
		now := time.Now()
		s.mu.Lock()
		if len(s.tasks) == 0 || s.tasks[0].next.After(now) {
			s.mu.Unlock()
			continue
		}
		t := s.tasks[0]
		deadline := t.next
		late := now.Sub(deadline)
		var missed uint64
		if late >= t.period {
			missed = uint64(late / t.period)
			t.next = deadline.Add(time.Duration(missed+1) * t.period)
		} else {
			t.next = deadline.Add(t.period)
		}
		heap.Fix(&s.tasks, t.index)
		s.mu.Unlock()

		t.mu.Lock()
		removed := t.removed
		t.mu.Unlock()
		if removed {
			continue
		}
		t.fn(now)
		t.record(now, late%t.period, missed)
	}
}
