USSD loan repayment integration — 27 October 2026

Deployed architecture:
Cloudflare -> USSD Caddy -> gateway menu -> private payment sidecar -> existing
LMS CollectionService -> configured provider. Existing signed LMS webhooks verify
the provider result and call the existing repayment poster. No core LMS routes,
schema or collection services were changed; the sidecar loads the same deployed
application image, dependencies, configuration, database and Redis network.

Caller flow:
3 Make payment -> choose loan -> enter amount -> 1 Confirm (0 cancels).
Confirmed initiation: "Payment request submitted. Please wait for your mobile-money
prompt and enter your wallet PIN there."
Uncertain initiation: "Payment request is processing. Check payment status before
retrying." The gateway never collects a PIN or names the payment provider.
4 Payment status -> choose loan -> current verified result from LMS.
Pending is never displayed as success. Completion requires LMS verified posting.

Identity/trust:
No customer PIN/password/OTP or customer token is required for options 3/4.
An authenticated operator must supply the actual calling MSISDN; the module does
not prove the subscriber number independently. A compromised operator credential
could initiate unwanted payment prompts. Protect the callback credential and verify
Hobbiton's subscriber provenance before public launch. The mobile-money provider
owns debit authorization. NRC is not used as a password or as a payment lookup key.

The private module resolves a single user from normalized phone variants and joins
loans to that user's applications. Duplicate/missing matches fail closed. Conflicting
loan.user_id values fail closed. Uses real loans.id, never application-ID fallback.
No arbitrary payer number is accepted in the public callback. Loan lists contain
only masked IDs, with at most five choices. Status includes closed loans with USSD
payment history, so full repayment receipts remain accessible.

Runtime:
Gateway: mightyfin-ussd-gateway.service, localhost:28093.
Private payment sidecar: mightyfin-ussd-payments-payments-1, localhost:28094.
Sidecar health: GET /healthz (liveness, not provider readiness).
Authenticated POST endpoints: /v1/loans, /v1/status-loans, /v1/collect, /v1/status.
The public Caddy routes only /ussd and /healthz to the gateway; never expose 28094.
Sidecar uses the current LMS image and a read-only mount of this module. It has
four PHP workers so a slow provider request does not block all menu reads.

Credential:
New LMS integration scope payments:ussd, denied by existing payments:collect APIs.
Token stored only in /home/mightyfin/.config/mightyfin/ussd-gateway/payments.key.
Only its hash is stored in LMS. Created 27 October 2026, expires after 90 days;
rotate before expiry. It was not emailed or committed. Existing operator pilot
key now accesses payment menus too; restrict its distribution accordingly.

Configuration in gateway runtime.env:
USSD_PAYMENTS_ENABLED=true
USSD_PAYMENTS_KEY_FILE=/home/mightyfin/.config/mightyfin/ussd-gateway/payments.key
This is independent of the legacy adapter's disabled /v1/repayments endpoint.
That old endpoint remains blocked; it must not directly post a repayment.

Duplicate protection:
Confirmation derives a stable SHA-256 key from session ID, bound caller, real loan
ID and normalized amount. LMS reserves the request durably before calling the
provider. Identical requests replay; changed payloads conflict. A per-loan cache
lock serializes competing IDs and unresolved prior collections block new prompts.
Timeouts/unknown outcomes must be reconciled, never treated as failed payments.
Status queries use existing server-side verification, not callback-reported amounts.

Verification:
- Go unit tests, race tests and vet pass, covering confirmation, cancellation,
  decimal handling, retry, uncertain outcome, navigation and provider-free wording.
- 21 isolated SQLite assertions exercise the real deployed CollectionService with
  a fake provider and fake repayment poster: ownership, duplicate phone matches,
  input/consent validation, idempotency, unresolved-payment blocking, verified
  posting once, mismatched provider details, and closed-loan receipt access.
  Run with --network none and NO production env_file using this image:
  docker run --rm --network none -v "$PWD/deploy/payments:/ussd:ro" \
    --entrypoint php mightyfin/admin-lms:product-filter-groups-20261012 /ussd/test.php
  Check the JSON pass marker; Laravel's console exception handler may exit zero
  even when a standalone test throws.
- Public read-only checks: 68 pass. They validate menus/authentication/rejections;
  no successful collection is initiated against a real account by these checks.
- Configured provider key, HTTPS base and webhook secret were present. That is a
  configuration check, NOT a real provider payment or network availability test.

Still required: a designated test loan, payer number and agreed amount; holder
approval in the actual provider prompt; verify the final LMS repayment and duplicate
callback behaviour. Real wallet debits and mobile-network handset tests were NOT
performed. Do not describe this as an end-to-end live payment certification.

Deploy sidecar:
  docker compose -f deploy/payments/compose.yaml up -d
After module edits, restart the sidecar to avoid any persistent opcode cache.
Rollback payment capability:
  set USSD_PAYMENTS_ENABLED=false in gateway runtime.env and restart gateway.
Preserve all integration collection records; keep LMS verification/webhook handling
running to reconcile any outstanding requests. Never delete records to retry.

Activation blocker found in production:
There is no active lipila_mobile_money payment type mapped to an asset clearing
account. Existing Airtel/MTN clearing accounts are not automatically reused. New
payment initiation is blocked before contacting the provider until an intended
mapping is supplied. Replay/reconciliation of any existing collection still works.
The menu is deployed; /healthz reports payment_menu_enabled=true but
financial_actions_enabled=false while this blocker remains. That readiness flag
checks configuration, not provider network availability or a verified wallet debit.
The requested accounting account and live test number/loan/amount are pending.
