package daemon

import (
	"testing"
	"time"
)

func TestWatchdogLimit(t *testing.T) {
	if got := watchdogLimit(5 * time.Second); got != 5*time.Minute {
		t.Errorf("5s tick limit = %v, want the 5m floor", got)
	}
	if got := watchdogLimit(time.Minute); got != 10*time.Minute {
		t.Errorf("1m tick limit = %v, want 10m", got)
	}
}
