package backend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kazie/zimon/internal/api"
)

// One Run takes the state lock for all; another waits for that, saying so,
// and can give up; once taken, Runs share it until the last lets go.
func TestLocalLock(t *testing.T) {
	gate, taken, released := make(chan struct{}), 0, 0
	l := &Local{Lock: func(ctx context.Context, _ func(string)) (func(), error) {
		select { // another zimon holds it until the gate opens
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		taken++
		return func() { released++ }, nil
	}}
	first := make(chan func(), 1)
	go func() {
		unlock, err := l.lock(context.Background(), func(api.Event) {})
		if err != nil {
			t.Error(err)
		}
		first <- unlock
	}()
	time.Sleep(50 * time.Millisecond) // the first is taking it

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var heard []string
	if _, err := l.lock(ctx, func(ev api.Event) { heard = append(heard, ev.Line) }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting behind the first: %v", err)
	}
	if len(heard) != 1 || heard[0] != "another change here" {
		t.Errorf("heard %q", heard)
	}

	close(gate)
	unlock1 := <-first
	unlock2, err := l.lock(context.Background(), func(api.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	unlock1()
	if released != 0 {
		t.Error("released while another Run holds it")
	}
	unlock2()
	if taken != 1 || released != 1 {
		t.Errorf("taken %d, released %d", taken, released)
	}
}
