# CLAUDE.md

## Project

vexil is an open source uptime monitor written in Go. The name is always lowercase.

- Repository: https://github.com/InAtTheGeekEnd/vexil
- Go module: `github.com/InAtTheGeekEnd/vexil`
- License: MIT

`SPEC.md` is the source of truth. If a task conflicts with `SPEC.md`, stop and ask. Do not change `SPEC.md` unless the user tells you to.

Do not read all of `SPEC.md`. Read sections 1 and 2, then only the sections for the current milestone:

| Milestone | Read these sections |
|---|---|
| 1 Skeleton | 3, 4, 8, 9.1, 9.2, 13, 15 |
| 2 Design system | 10 |
| 3 Engine | 5, 6, 8 |
| 4 Dashboard and forms | 9.3, 9.5, 10.5 |
| 5 Detail page and live updates | 9.4, 9.6, 10.5 |
| 6 Notifications | 7 |
| 7 Status page and white label | 11, 12 |
| 8 Release | 14, 16, 17, 19 |

## The two goals

1. **Simplicity wins every conflict.** Before you add code, a setting, a dependency or a feature, ask: "Can the user get this result without it?" If yes, do not add it.
2. **Beauty.** Every screen must look finished in light and dark themes, on desktop and on a phone. This includes empty states, errors and the login page.

## Hard rules

- Build only what `SPEC.md` describes. Section 2 lists the non-goals. Do not build them.
- Do not add a setting. If you think a setting is necessary, ask first.
- Use the standard library first. Maximum five direct dependencies. A new dependency needs the user's approval.
- No Node, npm, bundler or CSS framework. Hand-written CSS, vendored htmx, small vanilla JS.
- No CGO. The build must work with `CGO_ENABLED=0`.
- All templates, CSS, JS and fonts are embedded with `embed`.
- Never hard-code the product name in templates or messages. Use the brand setting. The only place for the literal default is the brand defaults.
- Charts are server-rendered SVG. No chart library.

## Commands

```
go build ./cmd/vexil        # build
go run ./cmd/vexil          # run on :8080, data in ./data
go test -race ./...         # test (do not use -v)
go vet ./...                # vet
staticcheck ./...           # lint
gofmt -l .                  # must print nothing
```

## How to work

- Work on one milestone from `SPEC.md` section 18 at a time. Do not start the next milestone until the user approves the current one.
- Milestone 2 (design system and `/styleguide`) comes before any feature UI. Match the tokens in section 10 exactly.
- Each milestone ends with passing tests, `go vet`, `staticcheck` and `gofmt`.
- Write table-driven tests. Use `httptest` for HTTP. Use temporary SQLite files for store tests.
- Keep commits small. One logical change per commit.
- Start every commit message with a prefix: `fix:`, `perf:`, `feat:`, `docs:`, `test:` or `chore:`. The release changelog groups commits by it. Use the imperative mood after the prefix: "feat: add TCP checker".
- When you finish a task, give a short summary: what changed, how you tested it, what is still open.

## Code style

- Standard Go style. Small packages under `internal/`. See `SPEC.md` section 15 for the layout.
- Pass `context.Context` to anything that does I/O.
- Return errors. Do not panic, except at startup for invalid embedded assets.
- Use `log/slog` for logs.
- Only one goroutine writes to SQLite (see `SPEC.md` section 6.3).

## UI text

- Short sentences. Active voice. Simple words.
- Error messages say what happened and what to do next.
- Check errors are short and readable by non-experts: "HTTP 503", "connection refused", "keyword not found".

## Token use

- Read only the files the task needs. Do not scan the whole repository.
- Run `go test` without `-v`. Show only failures.
- Do not print whole files to check them. Use `grep` or read a line range.
- Do not re-read a file you already have in context unless it changed.
- Keep summaries short.

# Compact instructions

When you compact, keep: the current milestone, decisions made, files changed, failing tests. Drop: file contents, passing test output, exploration.
