PLUGIN_NAME   = rollouts-plugin-metric-instana
BINARY_DIR    = bin
BINARY        = $(BINARY_DIR)/$(PLUGIN_NAME)

.PHONY: build test lint clean tidy

build: tidy
	mkdir -p $(BINARY_DIR)
	go build -o $(BINARY) ./...

test: tidy
	go test ./... -v -count=1

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BINARY_DIR)
