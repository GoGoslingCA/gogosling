# Architecture Review Conclusions and Phase Report (2026-10-10)

> Scope: First-round implementation of gogosling (contracts → two waves of parallel module implementation → integration → review → fix).
> Verification baseline: `go build ./... && go vet ./... && go test -race ./...`, `GOOS=windows/linux go vet ./...`, worker `ruff / mypy / pytest`, `npm run typecheck && npm test && npm run build`, `make e2e` (real Python worker process + fake model service) all passed; GitHub Actions three-platform matrix turned green starting from `8903381`.

## 1. Review Conclusions

The architecture is implemented according to [docs/architecture.md](../architecture.md), and three invariants are verifiable in code and tests:

| Invariant | Implementation Method | Verification |
|---|---|---|
| Daemon holds all state, frontend is just a client | `internal/app` composition root; desktop shell, Web, CLI all go through local HTTP + SSE; CLI does not open the database, only reads `daemon.json` | `internal/app` integration tests (fake worker process runs through conversation, approval, cancellation, conflict, lock, shutdown); `tests/e2e` |
| Secrets do not enter database, logs, or events | `secret` (OS keychain / encrypted file, backend chooses persistence); provider / channel records only store references; observability desensitization Handler; server only returns `has_secret` | provider / channel / server tests assert that responses and database do not contain secrets; e2e asserts database files have no plaintext secrets |
| Side effects must go through permissions and sandbox, third-party code is out-of-process | Worker process can only apply via `host.tool.call`; `tools.Executor` fixed sequence permission → execution; MCP / plugins are subprocesses; `archcheck` enforces dependency direction | permission hard rejection/rule/authorization/level tests; tools rejection to `is_error`; archcheck tests |

Dependency direction is enforced by `internal/archcheck` tests (cross-layer imports fail immediately), covering the rule that the transport layer must not import `store`.

**Overall Assessment**: Clear layering, enforceable boundaries, and critical security paths covered by tests; ready to proceed to the next phase (TeamAI team workflow, Linux/Windows sandboxing, IM adapter extensions). Manual smoke testing has not yet been performed on real Windows/Linux machines, which is currently the largest verification gap (CI's Windows matrix can only cover automated tests).

## 3. Issues Found and Fixed During Review

The review was completed from 6 perspectives (security invariants, concurrency and lifecycle, contract consistency, layering and duplication, cross-platform, document accuracy), with a total of 82 findings (7 high, 26 medium, 49 low). As requested by the user, filtering and verification of findings were completed by the architecture lead, without multi-agent double-check. All high and medium issues have been fixed, 29 low issues have been fixed, and the remaining 20 are listed in the backlog.

### 3.1 high(7/7 fixed)

| ID | Issue | Fix |
|---|---|---|
| DOC-01 | `auto_in_sandbox` directly allows command execution on unisolated Linux/Windows; the lower bound of untrusted input does not contain execute | Permission requests add `Isolated` fact (only run_command under Seatbelt is true); execute/network falls back to ask_risky when unisolated; execute asks at least under untrusted input |
| XP-03 | Path rules are case-sensitive, `.SSH/id_rsa` bypasses deny rules on macOS/Windows | darwin/windows case-insensitive matching; deny rules fail-closed on all platforms; Windows long name normalization |
| XP-05 | Desktop shell `app://` page cross-origin accesses loopback API, browser refuses to read response | Main process `protocol.handle` proxies `app://gogosling/api/*` to daemon and injects token; token no longer enters render process |
| XP-06 | `daemon stop` kills process solely based on leftover `daemon.json` pid, causing accidental kill during PID reuse | First use token to access `/version` to verify identity and `started_at`, if failed, only delete leftover files |
| XP-02 | Quotes in `cmd /C` on Windows are escaped by Go, causing all quoted commands to fail | `sandbox.Spec.RawCommandLine` + `SysProcAttr.CmdLine` passed as-is |
| XP-01 | Windows distribution lacks timezone database | `import _ "time/tzdata"` |
| C1 | Worker process writes empty-name tool calls into history, after which the entire session is rejected by the parser | Calls with missing names do not generate `tool_call` fragments; lenient when parsing history; add round-trip test for "output must be parseable by itself" |

### 3.2 medium(26/26 fixed, selected)

- Running state machine: `approval.*` events now enter the state machine via `session.NoteApproval`, and `Run.status` will display `awaiting_approval` (C2/CONC-1).
- Delete Agent: Check in-flight runs first, cascade delete sessions and publish `session.deleted`, scheduler and gateway in-memory states are cleaned up with `agent.deleted` (C5).
- Gateway: Derive routes by session key (can still reply after restart), deliver `push_text` to push to IM (LAY-05); failure reasons are localized instead of machine constants (C13).
- Concurrency: Engine bounded-waits for reverse request wrap-up (CONC-4); cancellation convergence of unaccepted runs (CONC-3); `ScopeAlways` decides first, then writes rules and audits (CONC-2); close step-by-step budget (CONC-5).
- Contracts: Windows graceful shutdown endpoint `POST /api/v1/system/shutdown` (XP-07); settings return `effective_locale / effective_timezone`, do not masquerade as user preferences when unset (C3/LAY-01); `Agent.tools` tri-state semantics (C4/DOC-02/DOC-04); message pagination limit unified to 500 (LAY-02); MCP `Headers` truly takes effect (LAY-04); credential backend chooses persistence, no silent switching (XP-11).
- Cross-platform: Subprocesses no longer pop up console windows (XP-08); kernel data directory protection covers case sensitivity and `\\?\` prefix (XP-04); proxy and certificate environment variables passed through to worker processes (XP-13); desktop shell deep link cold start, Windows autostart status, built-in extension packaging (XP-09/10/12).

### 3.3 Issues Fixed Directly by the Architecture Lead During the Integration Phase

- Duplicate injection of system prompts: session puts system messages into `Messages`, and the worker process injects them again according to `agent.system_prompt` (fixed on both sides with regression tests added, protocol specifies delivery only via `agent.system_prompt`).
- `go.sum` lacks MCP SDK transitive dependencies; platform assumptions and timing races in sandbox/engine tests on Windows; macOS runner's `LC_ALL` interferes with locale tests; rule path comparison failure caused by Windows 8.3 short paths.
- provider silently backfills `api.openai.com` for `openai_compatible` (prohibited by protocol); engine cancellation code ends as failure on the session side.

## 4. Module Completion Status

| Module | Status | Description |
|---|---|---|
| `model` / `event` / `config` / `version` | Completed | Contract layer; event bus with replay and `gap` |
| `store` | Completed | SQLite dual pool, embedded migrations, all repositories; Todo: upgrade snapshots, retention period cleanup |
| `observability` / `i18n` / `secret` / `identity` | Completed | Desensitized logs, zh/en language packs, keychain + encrypted files (backend persistence), local tokens and handle files |
| `permission` / `sandbox` / `tools` / `workspace` | Completed (Sandbox macOS only) | Rule engine, approval proxy, audit; Seatbelt; 6 built-in tools + `load_skill`; Linux bwrap / Windows Job Object to be implemented |
| `engine` + `worker` | Completed | Worker process supervision, stdio JSON-RPC, reasoning loop, openai_compatible / anthropic / ollama, reasoning.delta; worker's memory / knowledge is still a skeleton |
| `session` / `agent` / `provider` | Completed | Running state machine (including waiting for approval), push messages, template library, provider presets and connectivity tests |
| `scheduler` / `gateway` / `channel` | Completed (Channels console and telegram only) | cron parsing and missed compensation; gateway routing and reply chunking; Feishu, DingTalk, WeChat Work, Discord adapters to be implemented |
| `extension` / `protocol/mcp` | Completed | Skills / plugins / MCP services, tool summary pinning to prevent rug pull, built-in asset seeding; marketplace and signatures to be implemented |
| `server` / `client` / `app` / `cmd` | Completed | 51+2 routes, SSE, composition root, complete CLI; `protocol/acp` and `update` are still package comments |
| `apps/web` / `apps/desktop` | Completed (Not verified by packaging on real machines) | React UI; Electron shell (proxy, tray, deep link, daemon lifecycle) |
| Documentation | Completed | Architecture, AGENTS, OpenAPI, worker-rpc, 19 reference records, this report |

## 5. Todo

**P0 (Next Iteration)**
1. Windows / Linux real machine smoke testing: subprocesses without windows, command quotes, timezones, `daemon stop`, `app://` proxy and SSE in desktop shell packages.
2. Linux bwrap sandbox and Windows Job Object; `extension.ProcessRunner` integrated into sandbox (plugin subprocesses currently lack OS-level isolation).
3. `protocol/acp` (bidirectional) and `update` (kernel and runtime environment upgrade, pre-upgrade snapshot).
4. Distribute Python runtime environment with installation package (`electron-builder`'s `resources/worker` is currently an empty placeholder).

**P1**
5. IM adapters: Feishu, DingTalk, WeChat Work, Discord; inbound attachment download; slash commands.
6. worker's memory / knowledge; multimodal input (`model.Part` content fragments); 429/5xx backoff retry.
7. Hot update of scheduler timezone settings; execution history table; `job.updated` events.
8. Remaining low findings: LAY-06 (sinking of a small number of domain rules in the composition root), LAY-10 (convergence of export surfaces), LAY-11 (isomorphic type copying), LAY-17 (sinking of validation in server handlers), C7 documentation, DOC-05 (modified), CLI attachment upload / message viewing / update subcommands.
9. Desktop shell: `SIGTERM` fallback wait aligned with daemon worst-case shutdown duration; race condition between `launch()` and `stop()`; product decision on tray restarting daemons not started by this shell.

**P2**
10. Observability: OTel SDK adaptation, rotation by date; usage and cost statistics.
11. Extension marketplace: source signatures, revocation lists, archive / URL installation, plugin resident processes.
12. Multi-user mode and directory service access points.

## 6. Scale

As of this report: Go approx. 35.7k lines of implementation + 28.9k lines of tests (707 test functions); Python worker process approx. 3.5k lines + 2.6k lines of tests; Web approx. 6.0k lines, desktop shell approx. 2.8k lines of TypeScript; 16 commits.

## 7. Workflow Records

- The first wave of 12 modules and the second wave of 2 integration layer tasks were completed by parallel coding agents; each task was implemented and self-tested according to the contract.
- The architecture lead is responsible for contracts, integration alignment, repository-wide validation, filtering of review findings, and cross-group wrap-up; validation of review findings no longer uses multi-agent double-checking.
- Related documents: For user stories and PRD of the TeamAI team workflow, see `docs/prd/team-workflow.md`; for architecture and process design, see `docs/design/team-workflow-architecture.md` (in progress).
