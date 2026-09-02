BINARY := torpeek
PKG    := ./cmd/torpeek

# The release archive ships one folder per platform, so every build here must
# stay CGO-free: that is what keeps cross-compilation a single command.
export CGO_ENABLED := 0

.PHONY: build
build:
	go build -o bin/$(BINARY) $(PKG)

.PHONY: cross
cross:
	GOOS=darwin  GOARCH=amd64 go build -o dist/darwin-amd64/$(BINARY)      $(PKG)
	GOOS=darwin  GOARCH=arm64 go build -o dist/darwin-arm64/$(BINARY)      $(PKG)
	GOOS=linux   GOARCH=amd64 go build -o dist/linux-amd64/$(BINARY)       $(PKG)
	GOOS=windows GOARCH=amd64 go build -o dist/windows-amd64/$(BINARY).exe $(PKG)

.PHONY: check
check:
	go vet ./...
	go test ./...

# Goes to a live public swarm, takes minutes and costs real traffic, which is
# why it is not part of check. Writes docs/results/<version>-acceptance.md.
.PHONY: acceptance
acceptance:
	go test -tags acceptance -timeout 60m -v ./acceptance/ $(ARGS)

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: clean
clean:
	rm -rf bin dist
