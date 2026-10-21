# Fabric-X Endorser REST Client (`X-Client`)

A dedicated **Fabric-X Endorser Client** for the **Wholesale CBDC & Interbank Settlement** chaincode (`cbdc-wallet`). It executes smart contract rules against the ledger state, builds deterministic multi-namespace **Fabric-X Read/Write Sets**, signs them cryptographically, and returns them over HTTP REST endpoints.

---

## 1. Architecture & Component Topology

In Hyperledger Fabric-X, nodes do not run chaincode containers. Instead, an **Endorser** simulates transactions against the world state and produces a signed **Read/Write Set**. The client does **not** commit transactions directly; rather, its response is the verified state transition proposal (the Read/Write set with version locks) ready to be sent to the Orderer and Committer.

```
                  ┌───────────────────────────────────────────┐
                  │          External Application / HTTP      │
                  └─────────────────────┬─────────────────────┘
                                        │ (HTTP REST / JSON)
                                        ▼ Port 8080
┌────────────────────────────────────────────────────────────────────────────────────────┐
│  X-Client (Fabric-X Endorser REST Service)                                             │
│                                                                                        │
│   • Multi-Namespace Simulator (tracks ReadsOnly, ReadWrites, BlindWrites, & versions)   │
│   • Wholesale CBDC Logic (All 12 operations from cbdc-wallet)                          │
│   • Cryptographic Endorsement Signer (ECDSA P-256 Low-S standard)                       │
└───────────────────────────────────────┬────────────────────────────────────────────────┘
                                        │ (gRPC committerpb.QueryService)
                                        ▼ Port 7001
                  ┌───────────────────────────────────────────┐
                  │   Query Service Container (Committer)     │
                  │   Exposes: GetRows, GetNamespacePolicies  │
                  └─────────────────────┬─────────────────────┘
                                        │ (SQL / PostgreSQL wire)
                                        ▼ Port 5432
                  ┌───────────────────────────────────────────┐
                  │       PostgreSQL / YugabyteDB Store       │
                  │       Committed ledger world state        │
                  └───────────────────────────────────────────┘
```

---

## 2. Port & Component Mapping

| Component | Default Port | Protocol | Description |
|---|---|---|---|
| **X-Client REST API** | `8080` | HTTP / JSON | REST API serving all 12 CBDC wallet operations |
| **Query Service** | `7001` | gRPC (`committerpb`) | Fabric-X Committer Query Service container for state reads |
| **Committer / Sidecar** | `4001` | gRPC (`protos.Deliver`) | Fabric-X Sidecar streaming blocks and commit statuses |
| **Arma Orderers (Routers 1-4)** | `7050`, `7150`, `7250`, `7350` | gRPC (`common.AtomicBroadcast`) | Fabric-X 4-party BFT orderer cluster |
| **PostgreSQL / YugabyteDB** | `5432` / `5433` | TCP / PostgreSQL | Underlying relational storage for world state |

---

## 3. Multi-Namespace Representation (Fabric-X PDC Mapping)

Classic Fabric uses Private Data Collections (PDCs) and transient maps for confidential balances. In Fabric-X, we partition these into distinct **Namespaces** with separate endorsement policies:

| Classic Fabric Collection | Fabric-X Namespace | Scope & Keys Stored |
|---|---|---|
| Public State | `cbdc_public` | Public wallet directory (`WALLET_<id>`), movement records (`MOVE_<id>`) |
| `CentralBankPDC` | `cbdc_cb_pdc` | Central Bank supply ledger (`SUPPLY~<movementId>`) |
| `<MSP>WholesalePDC` | `cbdc_<MSP>_pdc` | Confidential balances (`BAL_<id>`), incoming credits (`CREDIT~<id>~<moveId>`), movement details (`MOVEDETAIL_<id>`) |

---

## 4. Endorsement Response Structure

When any endpoint is invoked, X-Client executes the operation and returns the **Read/Write Set**:

```json
{
  "txId": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "status": "ENDORSED",
  "payload": {
    "movementId": "T1001",
    "status": "SETTLED",
    "amountHash": "a591a6d40bf420404a011733cfb7b190d62c65bf0bcda32b57b277d9ad9f146e"
  },
  "readWriteSet": {
    "cbdc_public": {
      "namespace": "cbdc_public",
      "nsVersion": 1,
      "readsOnly": [
        { "key": "WALLET_WA", "version": 2 },
        { "key": "WALLET_WB", "version": 1 }
      ],
      "blindWrites": [
        { "key": "MOVE_T1001", "value": "..." }
      ]
    },
    "cbdc_Org1MSP_pdc": {
      "namespace": "cbdc_Org1MSP_pdc",
      "nsVersion": 1,
      "readWrites": [
        { "key": "BAL_WA", "version": 5, "value": "..." }
      ]
    },
    "cbdc_Org2MSP_pdc": {
      "namespace": "cbdc_Org2MSP_pdc",
      "nsVersion": 1,
      "blindWrites": [
        { "key": "CREDIT~WB~T1001", "value": "..." }
      ]
    }
  },
  "endorsement": {
    "endorserMsp": "Org1MSP",
    "identity": "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n",
    "signature": "30450221008f...",
    "timestamp": "2026-10-21T11:45:00Z"
  }
}
```

---

## 5. Understanding the Flow: Endorsement (Simulation) vs. Ledger Commit

A common point of confusion is why calling `POST /api/v1/wallets` returns a success payload with `status: "ENDORSED"`, but an immediate `GET /api/v1/wallets/WA` returns `"wallet WA does not exist"`.

When you invoke any mutation endpoint on `X-Client`, `X-Client` acts as the **Endorser**:
1. It simulates the operation against the current ledger snapshot.
2. It constructs the exact **Read/Write Set** of proposed changes.
3. It cryptographically signs the Read/Write set with the organization's ECDSA private key.

1. **Step 1 (Endorsement)**: Provided by `X-Client`. Simulates the rules and yields the signed Read/Write set proposal.
2. **Step 2 (Ordering)**: The client or application forwards the signed envelope to the **Arma Orderer** (`localhost:7050`), which sequences transactions into blocks.
3. **Step 3 (Commit)**: The **Committer** validates that versions haven't changed (MVCC check) and commits the rows to PostgreSQL.

`GET` endpoints query the committed ledger state (Step 3). Until the proposal has traveled through Steps 2 and 3, `GET` will report that the key has not yet been committed to the database.

---

## 6. Quick Start & Execution

### Option A: Standalone Mode (In-Memory Engine, No external containers required)
```bash
cd /home/juicedcore/Projects/X-Client
go run cmd/server/main.go -config config/config.yaml
```

### Option B: gRPC Mode (Connecting to Query Service container on port 7001)
Update `config/config.yaml`:
```yaml
mode: "grpc"
query_service:
  endpoint: "localhost:7001"
```
Or run with environment variables:
```bash
MODE=grpc QUERY_SERVICE_ENDPOINT=localhost:7001 go run cmd/server/main.go
```

### Option C: Docker Compose
```bash
docker compose up --build
```

### Option D: 3-Organization Distributed Deployment
You can run 3 separate microservices representing each organization independently:
```bash
# Terminal 1: Central Bank (Port 8080)
./bin/x-client -config config/centralbank.yaml

# Terminal 2: Commercial Bank A (Port 8081)
./bin/x-client -config config/bank_a.yaml

# Terminal 3: Commercial Bank B (Port 8082)
./bin/x-client -config config/bank_b.yaml
```
Alternatively, the default unified `config/config.yaml` loads all 3 organization keys into a single service and automatically selects the appropriate signing identity based on the request's `X-Caller-MSP` header.

---

## 7. REST API Endpoints & cURL Examples

All endpoints accept an optional `X-Caller-MSP` header (defaults to the endorser's configured MSP).

### 1. Create Wallet (`CentralBankMSP` only)
```bash
curl -X POST http://localhost:8080/api/v1/wallets \
  -H "Content-Type: application/json" \
  -H "X-Caller-MSP: CentralBankMSP" \
  -d '{
    "walletId": "WA",
    "ownerMsp": "Org1MSP",
    "participantType": "bank"
  }'
```

### 2. Freeze Wallet (`CentralBankMSP` only)
```bash
curl -X POST http://localhost:8080/api/v1/wallets/WA/freeze \
  -H "Content-Type: application/json" \
  -H "X-Caller-MSP: CentralBankMSP" \
  -d '{"reason": "Regulatory compliance check"}'
```

### 3. Unfreeze Wallet (`CentralBankMSP` only)
```bash
curl -X POST http://localhost:8080/api/v1/wallets/WA/unfreeze \
  -H "X-Caller-MSP: CentralBankMSP"
```

### 4. Query Wallet (Public Metadata)
```bash
curl http://localhost:8080/api/v1/wallets/WA
```

### 5. Check Balance (Confidential PDC query, Owner only)
```bash
curl http://localhost:8080/api/v1/wallets/WA/balance \
  -H "X-Caller-MSP: Org1MSP"
```

### 6. Consolidate Balance (Fold pending credits into settled balance)
```bash
curl -X POST http://localhost:8080/api/v1/wallets/WA/consolidate \
  -H "X-Caller-MSP: Org1MSP"
```

### 7. Mint CBDC (`CentralBankMSP` only)
```bash
curl -X POST http://localhost:8080/api/v1/movements/mint \
  -H "Content-Type: application/json" \
  -H "X-Caller-MSP: CentralBankMSP" \
  -d '{
    "walletId": "WA",
    "movementId": "M1001",
    "amount": 1000000,
    "salt": "secret-salt-16-chars-min"
  }'
```

### 8. Transfer CBDC (Interbank Settlement)
```bash
curl -X POST http://localhost:8080/api/v1/movements/transfer \
  -H "Content-Type: application/json" \
  -H "X-Caller-MSP: Org1MSP" \
  -d '{
    "fromWalletId": "WA",
    "toWalletId": "WB",
    "movementId": "T2001",
    "amount": 250000,
    "salt": "random-transfer-salt-16"
  }'
```

### 9. Redeem CBDC (Burn from circulation)
```bash
curl -X POST http://localhost:8080/api/v1/movements/redeem \
  -H "Content-Type: application/json" \
  -H "X-Caller-MSP: Org1MSP" \
  -d '{
    "walletId": "WA",
    "movementId": "R3001",
    "amount": 100000,
    "salt": "redeem-salt-16-chars"
  }'
```

### 10. Query Movement (Public Record)
```bash
curl http://localhost:8080/api/v1/movements/T2001
```

### 11. Verify Movement Amount Commitment
```bash
curl -X POST http://localhost:8080/api/v1/movements/T2001/verify \
  -H "Content-Type: application/json" \
  -d '{
    "amount": 250000,
    "salt": "random-transfer-salt-16"
  }'
```

### 12. Query Total Supply (`CentralBankMSP` only)
```bash
curl http://localhost:8080/api/v1/supply \
  -H "X-Caller-MSP: CentralBankMSP"
```
For SETUP reference:
      [SETUP.md](SETUP.md)
