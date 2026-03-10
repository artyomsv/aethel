.PHONY: build test test-race vet cross clean

build:
	go build -o aethel ./cmd/aethel
	go build -o aetheld ./cmd/aetheld

test:
	go test ./...

test-race:
	CGO_ENABLED=1 go test -race ./...

vet:
	go vet ./...

cross:
	GOOS=linux   GOARCH=amd64 go build -o dist/aethel-linux-amd64      ./cmd/aethel
	GOOS=linux   GOARCH=amd64 go build -o dist/aetheld-linux-amd64     ./cmd/aetheld
	GOOS=linux   GOARCH=arm64 go build -o dist/aethel-linux-arm64      ./cmd/aethel
	GOOS=linux   GOARCH=arm64 go build -o dist/aetheld-linux-arm64     ./cmd/aetheld
	GOOS=darwin  GOARCH=amd64 go build -o dist/aethel-darwin-amd64     ./cmd/aethel
	GOOS=darwin  GOARCH=amd64 go build -o dist/aetheld-darwin-amd64    ./cmd/aetheld
	GOOS=darwin  GOARCH=arm64 go build -o dist/aethel-darwin-arm64     ./cmd/aethel
	GOOS=darwin  GOARCH=arm64 go build -o dist/aetheld-darwin-arm64    ./cmd/aetheld
	GOOS=windows GOARCH=amd64 go build -o dist/aethel-windows-amd64.exe  ./cmd/aethel
	GOOS=windows GOARCH=amd64 go build -o dist/aetheld-windows-amd64.exe ./cmd/aetheld

clean:
	rm -f aethel aetheld aethel.exe aetheld.exe
	rm -rf dist/
