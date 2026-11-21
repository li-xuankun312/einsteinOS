package claudeweb

// eager_exec.go — a tool call runs on this machine as soon as its input has streamed in.
//
// The local replay used to start only after the whole round had streamed: 11/04 chunk 4's
// first round streamed for 9m37s while this machine waited, and dev unit 1's two builds ran
// one after the other only after its round had ended. Each tool call is now handed to one
// worker the moment its block closes; the worker runs them in order, exactly as the replay
// did, and the round's end only waits for what is still running.

import "sync"

type eagerRunner struct {
	mu      sync.Mutex
	results map[string]string
	done    map[string]chan struct{}
	queue   chan eagerJob
	run     func(name, input string) string
}

type eagerJob struct{ id, name, input string }

func newEagerRunner(run func(name, input string) string) *eagerRunner {
	r := &eagerRunner{results: map[string]string{}, done: map[string]chan struct{}{}, queue: make(chan eagerJob, 256), run: run}
	go func() {
		for j := range r.queue {
			res := r.run(j.name, j.input)
			r.mu.Lock()
			r.results[j.id] = res
			ch := r.done[j.id]
			r.mu.Unlock()
			close(ch)
		}
	}()
	return r
}

// submit starts a tool call (in order behind the ones before it).
func (r *eagerRunner) submit(id, name, input string) {
	r.mu.Lock()
	if _, dup := r.done[id]; dup {
		r.mu.Unlock()
		return
	}
	r.done[id] = make(chan struct{})
	r.mu.Unlock()
	r.queue <- eagerJob{id, name, input}
}

// result waits for a submitted call's result; ok is false when it was never submitted.
func (r *eagerRunner) result(id string) (string, bool) {
	r.mu.Lock()
	ch, ok := r.done[id]
	r.mu.Unlock()
	if !ok {
		return "", false
	}
	<-ch
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.results[id], true
}

// finished returns the results of every submitted call, waiting for the ones still running.
func (r *eagerRunner) finished() map[string]string {
	r.mu.Lock()
	ids := make([]string, 0, len(r.done))
	for id := range r.done {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	out := map[string]string{}
	for _, id := range ids {
		res, _ := r.result(id)
		out[id] = res
	}
	return out
}

// close ends the worker once everything submitted has run.
func (r *eagerRunner) close() { close(r.queue) }
