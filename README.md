# Fabric-X Endorser REST Client (`X-Client`) — 2-org network

A Fabric-X endorser, coordinator and submitter for the **Wholesale CBDC & Interbank Settlement** contract (`cbdc-wallet`), set up for a **two-organization network: Org1 and Org2** (the `2-org` network from [X-Setup](https://github.com/VeinyLebron/X-Setup)).

Each organization runs its own X-Client. A REST call to an organization's X-Client:

1. runs the CBDC rules against committed state read from that organization's committer,
2. builds the Fabric-X read/write set across the CBDC namespaces,
3. signs what that organization is responsible for, and asks the other organization's X-Client to re-check and co-sign where the policy requires it,
4. wraps the endorsed `applicationpb.Tx` in a signed envelope and broadcasts it to the Arma routers,
5. waits for the committer's verdict and returns `COMMITTED` (or the abort reason).

The namespace endorsement policies are enforced on-chain by the committer. A transaction that changes Org2's balance without both Org2's and Org1's signatures is rejected, whatever any client does.

> **Branches.** `two-org` (this branch): Org1 + Org2, network `X-Setup/2-org`. `main`: three organizations (Central Bank, Bank A, Bank B), network `X-Setup/3-org`. Same code; the branches differ in configuration and docs (plus the configurable issuer here).

## Roles

| Organization | X-Client | Role |
|---|---|---|
| **Org1** (`Org1MSP`) | `:8080` | **issuer** (creates and freezes wallets, mints, reads total supply) and a bank; co-signs Org2's balance changes |
| **Org2** (`Org2MSP`) | `:8081` | bank: transfers, redeems and consolidates its own wallets, with Org1's co-signature |

## Architecture

```
       Org1 app                                  Org2 app
          │ REST                                    │ REST
          ▼                                         ▼
  ┌────────────────┐   co-sign request      ┌────────────────┐
  │ X-Client Org1  │ ◀───────────────────── │ X-Client Org2  │
  │ :8080 (issuer) │  /internal/v1/endorse  │ :8081          │
  └───┬────────┬───┘                        └───┬────────┬───┘
    reads   envelope                          reads   envelope
      │        └──────▶ Arma routers ◀─────────────┘     │
      │                 7050 7150 7250 7350              │
      ▼                                                  ▼
  Org1 committer                                   Org2 committer
  Query Service 7001 · sidecar 4001                QS 7002 · sidecar 4002
```

How a co-signer decides to sign, called **peer re-execution**: the co-signer re-runs the same operation on its own copy of the ledger and signs only if it gets byte-identical changes.

## Namespaces and policies

| Namespace | Holds | Policy |
|---|---|---|
| `cbdc_public` | wallet records, movement records (no amounts) | `AND('Org1MSP.member')` |
| `cbdc_cb_pdc` | supply deltas | `AND('Org1MSP.member')` |
| `cbdc_credits` | pending incoming payments | `AND('Org1MSP.member')` |
| `cbdc_org1_pdc` | Org1 balances, audit copies | `AND('Org1MSP.member')` |
| `cbdc_org2_pdc` | Org2 balances, audit copies | `AND('Org2MSP.member','Org1MSP.member')` |

| Operation | Started at | Signed by |
|---|---|---|
| create / freeze / unfreeze wallet, mint | Org1 | Org1 |
| Org1 transfers, redeems, consolidates | Org1 | Org1 |
| Org2 transfers, redeems, consolidates | Org2 | Org2 + Org1 |

Payments are held as pending credits in `cbdc_credits`, so the payee does not have to be online. It folds them in with `consolidate`. To change these rules, change the on-chain policy with `scripts/update-policy.sh` and set the same rule under `endorsement_policies` in every X-Client config; the two must agree.

> Fabric-X has no private data: both committers store every namespace. `_pdc` restricts writes, not reads.

## Quick start

**Standalone** (no network; one process, throwaway keys for both orgs, in-memory ledger with committer rules):
```bash
make run          # or: docker compose up --build
curl -X POST localhost:8080/api/v1/wallets -H 'Content-Type: application/json' \
  -d '{"walletId":"WA","ownerMsp":"Org1MSP","participantType":"bank"}'
```
Use `-H 'X-Caller-MSP: Org2MSP'` to act as Org2.

**Against the 2-org network**: the network lives in **[X-Setup](https://github.com/VeinyLebron/X-Setup)**; clone it next to this repo. Full steps in **[SETUP.md](SETUP.md)**.
```bash
git clone https://github.com/VeinyLebron/X-Setup.git        # next to X-Client
cd X-Setup/2-org && make up                                # network + namespaces
cd ../../X-Client && ./scripts/copy-crypto.sh              # keys and certificates
docker compose -f docker-compose.2org.yaml up -d --build   # or: make run-org1 / make run-org2
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
config/                  config.yaml (standalone), org1.yaml, org2.yaml
scripts/copy-crypto.sh   copy keys/certs from X-Setup/2-org
scripts/update-policy.sh change a namespace's on-chain policy
docker-compose.yaml      standalone container
docker-compose.2org.yaml Org1 + Org2 containers on the devnet network
docs/                    documentation
```

## Development

```bash
make vet test     # go vet, gofmt check, go test -race ./...
```
The tests cover the contract lifecycle, multi-organization co-signing over HTTP (refusals, impostor keys, tampered proposals, peer outage), the 2-org issuer-is-a-bank layout, committer rules on the in-memory ledger, signatures, envelope format and the REST API. Requires Go 1.27.3+.
