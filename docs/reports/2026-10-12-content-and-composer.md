# Content Preview and Composer Area Delivery

## Implementation Results

First complete [Architecture Design](../design/content-preview-architecture.md), then implement according to the same file access, format distribution, parsing, and display interfaces. Continue using the Go daemon, React client, and existing permission boundaries, with no new database migrations or inference protocols added.

- Chat attachments, project materials, and workspace files enter the same artifact area. The original artifact editor retains mounts and drafts; when viewing a file, its save shortcut is suspended, and editing can still continue after closing.
- Markdown, text, code, HTML/SVG, DOCX, PPTX, XLSX, ODT/ODS/ODP, CSV/TSV, PDF, images, audio/video, and ZIP/TAR/GZIP directories have independent display modules. Adding similar formats only adds parsers and registration items, without modifying the chat page.
- Word displays body text, headings, in-paragraph emphasis, tables, and in-package images; presentations are sorted by actual slide relationships; spreadsheets retain coordinates, shared strings, and cached values. Formulas, macros, and external links are not executed.
- DOC/PPT/XLS/RTF and Office layouts use optional local LibreOffice conversion. Under macOS, a Seatbelt process with restricted read, write-only temporary directory, and disabled network is used; priority is lowered, single concurrency, 30-second timeout, and cleanup of temporary materials and execution slots is canceled.
- PDF uses an on-demand loaded PDF.js independent worker; only the current page is drawn, with a single-page canvas of up to 4 million pixels, and closing terminates the task. Fonts, CMaps, and image decoders are provided with the client, not relying on CDNs; the script execution engine is not packaged.
- Long text has a maximum of 60,000 characters per page, and ultra-long Markdown is read as paginated source code; documents have 100 content blocks per page, and document tables and worksheets have 100 rows per page. Avoid constructing page nodes for the entire long document at once.
- Parsing is single-concurrency with no waiting queue; the frontend only retries up to twice for transient `preview_busy`, 120ms each time, with no background polling. Closing/switching cancels requests and releases Blob URLs; corrupted files only affect the current panel.

## Composer Area Design and Popover Fixes

Adopting the layout of the user reference image is more reasonable: the body text occupies the main area, while attachments, directories, approvals, models, and sending are concentrated at the bottom, reducing the upper and lower rows of conditions and duplicate descriptions. Attachments, optimization, and sending use existing icons, retaining hover descriptions, screen reader names, and original shortcuts; usage and historical materials are grouped under "More". The saved state is retained, and approval rules follow the existing mechanism.

The root cause is that the working directory, permissions, models, etc., each use independent native `details`, with no mutually exclusive states, and no external click/focus-out handling. Material selection also directly deletes the `open` attribute of the DOM, resulting in inconsistent closing behavior.

Changed to a single top-level popover state; clicking another entry closes the old popover, and clicking the same entry again collapses it. It closes on external clicks, focus-out, Esc, window blur, entering settings, or switching tasks/assistants. Esc returns focus to the triggering entry, and material selection does not submit the outer message form. Nested descriptions, file readers, and approval dialogs remain independent.

Actual interface review steps:

1. Open working directory: The old version can display directory descriptions, but occupies the input area.
2. Click all approvals: The old version retains two expanded items simultaneously, with screenshots confirming overlap; the new version only retains the permissions popover, with an expansion count of 1.
3. Press Esc / click input box: The expansion counts in the new version both return to 0. Unit tests simultaneously verify navigation, material references, focus, and other dialogs.

For public display screenshots, see [Interface Assets](../assets/README.md); other temporary acceptance screenshots are not published with the repository.

- `28-composer-directory-before.jpg`: Working directory.
- `29-composer-overlap-before.jpg`: Two popovers overlapping.
- `30-composer-exclusive-after.jpg`: Mutually exclusive after fix.
- `31-content-word-composer.jpg`: Word tables/images and the new bottom bar.
- `32-slides-compact-composer.jpg`: Slide page turning and send button alignment within narrow content columns.
- `33-prompt-refinement-simple.jpg`: Modification suggestions popover opened on demand.

Screenshots do not equate to full accessibility certification. This round verified keyboard exit and focus recovery; the browser viewport override did not actually change the 1320px viewport, and this attempt was not counted as a 1024px window acceptance. The chat input area, which is approximately 500px wide when the artifact area is side-by-side, has been actually checked.

## Interfaces

The contract has been written into `api/openapi/openapi.yaml`. Optional capabilities are `workbench.preview: 1` and `workbench.preview_layout: 1`; if the old kernel does not declare them, the entry points are hidden.

```text
GET /api/v1/agents/{id}/files?path=...
GET /api/v1/agents/{id}/file-preview?path=...
GET /api/v1/agents/{id}/file-content?path=...&version=...
GET /api/v1/agents/{id}/file-layout?path=...&version=...

preview.Source.Open(context.Context, agentID, path string) (preview.File, error)
preview.Parser.Parse(context.Context, preview.Input) (preview.Document, error)
preview.Service.Preview(context.Context, agentID, path string) (preview.Document, error)
workspace.Workspace.OpenRead(context.Context, path string) (*os.File, error)
```

Original files go through the authentication API, disable caching, support Range, and do not put tokens in the URL. The workspace root handle prevents path traversal and out-of-bounds symbolic links, and rejects non-regular files. File version checks use size and modification time; this is a read-only consistency check and does not replace the security checks during P3B writes.

Unify `preview_*` errors to reuse domain error codes and zh/en copy. Low-level exception paths and bodies are not echoed; logs record request ID, format, phase, status, duration, and size. Responses provide `X-Preview-Id` for troubleshooting.

## Verification Results

```bash
test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...
npm run typecheck
npm test
npm run build
cd worker && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest
make e2e
go test -race ./internal/session ./internal/workbench/preview ./internal/server
```

Actual run passed: full Go, 231 desktop items, 360 Web items (47 files), 178 Python items; end-to-end approval with real Python worker processes and mock models passed. Long text pagination, PDF task termination, and audio/video unloading have been included in the tests. Finally, the production build succeeded, with the entire Web output being approximately 5.55MB, of which the PDF reading module is about 433KB and the worker is about 1.26MB (both uncompressed sizes), loaded on demand; code splitting cannot be equated to measured energy saving percentages.

OpenAPI 57 paths and 245 references parsed successfully; no internal prompt markers were found in production assets, and the QuickJS execution engine was not included. Repository scanning found no real API keys authorized for this round, and temporary real calling programs have been cleaned up. The wrap-up corrected the type compatibility issues of optional title pass-through for status badges and optional query parameters for draft test mocks.

Format and security tests include: DOCX heading/table/macro ignoring, PPTX logical order, XLSX shared strings/sparse coordinates/cached values, ODF mixed text/huge repetition counts, UTF-16, CSV multi-line fields, ZIP directories, corruption/traversal/decompression limit exceedance/XML instructions, cancellation and concurrency slots, paths and symbolic links, Range, file changes, old kernels, HTML isolation, late responses, Blob release, PDF termination, table pagination, and slide page turning.

Real local file calls are recorded in [Content Preview Results](2026-10-12-content-preview-api.json). After fixing the compatibility issues found in real Office files regarding whitespace outside XML documents, relationship namespaces, and absolute paths within packages, DOCX/PPTX/XLSX all passed. Generally, parsing APIs for small samples take about 3–10ms, which does not represent the time taken for large documents or end-to-end interfaces.

[Office Layout Results](2026-10-12-content-layout-results.json): All 9 real files of DOC, DOCX, ODT, PPT, PPTX, ODP, XLS, XLSX, and ODS output valid PDFs, taking about 0.98–1.23 seconds. Additionally, the dedicated real RTF conversion passed (about 1.85 seconds), along with tests for temporary material/slot cleanup upon cancellation.

The browser has actually viewed Markdown, HTML, Word, PPTX, XLSX, and PDF; Word tables and embedded images were visible, PPTX was pageable, and XLSX worksheets could be switched. After a corrupted DOCX reported an error, normal spreadsheets could still be opened. MP4 actually loaded a 640×360 frame with a duration of 1 second, and WAV had a duration of 0.5 seconds, both with readyState=4 and remaining paused; upon unmounting, they explicitly pause, remove src, unload media, and reclaim Blobs. Archived tasks can still read files, while sending remains disabled.

One-click backfill for prompt optimization, automatic closing of supplementary suggestions, and restoration passed in the isolated acceptance model, with no click on send during this check; previously existing user messages in the acceptance task were not deleted. For real gateway quality tests, see also [Records](2026-10-12-live-prompt-validation.md).

## Clear Boundaries and TODOs

- This implementation covers viewing and display, while existing text artifact editing, versioning, and P3B apply/revert continue to use the original interfaces; it does not claim to have implemented full-featured Office editing.
- Office content reading does not guarantee that complex master slides, chart objects, revisions, fields, and numeric styles will be consistent with the original application; local converters can be used to view the layout.
- The current legacy Office/layout conversion adapter is limited to macOS and requires LibreOffice to be installed or explicitly configured. The test converter is configured only via environment variables, without writing the local runtime path into the product. Windows/Linux retain built-in format reading and downloading, requiring equivalent isolated adapters to be added in the future.
- HTML/SVG is a static isolated preview: scripts, remote/relative resources, forms, and nested pages will not be executed or loaded. Web pages relying on them are not full application previews.
- RAR/7z, HEIC/TIFF, Apple iWork, etc., are only recognized and retained for download; whether media is playable depends on system codecs. Do not misrepresent attachment uploadability as full rendering capability for all formats.
- Input limits: text 2MiB, structured content 32MiB, original files 64MiB; compressed members/expansion volumes, pages, and cells have additional budgets. No cross-device energy consumption or peak memory comparisons were conducted.

This content preview and interaction adjustment did not change the default approval levels, user keys, database schemas, or existing task identities. For verification of subsequent public versions, see [Release Records](2026-10-12-public-release.md).
