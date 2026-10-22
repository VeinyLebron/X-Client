# Setup Guide: 2-org devnet (Org1 + Org2) + X-Client

End-to-end setup from a fresh clone. For the 3-organization network, see the `main` branch.

## 1. What runs where

| Component | Provided by | Port(s) |
|---|---|---|
| Arma orderer, 4 parties | `fabric-x-samples/devnet` | routers `7050` `7150` `7250` `7350` |
| Org1 committer (Query Service, sidecar) | devnet `compose.yaml` | `7001`, `4001` |
| Org2 committer | devnet `compose.org2.yaml` | `7002`, `4002` |
| X-Client Org1 (issuer + bank) | this repo, `config/org1.yaml` | `8080` |
| X-Client Org2 (bank) | this repo, `config/org2.yaml` | `8081` |

The devnet is used unmodified. The `devnet-3org` network from the `main` branch uses the same container names and ports, so stop it before starting this one (`make stop` in `devnet-3org`).

## 2. Prerequisites

- Linux or macOS, Go 1.27.3+, Docker with Compose, your user in the `docker` group (log out and in after adding it)
- `fabric-x-samples` cloned; the devnet is `fabric-x-samples/devnet`
- `nc` (netcat), which the devnet's `make start-both` uses to wait for ports: `sudo pacman -S openbsd-netcat` or `sudo apt install netcat-openbsd`. Without it the command never returns (see Troubleshooting).

## 3. Start the network and register the namespaces

```bash
cd /path/to/fabric-x-samples/devnet

make init          # only if devnet/crypto does not exist yet (it deletes and regenerates crypto)
make start-both    # 4-party orderer + Org1 and Org2 committers (28 containers)
```

Register the five CBDC namespaces with their endorsement policies:
```bash
make init-namespace NS=cbdc_public   POLICY="AND('Org1MSP.member')"
make init-namespace NS=cbdc_cb_pdc   POLICY="AND('Org1MSP.member')"
make init-namespace NS=cbdc_credits  POLICY="AND('Org1MSP.member')"
make init-namespace NS=cbdc_org1_pdc POLICY="AND('Org1MSP.member')"
make init-namespace NS=cbdc_org2_pdc POLICY="AND('Org2MSP.member','Org1MSP.member')"

make list-namespaces
```
Expected: `Installed namespaces (5 total)`, each at `version 0`. Creating a namespace that already exists is skipped. To use different rules, see [docs/POLICIES.md](docs/POLICIES.md) before registering.

## 4. Copy crypto material

```bash
cd /path/to/X-Client
./scripts/copy-crypto.sh /path/to/fabric-x-samples/devnet
```

| Destination | Contents |
|---|---|
| `crypto/org1/`, `crypto/org2/` | User1 signing identity (`cert.pem`, `priv_sk`), org TLS CA and TLS client keypair (`tls/`) |
| `crypto/cas/` | each org's public MSP CA, used to authenticate the other X-Client |
| `crypto/orderer-tls/` | orderer TLS CAs, used to verify the routers |

Everything under `crypto/` except the READMEs is git-ignored. Re-run the script after every `make init` in the devnet.

## 5. Run the two X-Clients

On the host, in two terminals:
```bash
make run-org1     # :8080, reads from the Org1 committer (7001 / 4001)
make run-org2     # :8081, reads from the Org2 committer (7002 / 4002)
```
Or as containers on the devnet's `fabric-x` network:
```bash
docker compose -f docker-compose.2org.yaml up -d --build
```

Each prints its policies and peer at startup, then `synchronizer ready at block N`. Check:
```bash
curl localhost:8080/healthz   # {"defaultEndorser":"Org1MSP","mode":"grpc","ready":true,"status":"UP"}
curl localhost:8081/healthz   # {"defaultEndorser":"Org2MSP",...,"ready":true,...}
```

## 6. End-to-end flow

```bash
J='-H Content-Type:application/json'

# Org1 (issuer) opens one wallet per org and mints 1,000,000 into its own
curl -X POST localhost:8080/api/v1/wallets $J -d '{"walletId":"WA","ownerMsp":"Org1MSP","participantType":"bank"}'
curl -X POST localhost:8080/api/v1/wallets $J -d '{"walletId":"WB","ownerMsp":"Org2MSP","participantType":"bank"}'
curl -X POST localhost:8080/api/v1/movements/mint $J -d '{"walletId":"WA","movementId":"M1001","amount":1000000,"salt":"secret-salt-16-chars-min"}'

# Org1 pays Org2 300,000 (Org1 signs alone)
curl -X POST localhost:8080/api/v1/movements/transfer $J -d '{"fromWalletId":"WA","toWalletId":"WB","movementId":"T2001","amount":300000,"salt":"secret-salt-16-chars-min"}'

# Org2 folds the payment into its balance (Org2 + Org1 sign; Org1's X-Client co-signs automatically)
curl -X POST localhost:8081/api/v1/wallets/WB/consolidate

# Org2 pays Org1 100,000; Org1 consolidates
curl -X POST localhost:8081/api/v1/movements/transfer $J -d '{"fromWalletId":"WB","toWalletId":"WA","movementId":"T2002","amount":100000,"salt":"secret-salt-16-chars-min"}'
curl -X POST localhost:8080/api/v1/wallets/WA/consolidate

curl localhost:8080/api/v1/wallets/WA/balance   # settled 800000
curl localhost:8081/api/v1/wallets/WB/balance   # settled 200000
curl localhost:8080/api/v1/supply               # totalSupply 1000000

# Org2 redeems 50,000 (burned; supply drops to 950000)
curl -X POST localhost:8081/api/v1/movements/redeem $J -d '{"walletId":"WB","movementId":"R3001","amount":50000,"salt":"redeem-salt-16-chars"}'

# Anyone holding the salt can check an amount
curl -X POST localhost:8081/api/v1/movements/T2001/verify $J -d '{"amount":300000,"salt":"secret-salt-16-chars-min"}'
```

Every mutation returns `"status": "COMMITTED"` and lists its endorsements. The Org2 consolidate shows `Org2MSP` and `Org1MSP` on `cbdc_org2_pdc`. Response formats are in [docs/API.md](docs/API.md).

Expected refusals:

| Try | Result |
|---|---|
| Org2 mints (`POST :8081/api/v1/movements/mint`) | `403 ACCESS_DENIED` |
| Org2 spends `WA` | `403 ACCESS_DENIED` |
| `X-Caller-MSP: Org1MSP` sent to `:8081` | `403` |
| Org2 transfers while Org1's X-Client is down | `503 PEER_UNREACHABLE` |
| transfer to a frozen wallet | `422 ACCOUNT_BLOCKED` |
| a write to `cbdc_org2_pdc` signed by only one org, submitted straight to the routers | committer aborts it: `ABORTED_SIGNATURE_INVALID` |

## 7. Standalone mode

No network: `make run` (or `docker compose up --build`) starts one in-memory X-Client on `:8080` holding throwaway keys for both orgs. Add `-H 'X-Caller-MSP: Org2MSP'` to act as Org2. The flow above works with every port set to 8080.

## 8. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `make start-both` never returns | `nc` is not installed, so the port-wait loop spins forever. The containers are up anyway: check `docker ps` shows 28 healthy, then press Ctrl-C. Better: install netcat. |
| `make start-both`: "port 7050 is already in use" | another devnet (e.g. `devnet-3org`) is running; `make stop` there |
| `permission denied … docker.sock` | your user is not in the `docker` group, or the session predates it: log out and in, or `newgrp docker` |
| X-Client exits: `failed to read CA cert crypto/org1/tls/ca.crt` | run `./scripts/copy-crypto.sh` |
| X-Client exits: `private key does not match certificate` | stale crypto after `make init`; re-run `copy-crypto.sh` |
| `/healthz` `"ready": false` | key index still replaying blocks, or sidecar unreachable; see `readyError` |
| `502 ABORTED_SIGNATURE_INVALID` | X-Client collects fewer signatures than the on-chain policy needs; see [docs/POLICIES.md §2](docs/POLICIES.md#2-two-places-that-must-agree) |
| `503 ENDORSEMENT_UNAVAILABLE` | the two X-Clients' `endorsement_policies` differ, or a peer is missing from `peers:` |
| `409 ABORTED_MVCC_CONFLICT` | a concurrent transaction touched the same keys; retry |
| `500 … namespace cbdc_x is not registered` | namespace not created (step 3) or wrong name in `namespaces:` |
| everything fails after `make purge` | the ledger was wiped: register the namespaces again (step 3) |

## 9. Tear down

```bash
docker compose -f docker-compose.2org.yaml down   # if the X-Clients run in Docker
cd /path/to/fabric-x-samples/devnet
make stop     # stop, keep the ledger
make purge    # stop and wipe the ledger (namespaces must be re-registered)
```
