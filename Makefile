.POSIX:

build:
	go build -o ps3top .

test:
	go test ./...

vet:
	go vet ./...

install:
	go install .

clean:
	rm -f ps3top

.PHONY: build test vet install clean
