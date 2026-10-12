# Public source cleanup

The public source snapshot uses the Gogosling.ca project identity and the organization module path `github.com/GoGoslingCA/gogosling`.

Comparison documents and their data-generation assets were removed. Mixed implementation reports retain gogosling's own behavior and validation, with obsolete reference sections and links removed. Source comments describe the implemented behavior directly. Public documentation no longer contains personal workstation paths.

The Go module, internal imports, dependency-check prefix, and linker package path were migrated together. No dependencies were added or removed. Local HTTP routes, persisted data, permissions, and application behavior remain compatible; Go source consumers must use the organization import path.

Publication commits use the project name without personal contact details or automatic tool attribution. A private recovery bundle was created outside the repository before consolidating the public source history. GitHub-managed cached views are separate from the branch's reachable history.

## Validation

```text
test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...
  Passed, including architecture dependency rules with the new module path.
make build && make e2e
  Passed; real Python worker and local fake-model approval flow.
npm run typecheck && npm test && npm run build
  Passed: 231 desktop tests and 368 Web tests.
(cd worker && uv sync --frozen && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest)
  Passed: 181 worker tests.
```

Tracked filenames and text were scanned for the removed descriptive and identity markers. Remaining Markdown links were checked for references to deleted files. Existing unrelated whitespace in the team workflow documents was excluded from the whitespace check.

Remote checks are available in [GitHub Actions](https://github.com/GoGoslingCA/gogosling/actions/workflows/ci.yml). This update is a source publication, not a signed desktop release.
