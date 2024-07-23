package timekeeper

import (
	"fmt"
	"time"
)

type ElapsingStatus int

const (
	Running ElapsingStatus = 1
	Pause   ElapsingStatus = 2
)

type Elapsing struct {
	checkpoint time.Time

	carryOn time.Duration

	status ElapsingStatus
}

func NewElapsing() *Elapsing {
	elapse := &Elapsing{
		// In Go, Now keeps track both of wallclock and monotonic clock
		// therefore we can use it to check delta as well
		checkpoint: time.Now(),

		status: Running,
	}

	return elapse
}

func (e *Elapsing) Pause() error {
	if e.status == Pause {
		return fmt.Errorf("elapsing is pause already")
	}

	e.carryOn = e.Report()
	e.status = Pause

	return nil
}

func (e *Elapsing) Resume() error {
	if e.status != Pause {
		return fmt.Errorf("elapsing is not pause")
	}

	e.checkpoint = time.Now()
	e.status = Running

	return nil
}

func (e *Elapsing) Reset() error {
	e.status = Running
	e.carryOn = 0
	e.checkpoint = time.Now()

	return nil
}

func (e *Elapsing) Report() time.Duration {
	if e.status == Pause {
		return time.Duration(0)
	}

	now := time.Now()
	total := now.Sub(e.checkpoint) + e.carryOn

	e.carryOn = time.Duration(0)
	e.checkpoint = now

	return total
}
