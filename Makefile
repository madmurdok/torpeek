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

# TOR-46 keeps an agent's worktree out of git; it does not remove one. This
# does, and only the ones that are finished: a worktree whose branch is
# already merged into main has nothing left to hand back. `git worktree
# prune` cannot do this - it only forgets entries whose directory a human
# already deleted, so a worktree left in place stays listed forever.
#
# Never forces. `git worktree remove` refuses a worktree with uncommitted
# changes, which is exactly the protection wanted: an agent still working in
# one keeps it, and says so.
.PHONY: prune-worktrees
prune-worktrees:
	@for dir in .claude/worktrees/*; do \
		[ -d "$$dir" ] || continue; \
		branch=$$(git -C "$$dir" branch --show-current); \
		if git branch --merged main | grep -qx "[ +*]*$$branch"; then \
			echo "removing $$dir ($$branch, merged)"; \
			git worktree remove "$$dir" || echo "  kept: $$dir has uncommitted work"; \
		else \
			echo "keeping  $$dir ($$branch, not merged)"; \
		fi; \
	done

.PHONY: clean
clean:
	rm -rf bin dist
