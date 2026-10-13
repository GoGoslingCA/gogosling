# Desktop diagnostics: server integration proposal

Gogosling.ca is a Canadian company developing the gogosling local-first workbench. Desktop diagnostics are optional: users review a small report and choose whether to send it to Gogosling.ca.

**Status:** this document proposes a desktop upload authorization extension. The existing [error-log OpenAPI](https://api.gogosling.ca/v1/error-logs/openapi.json) describes product-credential uploads; it does not yet advertise the ticket endpoint below. Do not describe the new flow as deployed until its contract and public acceptance tests pass.

## Boundaries

- Source development builds retain actionable local failure messages but do not show an upload entry point. Packaged releases can enable manual reporting through an explicit release configuration; packaging alone does not enable it.
- No automatic reporting, background retry, persistent upload queue, periodic polling, or durable device identifier. A failed or canceled report remains local until the user chooses to send it again.
- A report contains only allowlisted diagnostic codes and technical metadata. It excludes raw log lines, stack traces, paths, prompts, document contents, account details, headers, and credentials.
- The user reviews the exact JSON body before sending. The desktop main process freezes that body, computes its digest, obtains a ticket, and uploads the same bytes. The renderer never receives the ticket or a product credential.
- Shared server credentials must stay on the server. Do not embed them in source, builds, configuration distributed with an installer, or update metadata.
- Existing backend credentials, query authorization, website APIs, mobile APIs, and model services retain their current behavior.

## Authorization model

A publicly distributed app cannot keep a shared secret. Use an anonymous, short-lived, single-use upload ticket bound to one report instead.

**A ticket does not establish that the sender is an official app, an authenticated user, or a trustworthy source.** The server must treat desktop reports as untrusted anonymous submissions. A release check and explicit click are client behavior guarantees; the server cannot infer them from a request. Origin headers, CORS, user-agent strings, and a claimed version are not authentication.

The ticket grants only one upload for `gogosling-desktop`. It cannot list events, query an event, select another product, administer credentials, or access another service. If abuse cannot be contained by the bounded anonymous intake, disable ticket issuance while retaining existing authenticated backend uploads.

## Proposed ticket endpoint

`POST https://api.gogosling.ca/v1/desktop-diagnostics/tickets`

Use HTTPS on port 443 and `Content-Type: application/json`. No shared bearer credential or cookie is required to request a ticket. Reject other methods, compressed bodies, unexpected fields, and bodies over 1 KiB. Do not redirect.

Request:

```json
{
  "schema_version": 1,
  "body_sha256": "<64 lowercase hexadecimal characters>",
  "body_bytes": 1024,
  "event_id": "<report UUID>"
}
```

- `body_sha256`: SHA-256 of the exact, uncompressed UTF-8 upload body, including whitespace. No JSON reserialization or canonicalization is performed between hashing and sending.
- `body_bytes`: an integer from 1 through 16,384, inclusive; measure bytes, not characters.
- `event_id`: the UUID in the report's sole event. Reuse it when manually retrying that frozen report; create a new UUID for a newly generated report.
- `schema_version`: exactly `1`.

Return `201 Created` with `Cache-Control: no-store`:

```json
{
  "token": "<opaque bearer ticket>",
  "expires_at": "<UTC RFC3339 timestamp>",
  "body_sha256": "<the requested digest>"
}
```

Generate the token from at least 32 cryptographically random bytes. The default lifetime is 60 seconds; the contract permits at most 120 seconds. Store only its cryptographic hash plus its scope, digest, byte count, event ID, and expiry in bounded, expiring server state. Do not create a credential file for each request or retain expired tickets. A server restart may invalidate outstanding tickets; fail closed and let the user retry with a new ticket.

Return concise error codes without reflecting request values or internal exception text. Use `400` for malformed requests, `413` for excessive size, `415` for an unsupported content type or encoding, `422` for schema errors, `429` for throttling, and `503` for temporary unavailability. Send `Retry-After` with `429` and `503`. A missing or disabled ticket feature must fail closed; it must not fall back to anonymous uploads without a ticket.

## Upload using the existing endpoint

`POST https://api.gogosling.ca/v1/error-logs`

Send `Authorization: Bearer <ticket>` and `Content-Type: application/json`. Tokens must never appear in URLs, events, analytics, or logs. Clients reject redirects rather than forwarding authorization to a new location. Server responses use `Cache-Control: no-store`.

The ticket path adds a stricter policy to the existing endpoint. Existing product credentials retain their current scope and limits; do not loosen those checks to accommodate desktop uploads.

For desktop tickets, enforce all of the following before committing:

1. The ticket exists, is unexpired and unused, and has only desktop-ingest scope.
2. The received raw body is uncompressed and at most 16 KiB. Its byte count and SHA-256 match the ticket.
3. The JSON body has exactly one event, with the bound event ID. The envelope product and event service are exactly `gogosling-desktop`.
4. The event matches the desktop report schema and its value allowlists. The broad backend event schema is insufficient for this path.
5. Existing redaction, retention, product quota, global quota, and storage safeguards also pass. Redaction runs after checking the digest against the received bytes.

Bind successful ticket consumption and the durable event write atomically, or use an equivalent transaction design that cannot acknowledge an uncommitted report or accept concurrent double use. Invalid bodies do not consume a ticket. Failed attempts still count against abuse limits. Reject expired, unknown, or already consumed tickets with `401`; reject a different product or read scope with `403`. Reject a body binding or event-schema mismatch with `422` without reflecting submitted data.

Deduplicate using the existing `(product, event_id)` boundary and never overwrite an existing event on a duplicate. A lost response or client cancellation may occur after the server has committed. A manual retry requests a new ticket for the same frozen body and event ID, allowing the existing deduplication behavior to confirm receipt.

Keep the existing `201` acknowledgement shape:

```json
{
  "success": true,
  "accepted": 1,
  "duplicates": 0,
  "event_ids": ["<the bound report UUID>"]
}
```

For a report already stored, return `accepted: 0` and `duplicates: 1`. The desktop accepts success only when `accepted + duplicates === 1` and `event_ids` contains exactly its report UUID. Do not return stored event content. Treat client timestamps and versions as unverified claims; use the server receipt time for operational reporting.

## Report payload

The envelope uses the existing error-log schema with a fixed product and one event. The desktop-specific schema must reject unknown fields rather than merely redact arbitrary attachments.

Required fixed values:

- Envelope `product`: `gogosling-desktop`.
- Event `service`: `gogosling-desktop`.
- Event `environment`: `production`.
- Event `level`: `error`.
- Event `message`: `desktop_diagnostic_report`.

The following fields complete the event. Require them unless marked optional, and reject all other fields at every object level:

- `event_id`: UUID, equal to the ticket binding.
- `timestamp`: UTC RFC3339 timestamp.
- `release`: `unknown` or a version matching `^\d{1,4}\.\d{1,4}\.\d{1,4}(?:-(?:alpha|beta|rc)(?:\.\d{1,4})?)?$`.
- `tags.platform`: `darwin`, `linux`, `win32`, or `unknown`.
- `tags.arch`: `arm64`, `x64`, `ia32`, `arm`, `ppc64`, `s390x`, `riscv64`, or `unknown`.
- `tags.daemon_state` (optional): `idle`, `attaching`, `locating`, `starting`, `ready`, `restarting`, `stopping`, `stopped`, or `failed`.
- `tags.daemon_owned` (optional): boolean.
- `tags.restart_attempts` (optional): integer from 0 through 5.
- `tags.error_code` (optional): one of the daemon codes listed below.
- `tags.exit_code` (optional): integer from −2,147,483,648 through 4,294,967,295, accommodating signed and unsigned platform exit codes.
- `context.category`: `connection`, `task`, `preview`, or `application`.
- `context.recent_errors`: an array of zero to 20 objects, each containing only an allowlisted `code` and a UTC RFC3339 `timestamp`.

Daemon codes are `executable_not_found`, `spawn_failed`, `ready_timeout`, `exited`, `stale_handle`, and `restart_limit`. Recent-error codes accept those six codes plus `daemon_start_failed`, `daemon_unavailable`, `daemon_exited`, `renderer_unresponsive`, `renderer_gone`, `load_failed`, `api_proxy_failed`, `static_resource_failed`, `http_400`, `http_401`, `http_403`, `http_404`, `http_409`, `http_413`, `http_422`, `http_429`, `http_500`, `http_502`, `http_503`, and `http_504`. HTTP records contain only the status code; no URL or response body is collected.

The client omits unknown snapshot fields instead of sending their original values. No arbitrary message, stack, user comment, filename, URL, personal identifier, or additional nested object is accepted. Publish this exact schema in the external OpenAPI and validate it on both sides. The current client allowlists are implemented in [desktop diagnostics](../../apps/desktop/src/main/diagnostics.ts); the renderer-facing interface is defined separately in [shared diagnostics types](../../apps/desktop/src/shared/diagnostics.ts).

## Resource and abuse limits

- Enforce an explicit total outstanding-ticket cap and short expiry. Reject excess issuance without unbounded queues, per-request disk files, or uncontrolled memory growth.
- Start with configurable limits of six tickets per minute and 60 per day per source IP, in addition to stricter existing global and per-product limits. Apply upload-attempt limits as well as issuance limits.
- Derive IPs only from the connection or a trusted reverse-proxy configuration. Do not trust arbitrary forwarded headers. Use a rotating keyed hash for transient limiter keys; do not put raw IPs or limiter identifiers into diagnostic events. Keep limiter state bounded and expiring. Hashed IPs remain abuse-control data, not anonymous user identities.
- Rate limits mitigate abuse; they do not establish client identity. Shared networks can reach a per-IP limit. Return `Retry-After` and let the user retry later without automatic traffic.
- Cap concurrent ingest work and request duration, including slow request bodies. Reject overload early and retain product isolation so anonymous desktop traffic cannot consume existing services' budgets.
- Keep the current server-side retention and backup policies for accepted events. Do not extend retention through ticket retries or store duplicate event bodies.
- Redact Authorization headers at every reverse proxy and application log boundary. Do not log request or response bodies from either endpoint; ticket responses contain credentials.

## Desktop lifecycle

The client retains one frozen preview in memory for at most ten minutes and keeps at most 20 recent error-code records in memory. It permits one active submission, with one 15-second deadline covering ticket issuance, upload, and acknowledgement. Each response body is limited to 8 KiB; return JSON with the correct content type. The client requires `201` for both ticket issuance and upload acknowledgement; any other successful HTTP status is treated as an unexpected response. Cancel, navigation, and app shutdown abort active networking and release local resources. No automatic retry follows a timeout, cancellation, or rate limit. The client accepts `Retry-After` as seconds or an HTTP date and enforces a cooldown clamped to 1–3,600 seconds before another manual attempt. Server-side limits remain authoritative after that local cooldown expires. Cancellation cannot promise that an already transmitted event was not committed by the server.

The upload entry point requires an explicit release feature flag, `app.isPackaged`, and no development URL. The main process validates the requesting frame and accepts only the frozen report it created; the renderer cannot choose an endpoint or submit arbitrary log text. Essential local error messages remain available in both build modes.

## Acceptance and rollout

Before enabling the feature in a distributed release:

1. Publish the extended external OpenAPI, including the exact desktop schema, ticket route, status codes, limits, and scope behavior. Confirm compatibility with current backend uploads and separately authorized queries.
2. Use synthetic, non-sensitive reports to verify issuance, digest and byte binding, expiry, wrong product, read denial, malformed input, unknown fields, and oversized or compressed bodies. Never use production user content for acceptance tests.
3. Test concurrent reuse of one ticket, invalid-body attempts followed by a valid attempt, fresh-ticket duplicate uploads, lost acknowledgements, and cancellation after a possible commit. Only one event may be stored for a report UUID.
4. Test service restart before and after commit. Outstanding volatile tickets must fail closed, committed events must survive, and fresh-ticket retries must deduplicate.
5. Verify throttling, `Retry-After`, bounded limiter/ticket memory, slow-body timeouts, storage pressure, and graceful refusal during overload. Check that other products and existing website, mobile, and model routes remain functional.
6. Check access, application, and error logs for leaked bearer tickets, request bodies, response bodies, personal data, or secrets. Query results must remain redacted and query authorization must remain separate.
7. Verify the source build has no diagnostics-upload entry point or diagnostics network activity; an enabled packaged build supports preview, send, cancel, and manual retry. Confirm no upload occurs before Send.

Until these checks pass, client-side unit tests establish only the proposed protocol behavior. They do not establish that ticket authorization is deployed or that a live report reached the server.
