# Setup Guide: 3-org network (X-Setup) + X-Client

End-to-end setup of the three-organization wholesale CBDC network (Central Bank, Bank A, Bank B) from a fresh clone. For a two-organization network (Org1 + Org2 on the stock `devnet`), use the `two-org` branch.

---

## 1. Who runs what

| Component | Provided by | Port(s) |
|---|---|---|
| Arma orderer, 4 parties | `X-Setup/3-org` | routers `7050` `7150` `7250` `7350` |
| Org1 (Bank A) committer: Query Service, sidecar | X-Setup/3-org `compose.yaml` | `7001`, `4001` |
| Org2 (Bank B) committer | X-Setup/3-org `compose.org2.yaml` | `7002`, `4002` |
| Central Bank committer | X-Setup/3-org `compose.centralbank.yaml` | `7003`, `4003` |
| X-Client per organization | this repo | `8080` CB, `8081` Bank A, `8082` Bank B |

The network comes from **[X-Setup](https://github.com/VeinyLebron/X-Setup)** (`3-org/`): the upstream Fabric-X `devnet` plus a third peer organization, **`CentralBankMSP`** (`centralbank.example.com`), with one-command `make up`. Fabric-X networks share container names and the `fabric-x` network, so **run only one at a time**.

## 2. Prerequisites

- Linux or macOS, **Go 1.27.3+**, Docker with Compose (your user in the `docker` group)
- X-Setup cloned **next to** this repo (`git clone https://github.com/VeinyLebron/X-Setup.git`), so that `../X-Setup/3-org` exists
- No `nc` needed: X-Setup's targets wait for ports with bash.

## 3. Start the network

```bash
cd ../X-Setup/3-org
make up          # crypto (first run), orderers + three committer stacks (34 containers), namespaces
make status      # containers and the 5 namespaces at version 0
```

`make up` is the same as running `make init` (first run), `make start-3org`, waiting until all containers are healthy, and `make init-namespace-cbdc`. It registers:

| Namespace | Policy |
|---|---|
| `cbdc_public`, `cbdc_cb_pdc`, `cbdc_credits` | `AND('CentralBankMSP.member')` |
| `cbdc_org1_pdc` | `AND('Org1MSP.member','CentralBankMSP.member')` |
| `cbdc_org2_pdc` | `AND('Org2MSP.member','CentralBankMSP.member')` |

To use different rules, change the on-chain policy with `scripts/update-policy.sh` and set the same rule under `endorsement_policies` in every X-Client config; the two must agree.

## 4. Copy crypto material

```bash
cd ../../X-Client
./scripts/copy-crypto.sh          # defaults to ../X-Setup/3-org; or pass the network folder
```

For each organization this copies the User1 signing identity (`crypto/<org>/cert.pem`, `priv_sk`), the org TLS CA and the user TLS client keypair (`crypto/<org>/tls/`). It also copies the public MSP CA of every organization (`crypto/cas/`) and the orderer TLS CAs (`crypto/orderer-tls/`). Everything under `crypto/` except the READMEs is git-ignored.

## 5. Run the three X-Clients

On the host, in three terminals:
```bash
make run-centralbank   # :8080, reads from the CB committer (7003/4003)
make run-bank-a        # :8081, reads from the Org1 committer (7001/4001)
make run-bank-b        # :8082, reads from the Org2 committer (7002/4002)
```
or as containers on the devnet network:
```bash
docker compose -f docker-compose.3org.yaml up -d --build
```

Each prints its policies and peers, then `synchronizer ready at block N` once its key index has caught up. `curl localhost:808{0,1,2}/healthz` reports `"ready": true`.

## 6. End-to-end flow

```bash
J='-H Content-Type:application/json'

# Central Bank (8080) opens wallets and mints
curl -X POST localhost:8080/api/v1/wallets $J -d '{"walletId":"WA","ownerMsp":"Org1MSP","participantType":"bank"}'
curl -X POST localhost:8080/api/v1/wallets $J -d '{"walletId":"WB","ownerMsp":"Org2MSP","participantType":"bank"}'
curl -X POST localhost:8080/api/v1/movements/mint $J -d '{"walletId":"WA","movementId":"M1001","amount":1000000,"salt":"secret-salt-16-chars-min"}'

# Committed state is readable immediately
curl localhost:8081/api/v1/wallets/WA/balance          # pendingCredits 1000000

# Bank A (8081) pays Bank B; the Central Bank X-Client co-signs automatically
curl -X POST localhost:8081/api/v1/movements/transfer $J -d '{"fromWalletId":"WA","toWalletId":"WB","movementId":"T2001","amount":300000,"salt":"secret-salt-16-chars-min"}'

# Bank B (8082) folds the incoming credit into its balance
curl -X POST localhost:8082/api/v1/wallets/WB/consolidate
curl localhost:8082/api/v1/wallets/WB/balance          # settled 300000

# Bank A redeems; the Central Bank checks supply
curl -X POST localhost:8081/api/v1/movements/redeem $J -d '{"walletId":"WA","movementId":"R3001","amount":100000,"salt":"redeem-salt-16-chars"}'
curl localhost:8080/api/v1/supply                      # totalSupply 900000

# Anyone can check an amount commitment
curl -X POST localhost:8081/api/v1/movements/T2001/verify $J -d '{"amount":300000,"salt":"secret-salt-16-chars-min"}'
```

Every mutation returns `"status": "COMMITTED"` with the endorsements it carried. For example, the transfer shows `Org1MSP` and `CentralBankMSP` on `cbdc_org1_pdc`. Response formats are in [docs/API.md](docs/API.md).

Expected refusals:
- Bank B's X-Client debiting `WA` → `403 ACCESS_DENIED`
- `X-Caller-MSP: CentralBankMSP` sent to Bank A's X-Client → `403`
- transfer while the Central Bank X-Client is down → `503 PEER_UNREACHABLE`
- a transaction that writes Bank A's balance without Bank A's signature, submitted straight to the routers → the committer aborts it with `ABORTED_SIGNATURE_INVALID`

## 7. Standalone mode

No network at all: `make run` (or `docker compose up --build`) starts one in-memory X-Client holding throwaway keys for all three organizations on `:8080`. Use `X-Caller-MSP` to choose the acting organization. The flow above works unchanged, with every port set to 8080.

## 8. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `make start-3org`: "port 7050 is already in use" | another devnet (e.g. the stock `devnet`) is running; `make stop` there |
| `permission denied … docker.sock` | your user is not in the `docker` group, or the session predates it: log out and in, or `newgrp docker` |
| X-Client exits: `failed to read CA cert crypto/centralbank/tls/ca.crt` | run `./scripts/copy-crypto.sh` |
| X-Client exits: `private key does not match certificate` | stale crypto after `make init`; re-run `copy-crypto.sh` |
| `/healthz` `"ready": false` | key index still replaying blocks, or sidecar unreachable; see `readyError` |
| `502 ABORTED_SIGNATURE_INVALID` | X-Client collects fewer signatures than the on-chain policy needs: the on-chain policy and every config's `endorsement_policies` must match |
| `503 ENDORSEMENT_UNAVAILABLE` | the X-Clients' `endorsement_policies` differ, or a required MSP is missing from `peers:` |
| `503 PEER_UNREACHABLE` | the Central Bank X-Client is down or its URL in `peers:` is wrong |
| `409 ABORTED_MVCC_CONFLICT` | a concurrent transaction touched the same keys; retry |
| `500 … namespace cbdc_x is not registered` | run `make init-namespace-cbdc`, or fix `namespaces:` |
| everything fails after `make purge` | the ledger was wiped: run `make init-namespace-cbdc` again |

## 9. Tear down

```bash
docker compose -f docker-compose.3org.yaml down     # if X-Clients run in Docker
cd ../X-Setup/3-org
make down     # stop, keep the ledger
make reset    # stop and delete ledger and crypto (then re-run copy-crypto.sh after the next make up)
```
