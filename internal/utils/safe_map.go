package utils

import (
	"fmt"
	"sync"
)

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
func (srm *SafeResultsMap) Make(key string) chan map[string]interface{} {
	srm.mu.Lock()
	defer srm.mu.Unlock()

	channel := make(chan map[string]interface{}, 1)

	srm.data[key] = channel

	return channel
}

// Get retrieves the channel for the specified key.
func (srm *SafeResultsMap) Get(key string) (chan map[string]interface{}, bool) {
	srm.mu.RLock()
	defer srm.mu.RUnlock()

	val, ok := srm.data[key]

	return val, ok
}

// Delete removes the channel for the specified key.
//
// The channel itself is not closed because request/reply
// channels are single-use and owned by the waiting goroutine.
func (srm *SafeResultsMap) Delete(key string) {
	srm.mu.Lock()
	defer srm.mu.Unlock()

	delete(srm.data, key)
}

// SafeSend safely sends TDLib object to the channel without blocking.
//
// Since request channels are buffered with capacity 1 and are never closed,
// this operation is safe and efficient under concurrent workloads.
//
// If the channel buffer is already full, the value is discarded.
func (srm *SafeResultsMap) SafeSend(
	ch chan<- map[string]interface{},
	value map[string]interface{},
) {
	select {
	case ch <- value:
	default:
		fmt.Println("SafeSend: channel full, discarding value")
	}
}

// Clear removes all channels from the SafeResultsMap.
//
// Channels are not closed because request/reply
// channels are single-use and owned by the waiting goroutine.
func (srm *SafeResultsMap) Clear() {
	srm.mu.Lock()
	defer srm.mu.Unlock()

	clear(srm.data)
}

// Len returns the number of active channels.
func (srm *SafeResultsMap) Len() int {
	srm.mu.RLock()
	defer srm.mu.RUnlock()

	return len(srm.data)
}
