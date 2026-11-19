package kernel

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Units that run side by side reach their verifications at any time; the real machine runs
// one at a time, every GPU to each.
func TestVerificationsOnTheRealMachineRunOneAtATime(t *testing.T) {
	var running, most int32
	var wg sync.WaitGroup
	for _, u := range []string{"u1", "u2", "u3"} {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			onTheRealMachine(u, func() VerifyResult {
				n := atomic.AddInt32(&running, 1)
				for {
					m := atomic.LoadInt32(&most)
					if n <= m || atomic.CompareAndSwapInt32(&most, m, n) {
						break
					}
				}
				time.Sleep(30 * time.Millisecond)
				atomic.AddInt32(&running, -1)
				return VerifyResult{}
			})
		}(u)
	}
	wg.Wait()
	if most != 1 {
		t.Fatalf("%d verifications ran at once", most)
	}
}
