GO      ?= go
ARM64   := dist/cuefs-linux-arm64

.PHONY: all fmt vet test build build-arm64 fixtures clean

all: vet test build-arm64

fmt:
	gofmt -l -w .

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

build:
	$(GO) -o cuefs.exe ./cmd/cuefs

# Cross-compile for the remote test box; the local machine is Windows and the
# target is a low-spec ARM64 Linux host that must not be asked to compile.
build-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(ARM64) ./cmd/cuefs

fixtures:
	$(GO) run testdata/gen.go

clean:
	rm -rf dist
