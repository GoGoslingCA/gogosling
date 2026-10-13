# Contributing to gogosling

[Gogosling.ca](https://gogosling.ca) is developing this project in Canada, and contributions are welcome from everywhere. **Small, focused PRs are especially welcome.** Help us advance Canada's open-source AI ecosystem together.

English is the primary language for repository documentation and commit summaries. Bug reports and discussions in Chinese or French are also welcome. Code comments follow [AGENTS.md](AGENTS.md).

## Good places to start

- **Translations:** review the English, Chinese, or French locale packs. Preserve placeholder names and run locale tests.
- **Preview fixtures:** contribute a tiny synthetic document reproducing a parser bug, with its source and expected output. Do not attach customer files.
- **Accessibility:** check keyboard navigation, focus restoration, labels, and menu dismissal.
- **Setup:** try the [quickstart](docs/development/quickstart.md) on a clean machine and fix a missing step.
- **Performance:** add a reproducible local measurement with versions, hardware, workload, and before/after results. See the [benchmark plan](ROADMAP.md#help-measure-the-difference).

Browse [good first issues](https://github.com/GoGoslingCA/gogosling/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22good%20first%20issue%22) and [help wanted](https://github.com/GoGoslingCA/gogosling/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22help%20wanted%22). If those queues are empty, choose a small task above or propose one in Discussions.

## Your first PR

1. Fork the repository and follow the quickstart.
2. Read [AGENTS.md](AGENTS.md), the [English architecture guide](docs/development/architecture-guide.md), and the dependency/safety rules in [docs/architecture.md](docs/architecture.md).
3. Open a focused PR for a bug or documentation fix. Discuss new dependencies, shared contracts, and architectural changes before implementing them. Avoid unrelated cleanup or `go mod tidy`.
4. Explain the trigger, expected behavior, fix, and validation. Add meaningful regression tests for behavior changes and screenshots for UI changes.
5. Target `main`, fix failing checks, and explain any checks you could not run. Maintainers review before merging.

Do not commit build outputs, dependencies, credentials, local configuration, raw logs, or personal data. AI-assisted work is welcome when you can explain and verify it.

## Validation

From the repository root after installing dependencies:

```bash
make lint
make test
make build
npm run build
make e2e
```

The end-to-end suite uses a real Python worker and a local fake model. It needs `uv`, not a paid key; confirm it ran instead of skipping. Run affected module tests first. CI also tests the Go core on Linux, macOS, and Windows.

Commit format: `<type>(<scope>): <English summary>`, for example `fix(preview): handle empty documents`. Public commit summaries and repository documentation use English. Describe the problem and verification clearly in your PR.

## Help maintain a module

We welcome ongoing help with document formats, desktop compatibility, provider adapters, localization, and documentation. Introduce your interest in [Discussions](https://github.com/GoGoslingCA/gogosling/discussions), or mention it in a PR. Start with one bounded change, then help reproduce issues and review follow-ups.

Maintainers grant ongoing permissions after discussion; a contribution does not automatically grant repository or release access. There is no fixed review-time commitment.

Be respectful and specific. Critique code and evidence, not people. Harassment, discrimination, personal attacks, and publishing private information are unacceptable; maintainers may moderate disruptive contributions. Report vulnerabilities through [SECURITY.md](SECURITY.md). Contributions use the existing [MIT license](LICENSE).

You do not need to take on an entire module to contribute. A small change with clear verification is a useful starting point.
