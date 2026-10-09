package kernel

import (
	"sync"

	. "google.golang.org/adk/v2/include"
)

type WaitQueue struct {
	mu      sync.Mutex
	waiters []*TaskStruct
	name    string
}

func NewWaitQueue(name string) *WaitQueue {
	return &WaitQueue{
		name: name,
	}
}

func (wq *WaitQueue) SleepOn() {
	if Current == nil {
		return
	}

	wq.mu.Lock()
	wq.waiters = append(wq.waiters, Current)
	wq.mu.Unlock()

	Current.State = TASK_UNINTERRUPTIBLE
	Schedule()
}

func (wq *WaitQueue) InterruptibleSleepOn() {
	if Current == nil {
		return
	}

	wq.mu.Lock()
	wq.waiters = append(wq.waiters, Current)
	wq.mu.Unlock()

	Current.State = TASK_INTERRUPTIBLE
	Schedule()
}

func (wq *WaitQueue) WakeUp() {
	wq.mu.Lock()
	defer wq.mu.Unlock()

	for _, t := range wq.waiters {
		if t != nil && t.State != TASK_RUNNING {
			t.State = TASK_RUNNING
		}
	}
	wq.waiters = wq.waiters[:0]
}

func (wq *WaitQueue) WakeUpOne() {
	wq.mu.Lock()
	defer wq.mu.Unlock()

	for i := len(wq.waiters) - 1; i >= 0; i-- {
		t := wq.waiters[i]
		if t != nil && t.State != TASK_RUNNING {
			t.State = TASK_RUNNING
			wq.waiters = append(wq.waiters[:i], wq.waiters[i+1:]...)
			return
		}
	}
}

func (wq *WaitQueue) Count() int {
	wq.mu.Lock()
	defer wq.mu.Unlock()
	return len(wq.waiters)
}

func (wq *WaitQueue) Name() string {
	return wq.name
}

func (wq *WaitQueue) IsEmpty() bool {
	wq.mu.Lock()
	defer wq.mu.Unlock()
	return len(wq.waiters) == 0
}

func (wq *WaitQueue) HasWaiter(task *TaskStruct) bool {
	wq.mu.Lock()
	defer wq.mu.Unlock()
	for _, t := range wq.waiters {
		if t == task {
			return true
		}
	}
	return false
}

func (wq *WaitQueue) Remove(task *TaskStruct) {
	wq.mu.Lock()
	defer wq.mu.Unlock()
	for i, t := range wq.waiters {
		if t == task {
			wq.waiters = append(wq.waiters[:i], wq.waiters[i+1:]...)
			return
		}
	}
}

func (wq *WaitQueue) WakeUpInterruptible() {
	wq.mu.Lock()
	defer wq.mu.Unlock()

	remaining := wq.waiters[:0]
	for _, t := range wq.waiters {
		if t != nil && t.State == TASK_INTERRUPTIBLE {
			t.State = TASK_RUNNING
		} else {
			remaining = append(remaining, t)
		}
	}
	wq.waiters = remaining
}

var (
	bufferWait    = NewWaitQueue("buffer_wait")
	inodeWait     = NewWaitQueue("inode_wait")
	superWait     = NewWaitQueue("super_wait")
	keyboardWait  = NewWaitQueue("keyboard_wait")
	hdWait        = NewWaitQueue("hd_wait")
)

func GetBufferWait() *WaitQueue   { return bufferWait }
func GetInodeWait() *WaitQueue    { return inodeWait }
func GetSuperWait() *WaitQueue    { return superWait }
func GetKeyboardWait() *WaitQueue { return keyboardWait }
func GetHdWait() *WaitQueue       { return hdWait }
