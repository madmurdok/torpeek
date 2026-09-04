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

# Downloads the bundled ffmpeg/ffprobe named in third_party/ffmpeg.lock and
# verifies every checksum in it. Nothing it writes is in git (.gitignore
# carries /third_party/ffmpeg/), and it is a no-op once the binaries are there
# and still match.
#
# Exits non-zero for a platform the lock marks blocked. Nothing is blocked
# today: since TOR-93 all four platforms have a bundled build, an LGPL one on
# Linux and Windows and a GPL one on macOS. See docs/licensing.md.
.PHONY: ffmpeg
ffmpeg:
	./scripts/fetch-ffmpeg.sh

# The release archives: one folder per platform holding torpeek, ffmpeg,
# ffprobe and the licence material that has to travel with them (TOR-26).
#
# Depends on cross rather than rebuilding, because cross is the build whose
# flags were measured: CGO_ENABLED=0, and so bbolt rather than sqlite for the
# piece-completion store (TOR-59). Packaging must ship that binary, not a
# differently configured one.
#
# Fails at the end if any platform could not be packaged, so an incomplete
# release cannot be mistaken for a whole one.
.PHONY: archives
archives: cross
	./scripts/package.sh

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

# Fifty-one branches whose work is already in main make a branch listing
# unreadable and hide the three that are actually live (TOR-89). Only the ref
# is deleted: every commit on them is reachable from main and from the release
# tags, so nothing here loses work.
#
# release-* is kept on purpose. origin carries one branch per shipped release,
# and a release branch's tip is the only independent witness a tag could be
# checked against - the v0.1.0..v0.6.0 tags were backfilled after the fact
# (TOR-85), so "the tag says so" is not by itself evidence of where a release
# was cut.
#
# Local refs only. `--merged main` is the load-bearing guard, not the `-d`:
# measured, `git branch -d` judges "fully merged" against the CURRENT HEAD, so
# run from a release branch it will happily delete a branch main has never
# seen. What `-d` does add is refusing a branch a worktree has checked out -
# the same "an agent still working keeps it" protection prune-worktrees leans
# on, rather than a keep-list this target would have to be told about. origin
# carries no feature branches at all, so there is deliberately nothing remote
# to prune.
.PHONY: prune-branches
prune-branches:
	@git branch --merged main --format='%(refname:short)' \
		| grep -vxE 'main|release-.*' \
		| while read -r b; do git branch -d "$$b"; done

# The tag's version is derived from the code, never passed in: a tag that
# disagrees with internal/version is how a binary comes to claim a release
# nobody shipped. NAME is the release's own name, for the annotation - the
# tracker holds it, so it is the one thing this cannot derive.
#
# Refuses a dirty tree and an existing tag rather than asking. See
# RELEASING.md for where this sits in the ritual.
.PHONY: tag
tag:
	@v=$$(sed -n 's/^const Version = "\(.*\)"$$/\1/p' internal/version/version.go); \
	if [ -z "$$v" ]; then echo "cannot read Version from internal/version/version.go" >&2; exit 1; fi; \
	if [ -n "$$(git status --porcelain)" ]; then echo "working tree is dirty; tag what is committed" >&2; exit 1; fi; \
	if git rev-parse -q --verify "refs/tags/v$$v" >/dev/null; then echo "v$$v already exists" >&2; exit 1; fi; \
	msg="torpeek $$v"; \
	if [ -n "$(NAME)" ]; then msg="$$msg - $(NAME)"; fi; \
	git tag -a "v$$v" -m "$$msg" && echo "tagged v$$v at $$(git rev-parse --short HEAD)"

.PHONY: clean
clean:
	rm -rf bin dist
