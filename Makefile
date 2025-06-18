.DEFAULT_GOAL := build

BIN := $(shell pwd)/bin/tdlib-server
SRC := ./cmd/tdlib-server/tdlib-server.go
BIN_PREFIX ?= /usr/local/bin
SYMLINK := $(BIN_PREFIX)/tdlib-server

TDLIB_DIR ?= /usr/local
TD_INC ?= $(TDLIB_DIR)/include
TD_LIB ?= $(TDLIB_DIR)/lib
STATIC_TD_LIBS := -ltdjson_static -ltdjson_private -ltdclient -ltde2e -ltdmtproto \
	-ltdactor -ltddb -ltdnet -ltdsqlite \
	-ltdapi -ltdcore -ltdutils \
	-l:libssl.a -l:libcrypto.a -l:libz.a -l:libstdc++.a \


.PHONY: build
build: check_tdlib
	CGO_LDFLAGS="-L$(TD_LIB) -Wl,-rpath=$(TD_LIB) -ltdjson" \
	CGO_CFLAGS=-I$(TD_INC) \
	go build -o $(BIN) $(SRC)
	@echo "\ntdlib-server installed at $(BIN)"

.PHONY: static
static: check_tdlib
	CGO_LDFLAGS="-L$(TD_LIB) -Wl,-rpath=$(TD_LIB) $(STATIC_TD_LIBS) -lm -ldl -static-libgcc" \
	CGO_CFLAGS=-I$(TD_INC) \
	go build -o $(BIN) $(SRC)

.PHONY: clean
clean:
	rm -rf ./bin

.PHONY: install
install:
	ln -sf $(BIN) $(SYMLINK)
	@echo "\ntdlib-server installed at $(SYMLINK)"

.PHONY: uninstall
uninstall:
	rm -f $(SYMLINK)
	@echo "\ntdlib-server uninstalled from $(SYMLINK)"

.PHONY: check_tdlib
check_tdlib:
	@if [ ! -d "$(TD_INC)" ]; then \
		echo "Error: TDLib include directory not found at $(TD_INC)."; \
		echo "Please ensure that TDLIB_DIR environment variable is set correctly."; \
		echo "Alternatively, you can set the TD_INC environment variable manually for include directory."; \
		exit 1; \
	fi

	@if [ ! -d "$(TD_LIB)" ]; then \
		echo "Error: TDLib library directory not found at $(TD_LIB)."; \
		echo "Please ensure that TDLIB_DIR environment variable is set correctly."; \
		echo "Alternatively, you can set the TD_LIB environment variable manually for library directory."; \
		exit 1; \
	fi

.PHONY: help
help:
	@echo "Available commands:"
	@echo "  make build     - Build the tdlib-server binary"
	@echo "  make static    - Build the tdlib-server binary statically linked with TDLib"
	@echo "  make clean     - Remove the built binary and clean up"
	@echo "  make install   - Install tdlib-server system-wide"
	@echo "  make uninstall - Uninstall tdlib-server system-wide"
	@echo "  make help      - Display this help message"
