# REST API

Base URL: the organization's X-Client (`http://localhost:8080` Central Bank, `:8081` Bank A, `:8082` Bank B). Bodies are JSON.

**Who is calling.** Each X-Client acts only as its own organization (`default_msp_id`). Sending `X-Caller-MSP` for another organization returns `403`; call that organization's X-Client instead. In standalone mode (`allow_caller_header: true`), `X-Caller-MSP: Org1MSP` makes the request act as Bank A.

**Authentication.** If `server.api_token` is set, `/api/...` requires `Authorization: Bearer <token>`.

## Endpoints

| Method & path | Caller | Body | Result |
|---|---|---|---|
| `POST /api/v1/wallets` | Central Bank | `{walletId, ownerMsp, participantType}` | wallet record |
| `POST /api/v1/wallets/{id}/freeze` | Central Bank | `{reason}` | wallet record |
| `POST /api/v1/wallets/{id}/unfreeze` | Central Bank | – | wallet record |
| `GET  /api/v1/wallets/{id}` | anyone | – | wallet record |
| `GET  /api/v1/wallets/{id}/balance` | owner | – | `{walletId, settled, pendingCredits, available}` |
| `POST /api/v1/wallets/{id}/consolidate` | owner | – | `{walletId, consolidatedBalance}` |
| `POST /api/v1/movements/mint` | Central Bank | `{walletId, movementId, amount, salt}` | movement record |
| `POST /api/v1/movements/transfer` | owner of `fromWalletId` | `{fromWalletId, toWalletId, movementId, amount, salt}` | movement record |
| `POST /api/v1/movements/redeem` | owner | `{walletId, movementId, amount, salt}` | movement record |
| `GET  /api/v1/movements/{id}` | anyone | – | movement record |
| `POST /api/v1/movements/{id}/verify` | anyone | `{amount, salt}` | `{movementId, verified}` |
| `GET  /api/v1/supply` | Central Bank | – | `{totalSupply, entries}` |
| `GET  /healthz` | anyone, no token | – | `{status, mode, defaultEndorser, ready}`; `503` while the block sync is catching up |
| `POST /internal/v1/endorse` | peer X-Clients only | signed `EndorseRequest` | endorsements (see [ARCHITECTURE.md](ARCHITECTURE.md)) |

Field rules:
- `participantType`: `bank` for bank wallets; `central_bank` for wallets owned by `CentralBankMSP` (and only those).
- `amount`: integer > 0.
- `salt`: ≥ 16 characters. The public movement record stores `amountHash = SHA-256(movementId|amount|salt)`, never the amount. Share the salt with whoever should be able to verify the amount.
- `movementId`: unique across all mints, transfers and redemptions.

Behaviour:
- **Transfers do not credit the payee's balance directly.** They create a pending credit (visible as `pendingCredits`) that the payee folds in with `consolidate`. Any later transfer or redemption by the payee consolidates automatically first.
- `consolidate` with nothing pending returns `422 INVALID_STATE`.
- **Mutations wait for the ledger.** A `200` means committed, and reads immediately reflect it.
- **`?submit=false`** on any mutation returns the fully endorsed, signed envelope (`envelope`, base64 `common.Envelope`) without sending it to the orderer. The ledger is unchanged.

## Response format

Every successful response:
```json
{
  "txId": "a4610c51…",              // mutations only
  "status": "COMMITTED",            // COMMITTED | SUBMITTED | ENDORSED | QUERIED
  "payload": { … },                 // operation result (table above)
  "readWriteSet": {                 // per namespace: what was read and written
    "cbdc_org1_pdc": {
      "namespace": "cbdc_org1_pdc", "nsVersion": 0,
      "readWrites":  [{ "key": "BAL_WA", "version": 1, "value": "{…}" }],
      "blindWrites": [{ "key": "MOVEDETAIL_T2001", "value": "{…}" }]
    }
  },
  "endorsements": [                 // mutations only
    { "namespace": "cbdc_org1_pdc", "endorserMsp": "Org1MSP", "signature": "3045…" },
    { "namespace": "cbdc_org1_pdc", "endorserMsp": "CentralBankMSP", "signature": "3044…" }
  ],
  "envelope": "CpIK…"               // ?submit=false only
}
```

| `status` | HTTP | Meaning |
|---|---|---|
| `COMMITTED` | 200 | on the ledger |
| `SUBMITTED` | 202 | accepted by the routers, but no commit status within `commit_timeout`. Check later with `GET` |
| `ENDORSED` | 200 | `?submit=false`: signed, not sent |
| `QUERIED` | 200 | read-only call |

## Errors

```json
{ "error": "wallet WB is FROZEN", "code": "ACCOUNT_BLOCKED", "txId": "…" }
```
`txId` is present when a transaction reached the committer.

| HTTP | `code` | Cause / what to do |
|---|---|---|
| 400 | `INVALID_ARGUMENT` | malformed body, bad amount/salt, unknown participant type |
| 401 | `UNAUTHORIZED` | missing or wrong API token |
| 401 | `UNAUTHENTICATED` | (peer endpoint) request signature or certificate not trusted |
| 403 | `ACCESS_DENIED` | not the Central Bank / not the wallet owner / `X-Caller-MSP` for another org |
| 404 | `NOT_FOUND` | wallet or movement does not exist |
| 409 | `ALREADY_EXISTS`, `DUPLICATE_REQUEST` | wallet or movement ID already used |
| 409 | `ABORTED_MVCC_CONFLICT` | a concurrent transaction changed what this one read; retry |
| 409 | `PROPOSAL_MISMATCH` | the co-signer computed different changes (state lag after one retry, or tampering) |
| 422 | `INSUFFICIENT_BALANCE`, `ACCOUNT_BLOCKED`, `INVALID_STATE` | business rule (balance, frozen wallet, nothing to consolidate) |
| 422 | `NO_STATE_CHANGE` | the operation would write nothing |
| 502 | `ABORTED_SIGNATURE_INVALID` | endorsements do not satisfy the on-chain policy: client and on-chain policies differ ([POLICIES.md](POLICIES.md)) |
| 502 | other committer status (`MALFORMED_…`) | transaction rejected by the committer |
| 502 | `PEER_REJECTED` | co-signer failed unexpectedly |
| 503 | `PEER_UNREACHABLE` | co-signing X-Client is down or unreachable |
| 503 | `ENDORSEMENT_UNAVAILABLE` | no key, peer or policy configured for a required signature |
| 500 | `INTERNAL_ERROR` | Query Service or orderer unreachable, other infrastructure errors |

A co-signer's business-rule refusal is passed through with its own status and code. Normally the initiator hits the same rule first; a pass-through means the two organizations' ledger views differ.

## Example session (Central Bank :8080, Bank A :8081, Bank B :8082)

```bash
J='-H Content-Type:application/json'
curl -X POST localhost:8080/api/v1/wallets $J -d '{"walletId":"WA","ownerMsp":"Org1MSP","participantType":"bank"}'
curl -X POST localhost:8080/api/v1/wallets $J -d '{"walletId":"WB","ownerMsp":"Org2MSP","participantType":"bank"}'
curl -X POST localhost:8080/api/v1/movements/mint $J -d '{"walletId":"WA","movementId":"M1001","amount":1000000,"salt":"secret-salt-16-chars-min"}'
curl -X POST localhost:8081/api/v1/movements/transfer $J -d '{"fromWalletId":"WA","toWalletId":"WB","movementId":"T2001","amount":300000,"salt":"secret-salt-16-chars-min"}'
curl -X POST localhost:8082/api/v1/wallets/WB/consolidate
curl localhost:8082/api/v1/wallets/WB/balance     # settled 300000
```
