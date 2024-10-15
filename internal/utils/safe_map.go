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

// Make creates and returns a new channel for the specified key.
func (srm *SafeResultsMap) Make(key string) chan map[string]interface{} {
	srm.mu.Lock()
	defer srm.mu.Unlock()

	channel := make(chan map[string]interface{})
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

// Delete removes the channel for the specified key, closing it
// and optionally sending an abort message.
func (srm *SafeResultsMap) Delete(key string, send_abort bool) {
	srm.mu.Lock()
	defer srm.mu.Unlock()

	channel, exists := srm.data[key]
	if exists {
		if send_abort {
			srm.SafeSend(channel, MakeError(500, "Request aborted"))
		}

		close(channel)
		delete(srm.data, key)
	}
}

// SafeSend safely sends TDLib object to the channel, recovering if the channel is closed.
func (srm *SafeResultsMap) SafeSend(ch chan<- map[string]interface{}, value map[string]interface{}) {
	defer func() {
		recover()
	}()

	ch <- value
}

// ClearChannels closes all channels in the SafeResultsMap and deletes them.
//
// If send_abort is true, it sends TDLib "Request aborted" error to each channel before closing it.
func (srm *SafeResultsMap) ClearChannels(send_abort bool) {
	srm.mu.Lock()
	defer srm.mu.Unlock()

	keysToDelete := make([]string, 0, len(srm.data))

	for key, channel := range srm.data {
		if send_abort {
			srm.SafeSend(channel, MakeError(500, "Request aborted"))
		}

		keysToDelete = append(keysToDelete, key)
	}

	for _, key := range keysToDelete {
		close(srm.data[key])
		delete(srm.data, key)
	}
}
