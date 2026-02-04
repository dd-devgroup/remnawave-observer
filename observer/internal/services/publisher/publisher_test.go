package publisher

import (
	"testing"
	"time"
)

// slowConfirm simulates a DeferredConfirmation that responds after a delay.
type slowConfirm struct {
	delay time.Duration
	ack   bool
}

func (s *slowConfirm) Wait() bool {
	time.Sleep(s.delay)
	return s.ack
}

func TestWaitWithTimeout_Ack(t *testing.T) {
	dc := &slowConfirm{delay: 1 * time.Millisecond, ack: true}
	ack, timedOut := waitWithTimeout(dc, 1*time.Second)
	if timedOut {
		t.Fatal("unexpected timeout")
	}
	if !ack {
		t.Fatal("expected ack=true")
	}
}

func TestWaitWithTimeout_Nack(t *testing.T) {
	dc := &slowConfirm{delay: 1 * time.Millisecond, ack: false}
	ack, timedOut := waitWithTimeout(dc, 1*time.Second)
	if timedOut {
		t.Fatal("unexpected timeout")
	}
	if ack {
		t.Fatal("expected ack=false (nack)")
	}
}

func TestWaitWithTimeout_Timeout(t *testing.T) {
	dc := &slowConfirm{delay: 10 * time.Second, ack: true}
	start := time.Now()
	_, timedOut := waitWithTimeout(dc, 50*time.Millisecond)
	elapsed := time.Since(start)
	if !timedOut {
		t.Fatal("expected timeout")
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("timeout path took too long: %v", elapsed)
	}
}

func TestWaitWithTimeout_ZeroDelay(t *testing.T) {
	dc := &slowConfirm{delay: 0, ack: true}
	ack, timedOut := waitWithTimeout(dc, 100*time.Millisecond)
	if timedOut {
		t.Fatal("unexpected timeout for zero-delay confirm")
	}
	if !ack {
		t.Fatal("expected ack=true")
	}
}
