package utils

import "sync"

// SafeResultsMap is a thread-safe map for managing channels
// associated with TDLib responses to requests.
type SafeResultsMap struct {
	mu   sync.RWMutex
	data map[string]chan map[string]interface{}
}

// NewSafeResultsMap creates and returns a new SafeResultsMap instance.
func NewSafeResultsMap() *SafeResultsMap {
	return &SafeResultsMap{
		data: make(map[string]chan map[string]interface{}),
	}
}

// Make creates and returns a new buffered channel for the specified key.
func (m *SafeResultsMap) Make(key string) chan map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	channel := make(chan map[string]interface{}, 1)

	m.data[key] = channel

	return channel
}

// Get retrieves the channel for the specified key.
func (m *SafeResultsMap) Get(key string) (chan map[string]interface{}, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	val, ok := m.data[key]

	return val, ok
}

// Delete removes the channel for the specified key.
//
// The channel itself is not closed because request/reply
// channels are single-use and owned by the waiting goroutine.
func (m *SafeResultsMap) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, key)
}

// SafeSend safely sends TDLib object to the channel without blocking.
//
// Since request channels are buffered with capacity 1 and are never closed,
// this operation is safe and efficient under concurrent workloads.
//
// If the channel buffer is already full, the value is discarded.
func (m *SafeResultsMap) SafeSend(
	ch chan<- map[string]interface{},
	value map[string]interface{},
) {
	select {
	case ch <- value:
	default:
	}
}

// Clear removes all channels from the SafeResultsMap.
//
// Channels are not closed because request/reply
// channels are single-use and owned by the waiting goroutine.
func (m *SafeResultsMap) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	clear(m.data)
}

// Len returns the number of active channels.
func (m *SafeResultsMap) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.data)
}
