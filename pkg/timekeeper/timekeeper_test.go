package timekeeper

import (
	"testing"
	"time"
)

func TestElapsing(t *testing.T) {
	elapse := NewElapsing()

	time.Sleep(50 * time.Millisecond)

	d1 := elapse.Report()
	if d1 <= 50*time.Millisecond {
		t.Errorf("elapse time is wrong. expect some amount > 50ms, got %s", d1)
	}

	elapse.Pause()

	time.Sleep(50 * time.Millisecond)
	elapse.Resume()
	d2 := elapse.Report()

	if d2 >= 1*time.Millisecond {
		t.Errorf("elapse time is wrong. expect some amount > 50ms, got %s", d2)
	}
}

func TestReset(t *testing.T) {
	elapse := NewElapsing()

	time.Sleep(50 * time.Millisecond)

	elapse.Reset()
	d1 := elapse.Report()
	if d1 >= 1*time.Millisecond {
		t.Errorf("elapse time is wrong. expect some amount ~1us, got %s", d1)
	}
}

func TestCarryon(t *testing.T) {
	elapse := NewElapsing()
	time.Sleep(10 * time.Millisecond)
	elapse.Pause()

	time.Sleep(10 * time.Millisecond)
	elapse.Resume()
	d1 := elapse.Report()

	if d1 >= 20*time.Millisecond {
		t.Errorf("elapse time is wrong. expect some amount ~10ms, got %s", d1)
	}
}
