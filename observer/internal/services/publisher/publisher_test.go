package publisher

import (
	"fmt"
	"testing"
	"time"

	"github.com/rabbitmq/amqp091-go"
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

// --- isChannelError tests (Commit B) ---

func TestIsChannelError_AMQPClosedError(t *testing.T) {
	if !isChannelError(amqp091.ErrClosed) {
		t.Error("amqp091.ErrClosed must be detected as channel error")
	}
}

func TestIsChannelError_WrappedAMQPError(t *testing.T) {
	base := &amqp091.Error{Code: 504, Reason: "channel/connection is not open"}
	err := fmt.Errorf("publish failed: %w", base)
	if !isChannelError(err) {
		t.Error("wrapped *amqp091.Error must be detected as channel error")
	}
}

func TestIsChannelError_GenericError(t *testing.T) {
	if isChannelError(fmt.Errorf("network timeout")) {
		t.Error("generic error must NOT be channel error")
	}
}

func TestIsChannelError_Nil(t *testing.T) {
	if isChannelError(nil) {
		t.Error("nil must NOT be channel error")
	}
}
