.PHONY: build clean install
.DEFAULT_GOAL := build

EXECUTABLE := $(shell pwd)/bin/tdlib-server
SRC := ./cmd/tdlib-server/tdlib-server.go
INSTALL_DIR := /usr/local/bin
LINK := $(INSTALL_DIR)/tdlib-server

build:
	go build -o $(EXECUTABLE) $(SRC)
	@echo "\ntdlib-server installed at $(EXECUTABLE)"

build-wide:
	CGO_CFLAGS=-I/usr/local/include CGO_LDFLAGS="-Wl,-rpath=/usr/local/lib -ltdjson" go build -o $(EXECUTABLE) $(SRC)
	@echo "\ntdlib-server installed at $(EXECUTABLE)"

clean:
	rm -rf ./bin

install:
	ln -sf $(PWD)/$(EXECUTABLE) $(LINK)
	@echo "\ntdlib-server installed at $(LINK)"

help:
	@echo "Available commands:"
	@echo "  make build    - Build the tdlib-server binary"
	@echo "  make clean    - Remove the built binary and clean up"
	@echo "  make install  - Install tdlib-server system-wide"
	@echo "  make help     - Display this help message"
