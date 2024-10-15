package utils

import (
	"sync"
)

// IdGenerator is a thread-safe generator for unique request IDs.
type IdGenerator struct {
	currentRequestID int
	mu               sync.Mutex
}

// NewIdGenerator creates and returns a new instance of IdGenerator,
func NewIdGenerator() *IdGenerator {
	return &IdGenerator{
		currentRequestID: 0,
	}
}

// GenerateID increments the current request ID and returns the new value.
//
// This method is thread-safe.
func (id *IdGenerator) GenerateID() int {
	id.mu.Lock()
	defer id.mu.Unlock()

	id.currentRequestID++
	return id.currentRequestID
}
