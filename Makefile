.PHONY: build web install test vet run check

build: web
	go build -o tasktracker ./cmd/tasktracker

# Build the web app and install the binary where go install puts
# binaries (GOBIN, else GOPATH/bin), so that `tasktracker` on its own
# serves the UI.
install: web
	go install ./cmd/tasktracker

# The Svelte app is built into internal/server/dist and embedded in the
# binary. Needs bun.
web:
	cd web && bun install --frozen-lockfile && bun run build

test:
	go test ./...

vet:
	go vet ./...

# Everything CI runs.
check: vet test
	test -z "$$(gofmt -l .)"
	go mod tidy && git diff --exit-code go.mod go.sum
	cd web && bun run check && bun run test

run: build
	./tasktracker
