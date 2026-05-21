package utils

import (
	"sync/atomic"
)

type IdGenerator struct {
	currentRequestID atomic.Int64
}

func NewIdGenerator() *IdGenerator {
	return &IdGenerator{}
}

func (id *IdGenerator) GenerateID() int64 {
	return id.currentRequestID.Add(1)
}
