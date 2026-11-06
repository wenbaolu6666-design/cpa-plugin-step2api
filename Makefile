PLUGIN_ID := step2api
GIT_TAG := v1.0.0

.PHONY: test build clean

test:
	go vet ./...
	go test -v ./...

build:
	CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags "-s -w" -o bin/$(PLUGIN_ID).dll ./cmd/step2api-plugin

clean:
	rm -rf bin
