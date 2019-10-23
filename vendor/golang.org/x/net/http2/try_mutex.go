package http2

import "sync"

type tryMutex struct {
	ch chan struct{}
}

var _ sync.Locker = (*tryMutex)(nil)

func newTryMutex() *tryMutex {
	return &tryMutex{
		ch: make(chan struct{}, 1),
	}
}

func (m *tryMutex) TryLock() bool {
	select {
	case m.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (m *tryMutex) Lock() {
	m.ch <- struct{}{}
}

func (m *tryMutex) Unlock() {
	<-m.ch
}
