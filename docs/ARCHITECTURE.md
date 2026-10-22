# Architecture

How X-Client turns a REST call into a committed Fabric-X transaction on the 3-org network (Central Bank, Bank A = Org1, Bank B = Org2).

## Glossary

| Term | Meaning here |
|---|---|
| **Namespace** | A Fabric-X keyspace with its own endorsement policy (like a table with its own write rules). The CBDC data is split across 5 namespaces. |
| **Endorsement policy** | Which organizations must sign a transaction for it to change a namespace, e.g. `AND('Org1MSP.member','CentralBankMSP.member')`. The committer enforces it. |
| **Read/write set** | What a transaction read (key and version) and what it will write, per namespace. In Fabric-X this is an `applicationpb.TxNamespace`. |
| **Endorsement** | One organization's signature over one namespace of one transaction. |
| **Issuer / notary** | The Central Bank (`CentralBankMSP`): creates and freezes wallets, mints, and co-signs every transaction. |
| **Co-signing** | Getting another organization's endorsement because the policy requires it. Every bank transaction needs the Central Bank's co-signature. |
| **Peer re-execution** | How a co-signer decides whether to sign (worked example below). |
| **MVCC** | Optimistic concurrency. A transaction commits only if every key it read is still at the version it read. Otherwise it is aborted (`ABORTED_MVCC_CONFLICT`) and the client retries. |
| **Envelope** | The signed wrapper (`common.Envelope`) the orderer accepts. It carries the transaction and its endorsements. |

## Roles and namespaces

| Namespace | Holds | Policy |
|---|---|---|
| `cbdc_public` | `WALLET_<id>`, `MOVE_<id>` (amount-free movement records) | `AND('CentralBankMSP.member')` |
| `cbdc_cb_pdc` | `SUPPLY~<move>` supply deltas, Central Bank balances | `AND('CentralBankMSP.member')` |
| `cbdc_credits` | `CREDIT~<wallet>~<move>` pending incoming payments | `AND('CentralBankMSP.member')` |
| `cbdc_org1_pdc` | Bank A balances `BAL_<id>`, audit copies `MOVEDETAIL_<id>` | `AND('Org1MSP.member','CentralBankMSP.member')` |
| `cbdc_org2_pdc` | Bank B balances and audit copies | `AND('Org2MSP.member','CentralBankMSP.member')` |

- No bank can change another bank's balance, and the Central Bank cannot change a bank's balance alone. The committer enforces this, not X-Client: forged writes to `cbdc_org1_pdc` signed by Bank B, by Bank B + Central Bank, or by the Central Bank alone are all aborted with `ABORTED_SIGNATURE_INVALID`.
- Payments go to `cbdc_credits`, not to the payee's balance namespace, so **the payee bank does not have to sign or be online to be paid**. It folds credits in with `consolidate`.

> Fabric-X has no private data. Every committer stores every namespace, so `_pdc` restricts who may **write**, not who may read.

## Transaction lifecycle

```
 client ──REST──▶ Bank A X-Client                                   Central Bank X-Client
                  1. execute contract on Bank A's ledger view
                  2. build TxNamespaces (sorted, deterministic)
                  3. sign namespaces Org1 is required for
                  4. POST /internal/v1/endorse ───────────────────▶ 5. verify request signature
                                                                    6. re-execute on CB's view
                                                                    7. compare bytes; sign
                  8. verify returned signatures ◀────────────────── endorsements
                  9. assemble applicationpb.Tx, wrap in signed envelope
                 10. broadcast to all 4 Arma routers
                 11. poll Query Service GetTransactionStatus
 client ◀──────── 12. COMMITTED / abort reason
```

1. **Execute**: `engine.Invoke` creates a transaction ID like Fabric does (`SHA-256(nonce ‖ creator)`) and a timestamp. It runs the operation through `contract.Execute` on a simulator that reads committed state and records every read (with its version) and write.
2. **Build**: the simulator emits one `TxNamespace` per namespace: reads-only, read-writes (with the version read) and blind writes, sorted by key. Namespaces the operation only *reads* are dropped, because the committer rejects any namespace without writes (`MALFORMED_NO_WRITES`). The contract only reads facts outside its written namespaces that do not change (wallet ownership).
3. **Sign locally**: for each namespace, the policy names the required MSPs, and the node signs those it holds keys for. The signature is ECDSA (low-S, DER) over `TxNamespace.ASN1Marshal(txID, metadata)`, the exact bytes the committer verifies.
4. **Ask peers**: for every other required MSP, the node sends an `EndorseRequest` (operation, JSON request, txID, timestamp, proposed namespaces) to that organization's X-Client (`peers:`).
5. **Re-execute and sign** (peer side, `engine.EndorseForPeer`): see below.
6. **Envelope**: endorsements are placed per namespace (`Tx.Endorsements[i]` belongs to `Tx.Namespaces[i]`). The transaction goes into a `MESSAGE` envelope whose channel header carries the txID and whose signature header carries the initiator. The initiator signs it.
7. **Submit**: the envelope goes to every router. A majority must accept it. The node polls `GetTransactionStatus` until a final status arrives or `commit_timeout` passes (`202 SUBMITTED`).

## Peer re-execution, worked example

Bank A pays Bank B 250k (`WA → WB`, movement `T1`).

1. Bank A's X-Client runs the transfer and computes:
   - `cbdc_org1_pdc`: WA's pending 1,000,000 mint credit is folded in first, so `BAL_WA` becomes 750,000 (read as "must not exist yet"), plus `MOVEDETAIL_T1` and `MOVEDETAIL_M1`;
   - `cbdc_credits`: `CREDIT~WA~M1` deleted, new `CREDIT~WB~T1` of 250k;
   - `cbdc_public`: new `MOVE_T1`.
2. `cbdc_org1_pdc` needs Org1 **and** the Central Bank; the others need the Central Bank. Bank A signs `cbdc_org1_pdc` and sends the Central Bank's X-Client the request ("transfer 250k WA→WB, T1") plus the namespaces it computed.
3. The Central Bank first checks who is asking. The request is signed with Org1's MSP key, and the certificate must chain to Org1's MSP CA (`peers.Org1MSP.msp_ca`). Otherwise it returns `401`.
4. The Central Bank does **not** trust Bank A's computed changes. It **runs the same transfer itself**, as Org1, with the same txID and timestamp, against its own committer's state. That repeats every rule: Org1 owns WA, WA has enough balance, WB is not frozen, `T1` is unused.
5. It compares its result with Bank A's byte for byte.
   - **Identical**: the Central Bank signs all three namespaces and returns the signatures.
   - **Different**: it refuses with `409 PROPOSAL_MISMATCH`. Bank A may have tampered with the values, or one side is a block behind. Bank A retries once after 500 ms.
   - **The rules fail**: it returns the rule's error, e.g. `403 ACCESS_DENIED` when Bank B asks it to co-sign a debit of WA.
6. Bank A checks the signatures are valid and from the Central Bank, then submits.

Without re-execution a co-signer would sign whatever it is handed. With it, the Central Bank only signs changes its own copy of the contract would also make. Classic Fabric peers endorse the same way.

**Determinism.** Re-execution needs identical bytes on both sides, so the contract never reads the clock or randomness. The initiator fixes txID and timestamp (`contract.TxContext`), the simulator sorts namespaces and keys, and JSON field order is fixed by Go structs. Peers reject requests whose timestamp is more than 5 minutes off (`engine.MaxClockSkew`).

## Reading state

- **Point reads** go to the organization's own Query Service (`GetRows`): Bank A reads 7001, Bank B 7002, the Central Bank 7003.
- **Prefix scans** (pending credits, supply deltas) are not offered by the Query Service. Each node follows its sidecar's block stream (fabric-x-sdk `Synchronizer`) and keeps an in-memory index of existing keys (`store.KeyIndex`). Scans resolve key names there and fetch values and versions from the Query Service. The index is rebuilt from block 0 at start. `/healthz` reports `ready: false` until it has caught up.
- **Namespace versions** come from `GetNamespacePolicies` and are cached for 30 seconds.

## Standalone mode

With `mode: in-memory`, one process holds throwaway keys for all three organizations. Instead of the network it uses `store.InMemoryStore`, which commits with the committer's rules: MVCC checks, duplicate txIDs, no namespace without writes, endorsement count per namespace. It does not check signatures against policies.

## Package map

| Package | Responsibility |
|---|---|
| `cmd/server` | config loading (`${VAR:-default}` expansion), wiring, startup log |
| `pkg/contract` | CBDC rules: deterministic, typed errors, `Execute` dispatcher |
| `pkg/simulator` | records reads/writes, emits sorted `TxNamespace`s |
| `pkg/engine` | execute → endorse (local + peers) → envelope → submit; peer side `EndorseForPeer`; `Policies` |
| `pkg/peers` | `EndorseRequest` protocol, request signing and verification |
| `pkg/endorser` | MSP identities, namespace signatures |
| `pkg/fabricx` | txID/header, envelope, router broadcast, commit-status polling, local submitter |
| `pkg/store` | Query Service store, block-fed key index, in-memory ledger |
| `pkg/api` | REST handlers, error → HTTP mapping, API token |
