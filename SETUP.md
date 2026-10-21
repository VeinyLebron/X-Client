# Complete Setup Guide: Fabric-X Sample Network & X-Client

This guide walks through setting up the complete end-to-end environment from scratch for anyone cloning this repository.

---

## 1. Architectural Overview:

Before starting, it is crucial to understand the division of labor between the **Fabric-X Sample Network** and **X-Client**:

| Component | Provided By | Responsibilities | Default Port |
|---|---|---|---|
| **Query Service (Org 1 / Bank A)** | **Fabric-X Sample Network** | gRPC read gateway in front of Org 1's PostgreSQL DB | `7001` |
| **Query Service (Org 2 / Bank B)** | **Fabric-X Sample Network** | gRPC read gateway in front of Org 2's PostgreSQL DB | `7002` |
| **Arma Orderers (Routers 1-4)** | **Fabric-X Sample Network** | 4-party BFT consensus cluster ordering transactions | `7050`, `7150`, `7250`, `7350` |
| **Committer & Sidecar** | **Fabric-X Sample Network** | Validates MVCC version locks and writes blocks to DB | `4001` |
| **PostgreSQL Database** | **Fabric-X Sample Network** | Stores committed ledger world state rows | Internal to Docker |
| **X-Client (This Repo)** | **You / Application Owner** | Executes CBDC business rules & produces signed Read/Write sets | `8080` (or `8081`, `8082`) |

> **Key Takeaway**:  
> - The sample network provides the blockchain infrastructure (Orderers, Committers, Query Services, and Databases).  
> - `X-Client` connects to the **Query Service** over gRPC mTLS to read state, executes the smart contract logic in memory, and returns the cryptographically-signed **Read/Write Set**.

---

## 2. Prerequisites

- **Linux / macOS** environment
- **Go 1.22+** (tested with Go 1.26+)
- **Docker & Docker Compose** (ensure your user belongs to the `docker` group)
- A checkout of the upstream [**`hyperledger/fabric-x-samples`**](https://github.com/hyperledger/fabric-x-samples) repository

---

## 3. Step-by-Step Deployment Instructions

### Step 1: Start the Fabric-X Sample Network

Navigate to the `devnet` folder inside your `fabric-x-samples` clone:

```bash
cd /path/to/fabric-x-samples/devnet

# 1. Generate genesis block, crypto materials, and configuration
make init

# 2. Start the orderers, committer, and Query Service containers
make start
```

*Verification: Ensure the 4 Arma orderers (port `7050`), Sidecar (port `4001`), and Query Service (port `7001`) are up and healthy:*
```bash
docker ps
```

---

### Step 2: Register the 4 CBDC Namespaces

In Fabric-X, tables in PostgreSQL are only created after namespaces are registered with a channel policy. Run these 4 commands in `devnet/`:

```bash
cd /path/to/fabric-x-samples/devnet

# Public wallet directory & movement commitments
make init-namespace NS=cbdc_public  POLICY="OR('Org1MSP.member','Org2MSP.member')"

# Central Bank confidential supply ledger
make init-namespace NS=cbdc_cb_pdc   POLICY="OR('Org1MSP.member','Org2MSP.member')"

# Bank A (Org1) confidential balance and credits
make init-namespace NS=cbdc_org1_pdc POLICY="OR('Org1MSP.member','Org2MSP.member')"

# Bank B (Org2) confidential balance and credits
make init-namespace NS=cbdc_org2_pdc POLICY="OR('Org1MSP.member','Org2MSP.member')"
```

*Verification: List installed namespaces:*
```bash
make list-namespaces
```
You should see all 4 namespaces installed with `version 0`.

---

### Step 3: Copy Crypto Credentials into `X-Client`

`X-Client` needs the certificates and keys generated in Step 1 to sign endorsements and authenticate over mTLS to the Query Service.

Run the included automated helper script from the `X-Client` root:

```bash
cd /path/to/X-Client

# Run helper script pointing to your devnet directory
./scripts/copy-crypto.sh /path/to/fabric-x-samples/devnet
```

This automatically extracts and configures:
- **`crypto/centralbank/`**: Central Bank admin signing certificate and private key.
- **`crypto/bank_a/`**: Org1 (Bank A) user certificate, private key, and mTLS client keypair.
- **`crypto/bank_b/`**: Org2 (Bank B) user certificate, private key, and mTLS client keypair.

---

### Step 4: Build & Launch `X-Client`

Build the binary:
```bash
cd /path/to/X-Client
make build
```

You now have two options to run `X-Client`:

#### Option A: Single Unified Service (Recommended for testing / API gateways)
Loads credentials for Central Bank, Bank A, and Bank B into a single service, automatically using the correct signing key based on the `X-Caller-MSP` HTTP header:

```bash
./bin/x-client -config config/config.yaml
```
*Listens on `http://localhost:8080`.*

#### Option B: 3-Organization Distributed Deployment (Real production topology)
Start three independent microservices on distinct ports, each representing an isolated organization:

```bash
# Terminal 1: Central Bank Endorser (Port 8080)
./bin/x-client -config config/centralbank.yaml

# Terminal 2: Commercial Bank A Endorser (Port 8081)
./bin/x-client -config config/bank_a.yaml

# Terminal 3: Commercial Bank B Endorser (Port 8082)
./bin/x-client -config config/bank_b.yaml
```

---

## 4. Understanding the Transaction Flow: Endorse vs. Commit

When you make a request to `X-Client`:

1. **`POST` Endpoints Return `status: "ENDORSED"`**:
   ```bash
   curl -X POST http://localhost:8080/api/v1/wallets \
     -H "Content-Type: application/json" \
     -H "X-Caller-MSP: CentralBankMSP" \
     -d '{"walletId":"WA","ownerMsp":"Org1MSP","participantType":"bank"}'
   ```
   - `X-Client` connects to the Query Service (`localhost:7001`).
   - Simulates the wallet creation.
   - Outputs the **Read/Write Set** (`WALLET_WA` in `cbdc_public`, `BAL_WA` in `cbdc_org1_pdc`).
   - Cryptographically signs the Read/Write set with Central Bank's private key.
   - Returns the signed proposal.

2. **Why `GET` Reads Committed State**:
   - `X-Client` is the **Endorser**. It outputs the signed proposal.
   - To commit it to the blockchain, the signed envelope is forwarded to the **Arma Orderer** (`localhost:7050`).
   - The Orderer sequences the transaction into a block and sends it to the **Committer**.
   - The Committer executes the SQL write to PostgreSQL and emits status `COMMITTED` via the **Sidecar** (`localhost:4001`).
   - Calling `GET /api/v1/wallets/WA` queries PostgreSQL; once committed, the wallet is returned.

---

## 5. Verified End-to-End Test Commands

Here is the sequential flow to test all features:

```bash
# 1. Health check
curl http://localhost:8080/healthz

# 2. Central Bank opens a wallet for Bank A (Org1MSP)
curl -X POST http://localhost:8080/api/v1/wallets \
  -H "Content-Type: application/json" \
  -H "X-Caller-MSP: CentralBankMSP" \
  -d '{"walletId":"WA","ownerMsp":"Org1MSP","participantType":"bank"}'

# 3. Central Bank opens a wallet for Bank B (Org2MSP)
curl -X POST http://localhost:8080/api/v1/wallets \
  -H "Content-Type: application/json" \
  -H "X-Caller-MSP: CentralBankMSP" \
  -d '{"walletId":"WB","ownerMsp":"Org2MSP","participantType":"bank"}'

# 4. Central Bank mints 1,000,000 units into Bank A's wallet
curl -X POST http://localhost:8080/api/v1/movements/mint \
  -H "Content-Type: application/json" \
  -H "X-Caller-MSP: CentralBankMSP" \
  -d '{"walletId":"WA","movementId":"M1001","amount":1000000,"salt":"secret-salt-16-chars-min"}'

# 5. Bank A transfers 300,000 units to Bank B
curl -X POST http://localhost:8080/api/v1/movements/transfer \
  -H "Content-Type: application/json" \
  -H "X-Caller-MSP: Org1MSP" \
  -d '{"fromWalletId":"WA","toWalletId":"WB","movementId":"T2001","amount":300000,"salt":"secret-salt-16-chars-min"}'

# 6. Verify the transfer amount commitment
curl -X POST http://localhost:8080/api/v1/movements/T2001/verify \
  -H "Content-Type: application/json" \
  -d '{"amount":300000,"salt":"secret-salt-16-chars-min"}'
```

---

## 6. Tearing Down the Network

When you are done testing:

### Stop the Network (Preserves Database & Ledger)
```bash
cd /path/to/fabric-x-samples/devnet
make stop
```

### Complete Purge (Wipes All Containers and Database Volumes)
```bash
cd /path/to/fabric-x-samples/devnet
make purge
```
