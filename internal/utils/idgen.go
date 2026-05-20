package utils

import (
	"sync"
)

// IDGenerator is a thread-safe generator for unique request IDs.
type IDGenerator struct {
	currentRequestID int
	mu               sync.Mutex
}

// NewIDGenerator creates and returns a new instance of IDGenerator,
func NewIDGenerator() *IDGenerator {
	return &IDGenerator{
		currentRequestID: 0,
	}
}

// GenerateID increments the current request ID and returns the new value.
//
// This method is thread-safe.
func (id *IDGenerator) GenerateID() int {
	id.mu.Lock()
	defer id.mu.Unlock()

	id.currentRequestID++
	return id.currentRequestID
}
