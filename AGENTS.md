# Go Development Guidelines

## Build/Test Commands

- Build: `cd neutrino_server && go build ./cmd/neutrinod`
- Run all tests: `cd neutrino_server && go test ./...`
- Run single test: `cd neutrino_server && go test -v -run TestHandleGetStatus ./internal/api`
- Run with coverage: `cd neutrino_server && go test -v -race -coverprofile=coverage.out ./...`
- Format code: `cd neutrino_server && go fmt ./...`
- Lint: `cd neutrino_server && go vet ./...`
- Pre-commit: `prek run --all-files` (use prek, NOT pre-commit for local development)

## Code Style

- **Imports**: Group stdlib, external, internal packages with blank lines between groups
- **Types**: Use `any` instead of `interface{}` (Go 1.18+)
- **Naming**: PascalCase for exported, camelCase for unexported; descriptive variable names
- **Comments**: Use godoc format - sentences starting with the name being documented
- **Error Handling**: Always check errors; use `fmt.Errorf` with `%w` for wrapping
- **Tests**: Use table-driven tests with `t.Run()` for subtests; mock interfaces for dependencies
- **Interfaces**: Define interfaces in consumer packages, not producer packages
- **Context**: Pass `context.Context` as first parameter to functions that need it
- **JSON**: Use struct tags with snake_case for JSON fields (e.g., `json:"block_height"`)

# General Guidelines

- After making changes, run all tests to ensure nothing is broken. Then run prek to format and lint the code. Finally, update CHANGELOG.md with a summary of your changes.

## Release Preparation

- Before every release, review the version constants at the top of
  `scripts/update-dependencies.sh`, verify them against primary sources, and
  run `./scripts/update-dependencies.sh`.
- Review the complete dependency diff, resolve compatibility issues, and
  update `CHANGELOG.md` before creating release artifacts.
- Run `cd neutrino_server && go test -v -race -coverprofile=coverage.out ./...`,
  `cd neutrino_server && go vet ./...`, the Docker build and integration tests,
  and `prek run --all-files` before signing a release.
- Build and sign the release digest with
  `./scripts/release-build-sign.sh <version> --key 1C53A412D11EF3051704419C44912E1E03005B31`,
  commit `signatures/<version>/`, and verify it with
  `./scripts/verify-release-build.sh <version>`.
- Create a signed release tag with
  `git tag -s <version> -m "Release <version>"`. Do not push the commit or tag
  unless the user explicitly requests it.
