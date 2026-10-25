// SPDX-License-Identifier: MIT

package rt

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerPeriodic(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	var fast, slow atomic.Int64
	var mu sync.Mutex
	var times []time.Time
	tf := s.Add(2*time.Millisecond, func(now time.Time) {
		fast.Add(1)
		mu.Lock()
		times = append(times, now)
		mu.Unlock()
	})
	ts := s.Add(20*time.Millisecond, func(time.Time) { slow.Add(1) })
	if tf.Period() != 2*time.Millisecond || ts.Period() != 20*time.Millisecond {
		t.Fatal("Period")
	}
	eventually(t, "slow task runs", func() bool { return slow.Load() >= 3 })
	// The fast task ran far more often than the slow one.
	if f := fast.Load(); f < 2*slow.Load() {
		t.Fatalf("fast %d, slow %d", f, slow.Load())
	}
	// The run times passed to the task increase.
	mu.Lock()
	for i := 1; i < len(times); i++ {
		if !times[i].After(times[i-1]) {
			t.Fatalf("run %d at %v, not after %v", i, times[i], times[i-1])
		}
	}
	mu.Unlock()

	st := tf.Stats()
	if st.Cycles < 2 {
		t.Fatalf("stats %+v", st)
	}
	if st.MinInterval <= 0 || st.MaxInterval < st.MinInterval {
		t.Fatalf("intervals %+v", st)
	}
	if st.MaxLateness < st.MeanLateness || st.MeanLateness < 0 || st.MaxLateness >= tf.Period() {
		t.Fatalf("lateness %+v", st)
	}
	// The intervals are bounded below by roughly a period: deadlines are
	// start + n × period, so two runs are never much closer, unless a
	// run was late and the next one on time.
	if st.MaxInterval < tf.Period()/2 {
		t.Fatalf("max interval %v below the period", st.MaxInterval)
	}
}

func TestSchedulerFirstRunAfterOnePeriod(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	start := time.Now()
	ran := make(chan time.Time, 1)
	var once sync.Once
	s.Add(50*time.Millisecond, func(now time.Time) { once.Do(func() { ran <- now }) })
	select {
	case at := <-ran:
		if d := at.Sub(start); d < 50*time.Millisecond {
			t.Fatalf("first run after %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task never ran")
	}
}

func TestSchedulerDefaultPeriod(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	var n atomic.Int64
	task := s.Add(0, func(time.Time) { n.Add(1) })
	if task.Period() != time.Millisecond {
		t.Fatalf("period %v", task.Period())
	}
	if s.Add(-time.Second, func(time.Time) {}).Period() != time.Millisecond {
		t.Fatal("negative period")
	}
	eventually(t, "default period task", func() bool { return n.Load() >= 3 })
}

func TestSchedulerRemove(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	var a, b atomic.Int64
	ta := s.Add(time.Millisecond, func(time.Time) { a.Add(1) })
	s.Add(time.Millisecond, func(time.Time) { b.Add(1) })
	eventually(t, "both run", func() bool { return a.Load() >= 2 && b.Load() >= 2 })
	s.Remove(ta)
	// A run that was starting may still happen once.
	time.Sleep(5 * time.Millisecond)
	after := a.Load()
	bAfter := b.Load()
	eventually(t, "the other task continues", func() bool { return b.Load() >= bAfter+5 })
	if a.Load() != after {
		t.Fatalf("removed task ran %d more times", a.Load()-after)
	}
	if ta.Stats().Cycles != uint64(after) {
		t.Fatalf("cycles %d, runs %d", ta.Stats().Cycles, after)
	}
	// Removing twice is harmless.
	s.Remove(ta)
}

func TestSchedulerRemoveFromTask(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	var self atomic.Pointer[Task]
	var n atomic.Int64
	ready := make(chan struct{})
	task := s.Add(time.Millisecond, func(time.Time) {
		<-ready
		if n.Add(1) == 3 {
			s.Remove(self.Load())
		}
	})
	self.Store(task)
	close(ready)
	eventually(t, "three runs", func() bool { return n.Load() >= 3 })
	time.Sleep(20 * time.Millisecond)
	if got := n.Load(); got != 3 {
		t.Fatalf("task ran %d times after removing itself", got)
	}
	// The scheduler still runs other tasks.
	var m atomic.Int64
	s.Add(time.Millisecond, func(time.Time) { m.Add(1) })
	eventually(t, "another task", func() bool { return m.Load() >= 2 })
}

func TestSchedulerMissed(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	const period = 5 * time.Millisecond
	var n atomic.Int64
	task := s.Add(period, func(time.Time) {
		if n.Add(1) == 1 {
			// Block for several periods: the following deadlines pass.
			time.Sleep(6 * period)
		}
	})
	eventually(t, "runs after the blocking one", func() bool { return n.Load() >= 3 })
	st := task.Stats()
	if st.Missed < 3 {
		t.Fatalf("missed %d after blocking for 6 periods: %+v", st.Missed, st)
	}
	if st.MaxInterval < 5*period {
		t.Fatalf("max interval %v", st.MaxInterval)
	}
	// Lateness is kept below a period: missed deadlines are skipped, not
	// counted as jitter.
	if st.MaxLateness >= period {
		t.Fatalf("max lateness %v", st.MaxLateness)
	}
}

func TestSchedulerClose(t *testing.T) {
	s := NewScheduler()
	var n atomic.Int64
	s.Add(time.Millisecond, func(time.Time) { n.Add(1) })
	eventually(t, "runs", func() bool { return n.Load() >= 2 })
	s.Close()
	after := n.Load()
	time.Sleep(10 * time.Millisecond)
	if n.Load() != after {
		t.Fatal("task ran after Close")
	}
	s.Close() // idempotent

	// Closing an idle scheduler returns.
	idle := NewScheduler()
	done := make(chan struct{})
	go func() { idle.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close of an idle scheduler hangs")
	}
}

func TestTaskStatsZero(t *testing.T) {
	var task Task
	if st := task.Stats(); st != (TaskStats{}) {
		t.Fatalf("zero stats %+v", st)
	}
	// record computes intervals from the second run.
	base := time.Now()
	task.record(base, time.Millisecond, 0)
	task.record(base.Add(10*time.Millisecond), 3*time.Millisecond, 2)
	task.record(base.Add(14*time.Millisecond), 0, 0)
	st := task.Stats()
	want := TaskStats{Cycles: 3, Missed: 2, MaxLateness: 3 * time.Millisecond, MeanLateness: 4 * time.Millisecond / 3,
		MinInterval: 4 * time.Millisecond, MaxInterval: 10 * time.Millisecond}
	if st != want {
		t.Fatalf("stats\n got %+v\nwant %+v", st, want)
	}
}

func TestSchedulerManyTasksConcurrent(t *testing.T) {
	s := NewScheduler()
	defer s.Close()
	var wg sync.WaitGroup
	var total atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				task := s.Add(time.Duration(1+j%3)*time.Millisecond, func(time.Time) { total.Add(1) })
				time.Sleep(time.Millisecond)
				s.Remove(task)
			}
		}()
	}
	wg.Wait()
	// Every task is gone: no further runs after a grace period.
	time.Sleep(5 * time.Millisecond)
	after := total.Load()
	time.Sleep(20 * time.Millisecond)
	if total.Load() != after {
		t.Fatalf("removed tasks still run: %d -> %d", after, total.Load())
	}
}
