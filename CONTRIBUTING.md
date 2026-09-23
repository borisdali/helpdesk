# Contributing to aiHelpDesk

Thanks for considering a contribution. aiHelpDesk is a non-trivial, multi-service Go codebase — this doc exists so your first PR goes smoothly instead of bouncing on avoidable review friction.

## Before you start

1. **Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).** It's the map of the main packages, the agent processes, and how they talk to each other. Sending a PR without it is the single most common way to end up duplicating something that already exists elsewhere in the codebase, or missing a governance/audit hook the rest of the system relies on.
2. **Read [testing/README.md](testing/README.md).** It explains this project's testing strategy (unit, integration, governance, fault-injection) and where each kind of test belongs.
3. **Start small.** A missing unit test, a small bug fix, or one well-scoped feature is a better first PR than something large. If you're picking a feature rather than a bug fix, **open an issue first to coordinate** — this avoids two people independently building the same thing, or a PR landing on top of in-flight work on the current release branch.

## Branching

Unless your change is a production bug fix intended for `main`, work happens on the current release branch, not `main` directly. Check [the repo's branches](https://github.com/borisdali/helpdesk/branches) for the active `release/vX.Y.Z` branch, and see [what's already landed on it but not yet on `main`](https://github.com/borisdali/helpdesk/compare/main...release/v0.29.0) before you start, so you're not duplicating in-flight work.

## Code quality

This is the part we don't compromise on, regardless of how a PR was authored (see [AI-assisted contributions](#ai-assisted-contributions) below).

- **No spaghetti code, no duplicated logic.** If you're about to copy-paste a block instead of extracting a shared helper, don't — this codebase has an established pattern of catching exactly that in review and asking for a refactor before merge.
- **`golangci-lint run` must pass.** It already runs in CI ([`.github/workflows/golangci-lint.yml`](.github/workflows/golangci-lint.yml)) — run it locally before you push so review doesn't start with lint nits.
- **No premature abstraction, either.** Don't build a generic framework for a problem you have one instance of. Three similar lines is better than a wrong abstraction. This cuts both ways — duplication and over-engineering are both quality problems here, not just the first one.
- **Comments explain *why*, not *what*.** Well-named identifiers already say what the code does. A comment earns its place when it captures a non-obvious constraint, an invariant, or the reason a workaround exists — not a restatement of the next line.

## Building and testing locally

```bash
go build ./...
go vet ./...
go test ./...              # unit tests across the repo
make test-helm              # Helm chart template rendering (needs `helm` in PATH, no cluster)
make integration-governance  # real auditd process, full HTTP API — see testing/README.md
make integration             # broader integration suite
```

All of these must be clean before you open a PR. `go build`/`go vet`/`go test ./...` is the minimum bar — get in the habit of running it after every change, not just once at the end.

Some suites need more than a Go toolchain (a live database, Docker, or a K8s cluster) — `testing/README.md` explains which is which and how to stand up what each one needs.

## Test coverage

Current project coverage is tracked automatically by [Codecov](https://app.codecov.io/gh/borisdali/helpdesk) on every push and PR. **The rule is simple: coverage only goes up, PR by PR.** This is enforced by [`.codecov.yml`](.codecov.yml) as two separate CI checks on every PR:

- **Patch coverage** — the lines *your PR actually adds or changes* must be well-tested (currently 80%). This judges your new code, not the historical coverage of whatever file it happens to live in.
- **Project coverage** — total repo coverage must not regress from the base branch.

Both show up directly on your PR (inline diff comments + a status check) whether or not they're configured as merge-blocking in this repo's branch protection at any given time — treat them as real gates either way, not just informational.

If you're looking for a first contribution and don't have a specific bug or feature in mind, a missing-test PR that closes a real coverage gap is always welcome — see the coverage report linked above for where the gaps actually are.

## AI-assisted contributions

Use whatever coding assistant you like. This project is built with heavy AI assistance itself, so we're not going to pretend otherwise. But:

- **A human — exactly one — is responsible for the PR's content.** An assistant can write 100% of the code, the tests, and even the commit message. That doesn't change who's accountable for it.
- **Review it like you'd be paged for it, because you might be.** Before submitting, verify the assistant's claims against the actual code and actual test output — not against how plausible the diff reads. This project's entire premise is catching an AI that confidently states something it never actually verified; hold your own AI-assisted PR to the same standard you'd want an on-call agent held to. If a claim in your PR description isn't something you personally checked, don't include it as fact.
- **Be prepared to explain any line of it.** "The assistant wrote it that way" is not an answer to a review comment.
- **`Co-Authored-By` trailers are welcome, not required, but every commit must have a human author** — a maintainer merging the PR, not just an assistant's own commit identity.

## Submitting a PR

1. Keep it small and focused — one bug fix or one well-scoped feature, not a batch of unrelated changes.
2. Make sure `go build ./...`, `go vet ./...`, and `go test ./...` are clean, and that `golangci-lint run` passes.
3. Write a commit message that explains *why*, matching this repo's existing style — look at `git log` for the convention (`feat:`, `fix:`, `chore:`, `doc:` prefixes).
4. Open the PR against the current release branch (see [Branching](#branching) above), not `main`, unless it's a production bug fix.
5. Link the issue you coordinated on, if there was one.

Contributions big or small are genuinely welcome — just not at the cost of engineering quality. When in doubt, ask before you build.
