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
