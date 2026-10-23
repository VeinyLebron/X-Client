# Fabric-X Endorser REST Client (`X-Client`) — 3-org network

A Fabric-X endorser, coordinator and submitter for the **Wholesale CBDC & Interbank Settlement** contract (`cbdc-wallet`), set up for a **three-organization network: Central Bank, Bank A (Org1) and Bank B (Org2)** (A 3 org setup).

Each organization runs its own X-Client. A REST call to an organization's X-Client:

1. runs the CBDC rules against committed state read from that organization's committer,
2. builds the Fabric-X read/write set across the CBDC namespaces,
3. signs what that organization is responsible for, and asks the Central Bank's X-Client to re-check and co-sign the rest,
4. wraps the endorsed `applicationpb.Tx` in a signed envelope and broadcasts it to the Arma routers,
5. waits for the committer's verdict and returns `COMMITTED` (or the abort reason).

The namespace endorsement policies are enforced on-chain by the committer, so no organization can change a bank's balance on its own, whatever its X-Client does.

> **Branches.** `main` (this branch): Central Bank + Bank A + Bank B, network `X-Setup/3-org`. `two-org`: Org1 + Org2, network `X-Setup/2-org`, with Org1 as issuer. Same code; the branches differ in configuration and docs (`two-org` also makes the issuer configurable).

## Roles

| Organization | X-Client | Role |
|---|---|---|
| **Central Bank** (`CentralBankMSP`) | `:8080` | issuer and notary: creates and freezes wallets, mints, reads total supply, co-signs every transaction |
| **Bank A** (`Org1MSP`) | `:8081` | transfers, redeems and consolidates its own wallets, with the Central Bank's co-signature |
| **Bank B** (`Org2MSP`) | `:8082` | same, for its own wallets |

## Architecture

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

How a co-signer decides to sign, called **peer re-execution**: the Central Bank re-runs the same operation on its own copy of the ledger and signs only if it gets byte-identical changes.

## Namespaces and policies

Registered by `make up` in `X-Setup/3-org`:

| Namespace | Holds | Policy |
|---|---|---|
| `cbdc_public` | wallet records, movement records (no amounts) | `AND('CentralBankMSP.member')` |
| `cbdc_cb_pdc` | supply deltas, Central Bank balances | `AND('CentralBankMSP.member')` |
| `cbdc_credits` | pending incoming payments | `AND('CentralBankMSP.member')` |
| `cbdc_org1_pdc` | Bank A balances, audit copies | `AND('Org1MSP.member','CentralBankMSP.member')` |
| `cbdc_org2_pdc` | Bank B balances, audit copies | `AND('Org2MSP.member','CentralBankMSP.member')` |

| Operation | Started at | Signed by |
|---|---|---|
| create / freeze / unfreeze wallet, mint | Central Bank | Central Bank |
| transfer, redeem | paying bank | that bank + Central Bank |
| consolidate | owning bank | that bank + Central Bank |

Payments are held as pending credits in `cbdc_credits`, so **the receiving bank does not have to be online to be paid**. It folds them in with `consolidate`. To change these rules, change the on-chain policy with `scripts/update-policy.sh` and set the same rule under `endorsement_policies` in every X-Client config; the two must agree.

> Fabric-X has no private data: every committer stores every namespace. `_pdc` restricts writes, not reads.

## Quick start

**Standalone** (no network; one process, throwaway keys for all three orgs, in-memory ledger with committer rules):
```bash
make run          # or: docker compose up --build
```
`X-Caller-MSP` picks the acting organization (default `CentralBankMSP`).

**Against the 3-org network**: ; clone it next to this repo. Full steps in **[SETUP.md](SETUP.md)**.

```bash
# Bank A pays Bank B; Bank A's X-Client collects the Central Bank's co-signature.
curl -X POST localhost:8081/api/v1/movements/transfer -H 'Content-Type: application/json' \
  -d '{"fromWalletId":"WA","toWalletId":"WB","movementId":"T2001","amount":300000,"salt":"secret-salt-16-chars-min"}'
# → {"txId":"…","status":"COMMITTED","endorsements":[{"namespace":"cbdc_org1_pdc","endorserMsp":"Org1MSP",…},{…"CentralBankMSP"…}],…}
```

## Documentation

| Doc | Contents |
|---|---|
| [SETUP.md](SETUP.md) | step-by-step setup, end-to-end flow, troubleshooting |
| [docs/API.md](docs/API.md) | every endpoint, request and response format, error codes |

## Repository layout

```
cmd/server/              entry point, config loading
pkg/                     contract, simulator, engine, peers, endorser, fabricx, store, api
config/                  config.yaml (standalone), centralbank.yaml, bank_a.yaml, bank_b.yaml
scripts/copy-crypto.sh   copy keys/certs from X-Setup/3-org
scripts/update-policy.sh change a namespace's on-chain policy
docker-compose.yaml      standalone container
docker-compose.3org.yaml the three X-Clients on the X-Setup/3-org network
docs/                    documentation
```

## Development

```bash
make vet test     # go vet, gofmt check, go test -race ./...
```
The tests cover the contract lifecycle, a three-node multi-organization network over HTTP (co-signing, refusals, impostor keys, tampered proposals, Central Bank outage), committer rules on the in-memory ledger, signatures, envelope format and the REST API. Requires Go 1.27.3+.
