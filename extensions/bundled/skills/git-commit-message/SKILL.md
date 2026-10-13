---
name: git-commit-message
description: >-
  Write Conventional Commits messages when the user requests a commit message,
  a change summary, or a description before committing.
version: 1.0.0
allowed-tools: read_file run_command
---

# Write a Git commit message

## When to use

- The user requests a commit message, change summary, or help committing.
- The user supplies `git diff` / `git status` and asks how to describe it.

## Steps

1. Inspect changes with `run_command`: `git status --porcelain` and `git diff --staged --stat`; if nothing is staged, use `git diff --stat`. Read specific files as needed.
2. Choose type: `feat` feature, `fix` bug, `refactor` behavior-preserving restructuring, `docs` documentation, `test` tests, `chore` maintenance, `build` build/dependencies, or `ci` continuous integration.
3. Choose the changed module/directory as scope, such as `store`, `web`, or `worker`; scope may be omitted across modules.
4. Write `<type>(<scope>): <summary>` in imperative form, at most 72 characters, without a final period.
5. Add a body only to explain motivation/tradeoffs, separated by a blank line; do not repeat the diff.
6. Use `BREAKING CHANGE: <detail>` for breaking changes and `Refs #123` or `Fixes #123` for related issues.

## Format

```
<type>(<scope>): <summary>

<Optional body: motivation and tradeoffs>

<Optional footer: BREAKING CHANGE / Refs>
```

## Notes

- One focused change per commit. Suggest splitting unrelated changes rather than writing a vague summary.
- Follow repository language conventions (`git log --oneline -10`); otherwise follow the user's language. Public gogosling summaries use English.
- Do not invent changes. Ask if intent is unclear.
- Do not append personal profiles or automatic attribution trailers.
