# Roadmap

Developed by Gogosling.ca in Canada, open to contributors everywhere. Our priorities are local work, predictable resource use, and reviewable changes. This is a contribution map, not a release schedule.

## Available in the development preview

- Projects, selected materials, recoverable drafts, task history, and saved result versions.
- Shared Web/desktop UI, grouped settings, and English, Chinese, and French interfaces.
- Guarded text-file preview, apply, and undo on supported platforms.
- Bounded concurrency, workspace serialization, cancellation, and on-demand content viewers.
- OpenAI-compatible and Anthropic adapters, scheduled tasks, MCP, and extensions.

See the [README](README.md) for support boundaries. Tests do not replace release validation on real devices.

## Make first use dependable

**Open work:** reproducible release packaging, signed installers, clean-machine checks, and upgrade/rollback exercises.

**First PR:** document one setup failure with OS, architecture, reproduction, and minimal fix. Start in `apps/desktop`, `internal/update`, or `docs/development`.

## Improve document fidelity

**Open work:** more synthetic/public fixtures, clearer per-format limitations, and isolated conversion beyond macOS.

**First PR:** one small fixture and regression test for text, tables, slide order, or a malformed archive. Keep limits and sandboxing intact. Start in `internal/workbench/preview` and `apps/web/src/content`.

## Help measure the difference

**Open work:** reproducible benchmarks for idle use, long conversations, multiple agents, cancellation, and repeated preview open/close.

Measure the **whole process tree**: UI, daemon, worker, and converter. Record peak/steady-state memory, CPU time, disk growth, queue wait, latency, and resource release after cancellation. Include OS, hardware, revision, model, workload, warm/cold cache, and repetitions. Energy claims require suitable device measurements.

**First PR:** a local fake-model scenario with a script and raw results. Compare identical workloads; preserve approvals, recovery, and drafts. Start in `internal/session`, `worker/tests`, and `tests/e2e`.

## Make the workbench easier to use

**Open work:** keyboard/screen-reader validation, French copy review, small-window checks, and recovery messages.

**First PR:** reproduce one focus or translation issue, fix the relevant component/locale, and include verification. Start in `apps/web/src/components`, `apps/web/src/pages`, or locale packs.

## Propose or maintain an area

[Propose a feature](https://github.com/GoGoslingCA/gogosling/issues/new/choose) with a use case, scope, resource cost, and compatibility plan. Small fixes do not need a long design document. For continuing module maintenance, start in [Discussions](https://github.com/GoGoslingCA/gogosling/discussions).

Cloud accounts and hosted team spaces are not prerequisites for the local workflow. Architectural expansion should justify its ongoing resource and maintenance cost.
