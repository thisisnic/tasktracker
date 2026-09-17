.PHONY: build test vet run

build:
	go build -o tasktracker ./cmd/tasktracker

test:
	go test ./...

vet:
	go vet ./...

run: build
	./tasktracker
