# Desktop diagnostics: live validation

Date: 2026-10-13. Client: Beta 0.0.1103, source revision `1ae0fc906c5364538f9f1466aa6459263be60fab`, macOS arm64. Service contract: [OpenAPI 1.1.0](https://api.gogosling.ca/v1/error-logs/openapi.json).

Tests used isolated app data and profile directories, synthetic reports, the fixed public HTTPS endpoints, and normal TLS verification. No model connection, user project, chat content, raw log, or long-lived credential was used. Tickets stayed in memory and were excluded from saved evidence.

## Native desktop flow

The final interface checks used the unmodified packaged app:

1. Opening the report showed a 434-byte allowlisted preview. Consent was unchecked and Send was disabled.
2. Closing the preview returned to settings without sending it.
3. Explicit consent and Send reached the native UI's success receipt.
4. A second send was cancelled from the observed Sending state. The UI showed the cancellation explanation and a manual Retry action.
5. The dialog was closed and the app returned to its start page.

The native Retry button was not clicked in that final UI pass, to limit production traffic. Manual retry was separately exercised against the real service using the diagnostic module extracted from the same package. Do not describe that distinction as a fully automated native retry sequence.

## Real transport and recovery

The bundled `DesktopDiagnostics` module was exercised with native fetch and the production endpoints. The first series ran under Node 22.22.2; a separate check ran under the package's Electron 44.7.0 / Node 24.21.0 runtime.

- Ticket issuance and upload both returned `201`; the client validated the acknowledgement and matching event UUID.
- Closing an unsent preview made no fetch request.
- Cancelling an active upload returned `cancelled`. In one measured sample, the send promise settled 1.25 ms after cancellation and fetch rejected with `AbortError` after 1.88 ms. No automatic retry appeared during the subsequent 300 ms observation window.
- An explicit retry after cancellation reused the frozen body and succeeded.
- To test an uncertain outcome, the harness deliberately discarded a real `201` acknowledgement after the server had committed. A manual retry used the same event UUID and body digest, received `duplicate: true`, and did not overwrite or create a second event for that UUID.
- The separate packaged-Electron check also received `201` from both endpoints.

Observed complete-request durations were 1,562 ms for the first module upload, 349 ms for retry after cancellation, 347 ms for duplicate acknowledgement after the controlled lost response, and 6,808 ms for the separate packaged-Electron check. These are individual smoke-test observations, including network and connection setup, not latency percentiles or performance guarantees.

The controlled module/runtime checks requested six tickets and produced four unique synthetic reports. The native UI checks above were additional. No credential-bearing response bodies or raw transport headers are published with this record.

## Limits

Cancellation proves that the client stops waiting and releases its active operation; it cannot retract a report the server has already committed. Fetch cancellation timings do not establish server-side rollback or physical socket closure timing. The service's existing 90-day retention applies to accepted reports.

No query credential was used and no stored user reports were read. Server database contents, proxy log redaction, expiry/restart behavior, and the server owner's reported automated test count were not independently re-audited in this client pass. The deployed OpenAPI and returned acknowledgements were used to verify the client contract.

No diagnostic runtime code change was required. Source-build gating, fixed destinations, bounded response bodies, callback races, and local cancellation remain covered by the existing offline tests in `apps/desktop/test/diagnostics*.test.ts` and the web diagnostic tests. Future UI or packaging changes must preserve the validated protocol and consent boundary.
