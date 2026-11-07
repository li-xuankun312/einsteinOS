package claudeweb

import (
	"sync"
	"testing"
	"time"
)

// tool calls run as they arrive, one at a time and in order; the round's end waits only
// for what is still running
func TestToolCallsRunInOrderAsSoonAsTheyArrive(t *testing.T) {
	var mu sync.Mutex
	var order []string
	r := newEagerRunner(func(name, input string) string {
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		order = append(order, input)
		mu.Unlock()
		return "[pid 1 exit=0] " + input
	})
	defer r.close()
	start := time.Now()
	r.submit("a", "bash_tool", "1")
	r.submit("b", "bash_tool", "2")
	r.submit("a", "bash_tool", "1") // a duplicate id runs once
	time.Sleep(50 * time.Millisecond) // the stream goes on meanwhile
	if res, ok := r.result("b"); !ok || res != "[pid 1 exit=0] 2" {
		t.Fatalf("%q %v", res, ok)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("they ran while the stream went on, not after")
	}
	if _, ok := r.result("never"); ok {
		t.Fatal("not submitted")
	}
	if got := r.finished(); len(got) != 2 || len(order) != 2 || order[0] != "1" || order[1] != "2" {
		t.Fatalf("in order, once each: %v %v", got, order)
	}
}
