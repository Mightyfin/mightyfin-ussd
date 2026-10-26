# MightyFin USSD app

Status: **Standalone local Go application and browser simulator. External integrations deferred.**

Based on the 22 October 2026 version 1.2 requirements. All identities, offers, balances and
payments are synthetic. The app makes no outgoing API calls and needs no accounts, provider
credentials, database server, JavaScript build tools or third-party Go packages.

## Run

Requires Go 1.26+. In this folder:

```powershell
go run ./cmd/ussd
```

Open **http://127.0.0.1:8090**. To build a standalone Windows executable:

```powershell
go build -o ussd.exe ./cmd/ussd
.\ussd.exe
```

All browser assets are embedded, without remote scripts or fonts. The server binds only to
localhost. Stop with Ctrl+C. Data is saved in `data/`; `USSD_DATA_DIR` selects another directory.
Run one process per data directory. After an abrupt termination, remove `data/demo.lock` only
after confirming the previous process has stopped.

## Try the complete journey

1. Dial with synthetic number `0970000001`, select **1 Apply for a loan**, and register using a fictional
   name and NRC such as `123456/78/1`, then confirm your number. No channel PIN is created.
   Activation is simulated and separate from full loan KYC.
2. Select a product, amount, tenure and screening answers, then explicitly submit. Documents
   are not required before submission. Exiting earlier preserves a draft.
3. In the panel on the right, **Accept demo KYC**, then **Issue demo offer**.
4. On the phone select **00 Main → 3 Check status → 1 Application → 1 Next action**.
   Review all three offer pages and explicitly accept that version.
5. In the panel, **Release demo payout**. Open **Transactions** and confirm its demo success.
6. Dial again, select **This is me**, then **Repay your loan**, your loan, amount and method.
   Confirm the quote, then simulate a payment result in **Transactions**. Mobile money ends
   the local USSD session with “Please wait for the Lipila payment prompt.” Customers enter
   their mobile money PIN only in the provider's separate authorisation prompt. No real prompt
   arrives in this disconnected demo. Bank payments show their separate demo instructions.

Navigation: **0 Back**, **00 Main**, **99 Exit**. Each dial creates a five-minute session.
Customer menus open after name confirmation, without a MightyFin PIN. Drafts survive restart.
After confirmation, the main menu adapts: an active loan shows **Repay your loan**, a draft
shows **Continue your application**, missing KYC shows **Complete your documents**, and a
current offer shows **Review your offer**. An unresolved payment takes priority and shows
**Check your payment**. A new application is offered only when no current journey requires
attention. On a recognised number, the opening screen greets the stored customer by name and
offers **This is me** / **Not me**. Confirming opens the personalised main menu directly.
**Not me** clears the account context and suppresses the name and automatic matching
for the rest of that session, including after Back/Main navigation and restart. It does not alter
the customer's stored record. A fresh dial starts a new identity confirmation. Unknown numbers
receive a generic welcome. Name confirmation selects a local demo customer; it is not verified
identity authentication. Provider PIN authorisation approves a payment, not access to borrower
records. Live access controls and trusted gateway identity remain integration decisions.
If records change after a personalised menu is displayed, the app refreshes that menu and asks
for a fresh choice before following a different action.
The phone and demo staff panel intentionally share a local workbench; the latter is not a
production staff authorisation system.

## Local features

| Journey | Behaviour |
| --- | --- |
| Identification and access | Named greeting with This is me / Not me, registration without channel credentials, duplicate identity checks, shared-phone and contact-change assistance cases. Actual identity verification is deferred. |
| Applications | Synthetic products, exact minor-unit amounts, screening validation, saved drafts, stale edit protection, explicit submission consent and one shared application reference. |
| KYC and assistance | Web, WhatsApp and officer choices create assigned cases on the existing application. Demo staff can accept KYC. No uploads, links or messages are sent. |
| Assessment and offers | Demo staff review, action required/correction, decline, versioned expiring offers, paginated disclosures and explicit acceptance/decline. |
| Payouts | Accepted offer and accepted demo KYC required before staff release. Durable operation; pending, unknown, failure and success; separate ledger posting. |
| Repayments | Loan selection/pagination; due, settlement and partial amounts; mobile money and bank simulations; source confirmation; expiring quotes; pending protection; receipts and remaining balance. |
| Exceptions | Conflicting-event cases, delayed posting, linked repayment reversals and payout reversal investigation. Refunds, excess/wrong-loan corrections and third-party collections route to assistance. |
| Cancellation | Explicit withdrawal; one transaction lock prevents withdrawal and payout release both succeeding. In-flight payout cancellation requires investigation. |

Demo pricing is fictional: 10% interest, a K10 fee deducted from principal on payout, and a
single repayment of principal plus interest at the selected tenure. Both demo products permit
partial repayments. These are not MightyFin prices or credit rules. A future live channel must
use LMS products, decisions, balances and quotes rather than deploying this demo ledger.

## Answer validation

Every customer question is validated on the server. The simulator also selects the numeric,
decimal, telephone or text keyboard and shows a field-specific length limit and hint.
Invalid answers stay on the same question and do not change saved application or payment data.

| Question | Accepted input |
| --- | --- |
| Menu or consent choice | Only an option actually shown. `+1`, `01`, `1.0`, words and unlisted options are rejected. |
| Customer name | 3–60 Unicode characters; letters, spaces, apostrophes, hyphens and periods for initials. Numbers, emoji and markup are rejected. Repeated spaces are normalised. |
| NRC | Exactly six digits, slash, two digits, slash, one digit, e.g. `123456/78/1`. This checks format and existing local identities, not official identity validity. |
| Loan amount | Positive decimal kwacha with at most two decimal places, within the selected product's permitted range. No signs, commas, exponent notation or currency symbols. |
| Tenure | A whole number of months exactly matching one of the product's listed terms. |
| Employer / business | 2–100 characters with at least one letter. Names such as `3M Zambia Ltd.` and ordinary business punctuation are supported; numbers alone and markup are rejected. |
| Income | Positive decimal kwacha, at most two decimal places. A single digit such as `5` is valid and stored as `5.00`; no invented income eligibility threshold is applied. |
| Other repayment amount | Positive decimal kwacha, at most two decimal places, no greater than the current loan balance. |
| Mobile number | Demo mobile format beginning `07` or `09`, or equivalent `260` / `+260` format. Ordinary grouping spaces are removed. The repayment source must match the session's number. This is not an ownership or carrier-allocation check. |
| Demo bank reference | 4–24 ASCII letters/numbers with optional internal hyphens or slashes; must start/end in a letter or number. This is a demo rule, not a claimed Lipila contract. |

All answers reject hidden/control characters, multiple lines and oversized input. Existing
drafts are revalidated at submission, so saved answers from an older version cannot bypass
the current checks. Request/session/event references are restricted to portable identifier
characters. These checks apply when calling the HTTP API directly as well as in the browser.
Exact `0`, `00` and `99` remain reserved navigation commands, including on data-entry screens;
use `99.00` if an amount or income of 99 kwacha is intended.

## Persistence and integrity

- AES-GCM encrypted snapshots, temporary-file write, sync and replacement. Keep `demo.key`
  with `state.enc` to reopen saved data. The key beside the file is local development convenience,
  not production key management. Restrict access to the data directory.
- One process-wide transaction lock with rollback on failed writes. This is a single-process
  demonstration store, not a multi-instance or high-volume production database.
- Incremental inputs only: session ID, request ID and monotonic sequence. Exact retries return
  saved responses; altered request reuse and out-of-order requests are rejected. Submission,
  acceptance, payments, posting and reversals have separate references and duplicate guards.
- No PIN fields, prompts or hashes are implemented in this channel. Only the provider's separate
  payment interface handles mobile money PIN entry. Request fingerprints are keyed hashes.
  Upgrading the old PIN-based demo keeps business records, removes obsolete stored credential
  fields and invalidates cached sessions so old PIN screens cannot reappear.
- Provider outcomes, ledger posting and support cases stay separate. Initiation is not success.
  Late contradictory results create a case without overwriting the terminal result.
- Local API requests need the per-process token injected into the page. Host and Origin checks
  restrict localhost access. Anyone with workbench access can operate its synthetic staff panel.
  Do not expose this development server through a tunnel.
- Case teams are assigned. Numerical update deadlines remain **to be agreed**. SMS, provider
  settlement, callback verification and reconciliation scheduling are not represented as real
  completed operations.

## Local HTTP routes

These are simulator APIs, not a confirmed partner contract. `/api/*` requires `X-Demo-Token`.

| Route | Purpose |
| --- | --- |
| `GET /` | Embedded phone and demo operations panel |
| `POST /api/sessions` | `{session_id, phone, request_id, sequence, input}` returns `{session_id, sequence, screen_id, text, end, input_mode, max_length, input_hint}` |
| `GET /api/demo/state` | Synthetic operations state; excludes raw sessions |
| `POST /api/demo/actions` | `{action, reference, event_id}` for demo staff controls |
| `GET /healthz` | Local process health |

First dial uses an empty session ID and input, and sequence 1. The local phone field is simulator
input, not verified live identity. No gateway, cumulative input format or network limits are wired.

## Verification

```powershell
go test ./... -count=1
go vet ./...
go build ./cmd/ussd
```

Tests cover the HTTP-driven full journey, mobile money and bank simulations, unknown outcomes
blocking duplicate collections, unposted success, posting/reversal deduplication, late conflicting
events, encrypted persistence/restart, request conflicts, credential removal, session confirmation
expiry, stale drafts/offers/quotes, rollback and withdrawal racing payout. Validation tests cover
every numbered menu and data-entry question, direct HTTP bypass attempts, Unicode names,
numeric text rejection, boundary amounts, phone/reference formats and preservation of valid data.
`go test -race ./...` additionally requires a working CGO C toolchain.

## Deferred live work

When integration is requested: agree the gateway contract, trusted number and access controls,
network deadlines and character limits; connect authorised identity/LMS/customer APIs;
replace demo products and ledger; implement real KYC handoffs and notifications; integrate
contracted Lipila methods, verified callbacks, reconciliation and staff authorisation. Production
recovery approval, refunds, destination ownership checks, full legal terms, operating SLAs,
durable multi-instance storage, load testing and business UAT remain launch dependencies.
This local app does not claim completion of the production release gates.
