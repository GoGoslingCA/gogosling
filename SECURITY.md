# Security policy

gogosling is an early development project. Security fixes target current `main`; there is no long-term support release policy yet.

## Report privately

Use [GitHub private vulnerability reporting](https://github.com/GoGoslingCA/gogosling/security/advisories/new). Include revision, platform, minimal reproduction, expected boundary, and impact. Use synthetic files and redact credentials and personal data.

Do not publish exploit details in a public issue. If the private form is unavailable, open an issue requesting a private contact route **without vulnerability details**. Maintainers will arrange the next step. No fixed response time or bounty is promised.

## Boundaries worth testing

- Local HTTP authentication, Host/Origin checks, and renderer isolation.
- Workspace traversal, symlinks, approval binding, conflicts, and recovery.
- Parser limits, HTML sandboxing, archive expansion, and converter isolation.
- Credential handling in logs, events, database, workers, and extensions.

Local-first is not offline-only: configured models and tools may receive data or use external services. Test only authorized systems and data. See the [architecture invariants](docs/architecture.md#6-security-invariants).
