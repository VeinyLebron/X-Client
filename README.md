# Fabric-X Endorser REST Client (`X-Client`)

A Fabric-X endorser, coordinator and submitter for the **Wholesale CBDC & Interbank Settlement** contract (`cbdc-wallet`). Each organization (Central Bank, Bank A, Bank B) runs its own X-Client. A REST call to an organization's X-Client:

1. executes the CBDC rules against committed state read from that organization's committer,
2. builds the Fabric-X read/write set across the CBDC namespaces,
3. signs the namespaces that organization is responsible for, and asks the Central Bank's X-Client to re-execute and co-sign the rest,
4. wraps the fully endorsed `applicationpb.Tx` in a signed envelope and broadcasts it to the Arma routers,
5. waits for the committer's verdict and returns `COMMITTED` (or the abort reason).

The committer enforces the namespace endorsement policies on-chain, so no organization can change another bank's balance on its own, whatever its X-Client does.

---

## 1. Architecture

```
     Bank A app                 Central Bank app                Bank B app
         │ REST                        │ REST                        │ REST
         ▼                             ▼                             ▼
 ┌────────────────┐  co-sign   ┌────────────────┐   co-sign  ┌────────────────┐
 │ X-Client       │──────────▶ │ X-Client       │ ◀──────────│ X-Client       │
 │ Org1MSP  :8081 │  /internal │ CentralBankMSP │  /internal │ Org2MSP  :8082 │
 └───┬────────┬───┘            │          :8080 │            └───┬────────┬───┘
     │        │                └───┬────────┬───┘                │        │
   reads   envelope              reads   envelope              reads   envelope
     │        │                    │        │                    │        │
     │        └────────────────────┼────────┼──▶ Arma routers ◀──┼────────┘
     │                             │        └──▶ 7050 7150 7250 7350
     ▼                             ▼                             ▼
 Org1 committer               CentralBank committer          Org2 committer
 QS 7001 · sidecar 4001       QS 7003 · sidecar 4003         QS 7002 · sidecar 4002
```

* **Reads**: point lookups go to the organization's own Query Service. Prefix scans (pending credits, supply) use a key index that X-Client builds by following committed blocks from the sidecar, because the Query Service only serves point lookups.
* **Endorsement**: the initiating X-Client signs its own namespaces. For every other required organization it sends `{op, request, txId, timestamp, proposed namespaces}` to that organization's X-Client, signed with the initiator's MSP identity. The peer checks the certificate against the initiator's MSP CA, **re-executes the operation on its own view of the ledger**, and signs only if the bytes are identical.
* **Submission**: the envelope (`MESSAGE` header, Fabric-style `txId = SHA-256(nonce ‖ creator)`) goes to every router. X-Client then polls `GetTransactionStatus` until the committer reports a final status.

## 2. Namespaces and endorsement policies

Registered by `make init-namespace-cbdc` in `fabric-x-samples/devnet-3org`:

| Namespace | Keys | Policy |
|---|---|---|
| `cbdc_public` | `WALLET_<id>`, `MOVE_<id>` | `AND('CentralBankMSP.member')` |
| `cbdc_cb_pdc` | `SUPPLY~<move>` (and Central Bank balances) | `AND('CentralBankMSP.member')` |
| `cbdc_credits` | `CREDIT~<wallet>~<move>` pending incoming credits | `AND('CentralBankMSP.member')` |
| `cbdc_org1_pdc` | `BAL_<id>`, `MOVEDETAIL_<id>` for Bank A | `AND('Org1MSP.member','CentralBankMSP.member')` |
| `cbdc_org2_pdc` | `BAL_<id>`, `MOVEDETAIL_<id>` for Bank B | `AND('Org2MSP.member','CentralBankMSP.member')` |

The Central Bank acts as notary and co-signs every transaction. A bank additionally signs anything that touches its own balances. Incoming credits live in `cbdc_credits`, so **the receiving bank does not have to be online to be paid**. It folds credits into its balance with `consolidate`.

| Operation | Initiated at | Signed by |
|---|---|---|
| create / freeze / unfreeze wallet, mint | Central Bank | Central Bank |
| transfer A → B, redeem | Bank A | Bank A + Central Bank |
| consolidate | owning bank | owning bank + Central Bank |

> Fabric-X has no private data: every committer stores every namespace. The `_pdc` namespaces restrict who may **write**, not who may read. Amounts are visible in the read/write set to any network member.

## 3. Ports

| Component | Port |
|---|---|
| X-Client Central Bank / Bank A / Bank B | `8080` / `8081` / `8082` |
| Query Service Org1 / Org2 / Central Bank | `7001` / `7002` / `7003` |
| Sidecar Org1 / Org2 / Central Bank | `4001` / `4002` / `4003` |
| Arma routers | `7050`, `7150`, `7250`, `7350` |

## 4. Quick start

### Standalone (in-memory ledger, no network)
One process holds throwaway keys for all three organizations and commits locally with the same MVCC rules as the committer. `X-Caller-MSP` picks the acting organization.
```bash
make run                                 # or: docker compose up --build
```

### Against `devnet-3org`
See [SETUP.md](SETUP.md). In short:
```bash
cd ../repos-bench/fabric-x-samples/devnet-3org
make init && make start-3org && make init-namespace-cbdc
cd -; ./scripts/copy-crypto.sh
make run-centralbank & make run-bank-a & make run-bank-b    # or: docker compose -f docker-compose.3org.yaml up -d --build
```

## 5. REST API

Each per-organization X-Client acts **only as its own MSP**. Sending `X-Caller-MSP` for another organization returns `403`; send the request to that organization's X-Client instead. Set `server.api_token` (or `API_TOKEN`) to require `Authorization: Bearer <token>` on `/api` routes.

| Method & path | Who | Effect |
|---|---|---|
| `POST /api/v1/wallets` | Central Bank | create wallet `{walletId, ownerMsp, participantType}` |
| `POST /api/v1/wallets/{id}/freeze` | Central Bank | `{reason}` |
| `POST /api/v1/wallets/{id}/unfreeze` | Central Bank | |
| `GET  /api/v1/wallets/{id}` | anyone | public wallet record |
| `GET  /api/v1/wallets/{id}/balance` | owner | settled + pending credits |
| `POST /api/v1/wallets/{id}/consolidate` | owner | fold pending credits into the balance |
| `POST /api/v1/movements/mint` | Central Bank | `{walletId, movementId, amount, salt}` |
| `POST /api/v1/movements/transfer` | payer bank | `{fromWalletId, toWalletId, movementId, amount, salt}` |
| `POST /api/v1/movements/redeem` | owner | `{walletId, movementId, amount, salt}` |
| `GET  /api/v1/movements/{id}` | anyone | public movement record |
| `POST /api/v1/movements/{id}/verify` | anyone | `{amount, salt}` → checks the amount commitment |
| `GET  /api/v1/supply` | Central Bank | total CBDC in circulation |
| `GET  /healthz` | anyone | liveness, and readiness of the block sync |

Mutations accept `?submit=false` to return the fully endorsed, signed envelope (base64) without ordering it.

```bash
# Bank A pays Bank B; Bank A's X-Client collects the Central Bank's co-signature.
curl -X POST localhost:8081/api/v1/movements/transfer -H 'Content-Type: application/json' \
  -d '{"fromWalletId":"WA","toWalletId":"WB","movementId":"T2001","amount":300000,"salt":"secret-salt-16-chars-min"}'
```
```json
{
  "txId": "265551c1e7e0…",
  "status": "COMMITTED",
  "payload": { "movementId": "T2001", "type": "TRANSFER", "amountHash": "…", "status": "SETTLED", … },
  "readWriteSet": { "cbdc_org1_pdc": { "readWrites": [{ "key": "BAL_WA", "version": 1, "value": "…" }], … }, … },
  "endorsements": [
    { "namespace": "cbdc_org1_pdc", "endorserMsp": "Org1MSP", "signature": "3045…" },
    { "namespace": "cbdc_org1_pdc", "endorserMsp": "CentralBankMSP", "signature": "3044…" },
    …
  ]
}
```

`status` is `COMMITTED`, `ENDORSED` (`submit=false`), `SUBMITTED` (`202`: ordered, no verdict before `commit_timeout`) or `QUERIED`. Errors return `{error, code, txId?}`:

| HTTP | `code` |
|---|---|
| 400 | `INVALID_ARGUMENT` |
| 401 | `UNAUTHORIZED` (API token), `UNAUTHENTICATED` (peer request signature) |
| 403 | `ACCESS_DENIED` |
| 404 | `NOT_FOUND` |
| 409 | `ALREADY_EXISTS`, `DUPLICATE_REQUEST`, `ABORTED_MVCC_CONFLICT` (retry), `PROPOSAL_MISMATCH` |
| 422 | `INSUFFICIENT_BALANCE`, `ACCOUNT_BLOCKED`, `INVALID_STATE`, `NO_STATE_CHANGE` |
| 502 | committer rejected the transaction (status name as code), `PEER_REJECTED` |
| 503 | `PEER_UNREACHABLE`, `ENDORSEMENT_UNAVAILABLE`, `/healthz` not ready |

## 6. Configuration

`config/centralbank.yaml`, `config/bank_a.yaml` and `config/bank_b.yaml` target devnet-3org from the host. `${VAR:-default}` references let `docker-compose.3org.yaml` swap in container endpoints. Main keys:

| Key | Meaning |
|---|---|
| `mode` | `grpc` (network) or `in-memory` |
| `default_msp_id`, `endorsers` | the MSP this instance acts as and its signing key (missing or mismatched keys are a startup error) |
| `query_service`, `sidecar`, `orderers` | endpoints and `tls` (`mode: none\|tls\|mtls`, `ca_cert_paths`, `cert_path`, `key_path`, `server_name`) |
| `peers` | other organizations' X-Client URLs and MSP CA certificates (who may ask this node to co-sign, and whom it asks) |
| `namespaces`, `endorsement_policies` | namespace layout and required signers. Must match the on-chain policies |
| `commit_timeout`, `server.api_token`, `allow_caller_header` | |

## 7. Development

```bash
make vet test        # go vet, gofmt check, go test -race ./...
```
The tests cover the contract lifecycle, a three-node multi-organization network over HTTP (co-signing, refusal, impostor keys, tampered proposals, CB outage), MVCC and committer rules on the in-memory ledger, signatures, the envelope format, and the REST API.

```
cmd/server/      config loading and wiring
pkg/contract/    CBDC rules (deterministic, typed errors)
pkg/simulator/   read/write capture → applicationpb.TxNamespace
pkg/engine/      execute → endorse (local + peers) → envelope → submit
pkg/peers/       signed endorsement protocol between X-Clients
pkg/endorser/    MSP identities, Fabric-X namespace signatures
pkg/fabricx/     envelope assembly, router broadcast, commit status
pkg/store/       Query Service store, block-fed key index, in-memory ledger
pkg/api/         REST handlers
```
