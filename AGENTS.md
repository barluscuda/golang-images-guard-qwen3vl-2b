# Repository Guidelines

## Project Structure & Module Organization

This Go 1.25 module keeps executable entry points in `cmd/api`, `cmd/worker`, and `cmd/migrate`. Application code lives under `internal/`: `domain` holds core types and rules, `service` coordinates work, `port` defines interfaces, `adapter` contains HTTP, storage, worker, PostgreSQL, and llama.cpp integrations, and `repository` implements persistence. Unit tests sit beside packages; PostgreSQL integration tests are in `internal/repository`. SQL migrations belong in `migrations/`; runtime YAML and policy text are in `config/`; API examples are in `docs/`.

## Build, Test, and Development Commands

- `go test ./...` runs the unit and package test suite (also used in CI).
- `go vet ./...` checks for common Go mistakes (also used in CI).
- `go build ./cmd/api ./cmd/worker ./cmd/migrate` compiles all three commands.
- `docker build -t images-guard:local .` builds the application image.
- `GUARD_TEST_DATABASE_DSN='postgres://guard:password@127.0.0.1:5432/guard?sslmode=disable' go test ./internal/repository -v` enables repository integration tests against PostgreSQL; the tests create and drop their own schema.

For a full local stack, follow the Docker Compose steps in `README.md`. Apply schema changes through `cmd/migrate` and paired `.up.sql`/`.down.sql` files; application startup does not migrate the database.

## Coding Style & Naming Conventions

Format Go changes with `gofmt`. Use standard Go naming: exported identifiers in `PascalCase`, internal identifiers in `camelCase`, and concise package names. Keep domain code independent of infrastructure; put external system details in adapters. Name tests `*_test.go` and test functions `TestThing`. Keep configuration secrets in environment variables or an untracked `.env`, never in committed config.

## Testing Guidelines

Add focused unit tests beside changed packages and run `go test ./...` before submitting. Repository integration tests require `GUARD_TEST_DATABASE_DSN`; model-facing tests use an HTTP stub, so they do not require a live inference server. CI also runs `go vet ./...` and builds the Docker image.

## Commit & Pull Request Guidelines

Recent commits use short imperative summaries, sometimes with a conventional prefix such as `docs:`. Keep each commit focused and explain the change in its subject. Before every commit, review whether its changes require updates to `README.md`, API examples, configuration guidance, or other relevant docs. If no documentation is updated, ask the user whether they want it updated. When the user declines, include `Docs not updated (user declined).` in the commit description. Pull requests should describe behavior and motivation, list relevant verification commands, link related issues, and include request/response examples when changing the API. Call out migration or configuration changes explicitly.
