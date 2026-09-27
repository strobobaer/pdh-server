package scheduler

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateCronSpec(t *testing.T) {
	if err := ValidateCronSpec(""); err != nil {
		t.Errorf("empty spec should be valid (no schedule), got %v", err)
	}
	if err := ValidateCronSpec("0 6 * * 1"); err != nil {
		t.Errorf("valid standard cron spec rejected: %v", err)
	}
	if err := ValidateCronSpec("not a cron expression"); err == nil {
		t.Error("expected error for invalid cron spec")
	}
}

func TestCronManagerScheduleInvalidSpec(t *testing.T) {
	m := NewCronManager()
	defer m.Stop()
	if err := m.Schedule("conn-1", "not a cron expression", func() {}); err == nil {
		t.Fatal("expected error scheduling invalid cron spec")
	}
}

func TestCronManagerScheduleReplacesExisting(t *testing.T) {
	m := NewCronManager()
	defer m.Stop()
	if err := m.Schedule("conn-1", "0 6 * * 1", func() {}); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if err := m.Schedule("conn-1", "0 7 * * 1", func() {}); err != nil {
		t.Fatalf("Schedule (replace): %v", err)
	}
	m.Unschedule("conn-1")
	m.Unschedule("conn-1") // idempotent, must not panic
}

func TestCronManagerScheduleEmptySpecUnschedules(t *testing.T) {
	m := NewCronManager()
	defer m.Stop()
	if err := m.Schedule("conn-1", "0 6 * * 1", func() {}); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if err := m.Schedule("conn-1", "", func() {}); err != nil {
		t.Fatalf("Schedule (empty spec): %v", err)
	}
	m.Unschedule("conn-1")
}

func TestIntervalManagerFiresJob(t *testing.T) {
	m := NewIntervalManager()
	defer m.StopAll()
	var calls int32
	m.Start("conn-1", 20*time.Millisecond, func() { atomic.AddInt32(&calls, 1) })
	time.Sleep(100 * time.Millisecond)
	m.Stop("conn-1")
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("expected job to fire at least once")
	}
}

func TestIntervalManagerStartIsIdempotent(t *testing.T) {
	m := NewIntervalManager()
	defer m.StopAll()
	var calls int32
	m.Start("conn-1", 20*time.Millisecond, func() { atomic.AddInt32(&calls, 1) })
	m.Start("conn-1", 5*time.Millisecond, func() { atomic.AddInt32(&calls, 1000) }) // must be ignored - already running
	time.Sleep(60 * time.Millisecond)
	m.Stop("conn-1")
	if atomic.LoadInt32(&calls) >= 1000 {
		t.Fatal("second Start() call should have been a no-op for an already-running id")
	}
}

func TestIntervalManagerStopStopsFiring(t *testing.T) {
	m := NewIntervalManager()
	var calls int32
	m.Start("conn-1", 15*time.Millisecond, func() { atomic.AddInt32(&calls, 1) })
	time.Sleep(50 * time.Millisecond)
	m.Stop("conn-1")
	after := atomic.LoadInt32(&calls)
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&calls) != after {
		t.Fatal("job continued firing after Stop()")
	}
}

func TestIntervalManagerStopAll(t *testing.T) {
	m := NewIntervalManager()
	m.Start("a", 15*time.Millisecond, func() {})
	m.Start("b", 15*time.Millisecond, func() {})
	m.StopAll()
	m.Stop("a") // must not panic calling Stop again after StopAll
}
