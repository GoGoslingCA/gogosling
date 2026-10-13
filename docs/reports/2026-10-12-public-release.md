# Public repository and locale validation

This update prepares the repository's English public entry point and integrates the pending French language/settings work. It does not publish a signed desktop release.

## Changes

- English README, Canadian company attribution, actual development screenshots, an initial Chinese README (later retired when public docs were consolidated in English), English setup and architecture guides, roadmap, and contribution/maintenance paths.
- English issue forms for bugs, features, and performance; PR template and private security reporting instructions.
- French UI, daemon, worker, and shell messages. OpenAPI adds `fr` to the existing locale enum; no route or persistence shape changes.
- Locale normalization uses the primary language subtag, so `fr-CA` works and arbitrary words such as `french` do not masquerade as valid tags.
- Settings apply the new language only after a successful save. The success toast now uses the saved language. Failed/unsupported-language saves preserve the draft and previous active setting.

## Windows CI diagnosis

The initial CI run failed in `TestRunCRUDAndActiveListing`. The test assumed successive clock reads were distinct. Equal timestamps can occur, and the repository correctly resolves ties by ID; random IDs do not promise insertion order.

Sorting fixtures now set distinct timestamps explicitly. A separate regression inserts identical-timestamp runs in reverse ID order and checks both pagination and active-run ordering. The related agent-order fixture is also clock-independent. No production ordering, assertions, platform coverage, or CI jobs were removed.

## Local checks

Executed from the repository root with the installed Go/Node/uv tools on PATH:

```text
test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...
  Passed, including architecture dependency checks.
go test ./internal/store -run 'TestRunCRUDAndActiveListing|Test运行时间相同时按ID稳定分页|TestAgentCRUD' -count=30
  Passed all 30 repetitions.
npm run typecheck && npm test && npm run build
  Passed: 231 desktop tests and 368 Web tests.
(cd worker && uv sync --frozen && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest)
  Passed: 181 Python tests.
make e2e
  Passed with the real Python worker and a local fake model.
git diff --check
  Passed.
```

Locale tests cover key/placeholder parity, browser preference order, document language, date formatting, save-after-success, save failure, and fallback when an older daemon rejects French. Existing Chinese/English fallback defaults are retained.

A separate local demo daemon was used for browser validation and English screenshots. French settings survived a browser reload; switching back to English updated the page and save notice. This demo did not call an external model or alter the user's working daemon.

## Publication checks and limits

The public source snapshot excludes generated binaries, credentials, local configuration, dependencies, and temporary review entry points. Selected provider/GitHub/AWS key patterns and private-key blocks were checked; no actionable match was found. Pattern checks do not cover every possible sensitive value.

The English setup guide and API enum document the additive language change. The architecture inventory includes French. The repository and Go module now use the organization path `github.com/GoGoslingCA/gogosling`; clients of the Go source must use that import path. Local HTTP routes and data formats are unchanged.

Public source history is consolidated under the project identity. Earlier local recovery data is kept privately outside the repository.

Remote CI is evaluated against the pushed commit in [GitHub Actions](https://github.com/GoGoslingCA/gogosling/actions/workflows/ci.yml). Local macOS checks alone do not establish Windows/Linux success. Remaining product priorities include signed installers, real-device desktop validation, and reproducible resource benchmarks; see [the roadmap](../../ROADMAP.md).
