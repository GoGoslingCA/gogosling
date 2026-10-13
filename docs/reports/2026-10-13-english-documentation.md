# English documentation publication

The public documentation for gogosling, developed by Gogosling.ca in Canada, is consolidated in English. Contributions remain welcome from developers everywhere.

## Scope

- Converted 39 documentation files covering architecture, protocols, module plans, requirements, user instructions, development guides, and delivery records.
- Removed the duplicate localized repository README and updated the documentation index, language labels, links, and heading fragments.
- Converted 150 OpenAPI titles, summaries, and descriptions. A structural comparison confirmed that paths, fields, types, required lists, enums, defaults, and validation constraints did not change. All 57 paths and 245 local references resolve.
- Retained all 108 requirement/risk/acceptance identifiers in the team PRD and all 99 identifiers in its architecture design. Draft and historical status are explicitly retained.
- Corrected wording around approval boundaries, cancellation results, character limits, and the current one-click prompt enhancement workflow.
- Public commit summaries use English and the Gogosling.ca project identity, without contact details or automatic attribution trailers.

Application locale packs, runtime persona assets, original multilingual test records, and actual test-symbol names retain their values. These are functional data rather than untranslated documentation prose.

## Validation

```text
test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...
make e2e
  Passed, including the real worker, local fake model, and approval flow.
npm run typecheck && npm test && npm run build
  Passed: 231 desktop tests and 368 Web tests.
(cd worker && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest)
  Passed: 181 worker tests.
git diff --check
  Passed.
```

Local document targets and translated heading fragments were checked. Publication checks cover current repository-owned files and project identity; the existing repository is retained. Remote results are attached to the published revision in [GitHub Actions](https://github.com/GoGoslingCA/gogosling/actions/workflows/ci.yml).
