# Architecture

How X-Client turns a REST call into a committed Fabric-X transaction on the 2-org network (Org1, Org2).

## Glossary

| Term | Meaning here |
|---|---|
| **Namespace** | A Fabric-X keyspace with its own endorsement policy (like a table with its own write rules). The CBDC data is split across 5 namespaces. |
| **Endorsement policy** | Which organizations must sign a transaction for it to change a namespace, e.g. `AND('Org2MSP.member','Org1MSP.member')`. The committer enforces it. |
| **Read/write set** | What a transaction read (key and version) and what it will write, per namespace. In Fabric-X this is an `applicationpb.TxNamespace`. |
| **Endorsement** | One organization's signature over one namespace of one transaction. |
| **Issuer** | The organization that creates and freezes wallets and mints. On this branch it is Org1 (`central_bank_msp: Org1MSP`). |
| **Co-signing** | Getting another organization's endorsement because the policy requires it. Org2's balance changes need Org1's co-signature. |
| **Peer re-execution** | How a co-signer decides whether to sign (worked example below). |
| **MVCC** | Optimistic concurrency. A transaction commits only if every key it read is still at the version it read. Otherwise it is aborted (`ABORTED_MVCC_CONFLICT`) and the client retries. |
| **Envelope** | The signed wrapper (`common.Envelope`) the orderer accepts. It carries the transaction and its endorsements. |

## Roles and namespaces

| Namespace | Holds | Policy |
|---|---|---|
| `cbdc_public` | `WALLET_<id>`, `MOVE_<id>` (amount-free movement records) | `AND('Org1MSP.member')` |
| `cbdc_cb_pdc` | `SUPPLY~<move>` supply deltas | `AND('Org1MSP.member')` |
| `cbdc_credits` | `CREDIT~<wallet>~<move>` pending incoming payments | `AND('Org1MSP.member')` |
| `cbdc_org1_pdc` | Org1 balances `BAL_<id>`, audit copies `MOVEDETAIL_<id>` | `AND('Org1MSP.member')` |
| `cbdc_org2_pdc` | Org2 balances and audit copies | `AND('Org2MSP.member','Org1MSP.member')` |

- Org1 can do anything it is allowed to do by signing alone.
- Org2 can only change its own balance with Org1's co-signature, and Org1 can never change Org2's balance without Org2. The committer enforces this, not X-Client: a write to `cbdc_org2_pdc` signed by only one of them is aborted with `ABORTED_SIGNATURE_INVALID`.
- Payments to a wallet are written to `cbdc_credits`, not to the payee's balance namespace, so **the payee does not have to sign or be online to be paid**. The payee later runs `consolidate` to fold credits into its balance.

> Fabric-X has no private data. Both committers store every namespace, so `_pdc` restricts who may **write**, not who may read.

## Transaction lifecycle

```
 client ──REST──▶ Org2 X-Client                                     Org1 X-Client
                  1. execute contract on Org2's ledger view
                  2. build TxNamespaces (sorted, deterministic)
                  3. sign namespaces Org2 is required for
                  4. POST /internal/v1/endorse ───────────────────▶ 5. verify request signature
                                                                    6. re-execute on Org1's view
                                                                    7. compare bytes; sign
                  8. verify returned signatures ◀────────────────── endorsements
                  9. assemble applicationpb.Tx, wrap in signed envelope
                 10. broadcast to all 4 Arma routers
                 11. poll Query Service GetTransactionStatus
 client ◀──────── 12. COMMITTED / abort reason
```

1. **Execute**: `engine.Invoke` creates a transaction ID like Fabric does (`SHA-256(nonce ‖ creator)`) and a timestamp. It runs the operation through `contract.Execute` on a simulator that reads committed state and records every read (with its version) and write.
2. **Build**: the simulator emits one `TxNamespace` per namespace: reads-only, read-writes (with the version read) and blind writes, sorted by key. Namespaces the operation only *reads* are dropped, because the committer rejects any namespace without writes (`MALFORMED_NO_WRITES`). The contract only reads facts outside its written namespaces that do not change (wallet ownership).
3. **Sign locally**: for each namespace, the policy (`endorsement_policies`, or the defaults) names the required MSPs. The node signs those it holds keys for. The signature is ECDSA (low-S, DER) over `TxNamespace.ASN1Marshal(txID, metadata)`, the exact bytes the committer verifies.
4. **Ask peers**: for every other required MSP, the node sends an `EndorseRequest` to that organization's X-Client (`peers:` in the config). The request carries the operation, its JSON request, txID, timestamp and the proposed namespaces.
5. **Re-execute and sign** (peer side, `engine.EndorseForPeer`): see below.
6. **Envelope**: the endorsements are placed per namespace (`Tx.Endorsements[i]` belongs to `Tx.Namespaces[i]`). The transaction goes into a `MESSAGE` envelope whose channel header carries the txID and whose signature header carries the initiator's identity. The initiator signs it.
7. **Submit**: the envelope goes to every router. A majority must accept it. The node then polls `GetTransactionStatus` until the committer returns a final status or `commit_timeout` passes (`202 SUBMITTED`).

## Peer re-execution, worked example

Org2 pays Org1 100k (`WB → WA`, movement `T3`).

1. Org2's X-Client runs the transfer and computes:
   - `cbdc_org2_pdc`: `BAL_WB` 300k → 200k (recording the version it read), plus `MOVEDETAIL_T3`;
   - `cbdc_credits`: new `CREDIT~WA~T3` of 100k;
   - `cbdc_public`: new `MOVE_T3`.
2. `cbdc_org2_pdc` needs Org2 **and** Org1, and the other two need Org1. Org2 signs `cbdc_org2_pdc` itself and sends Org1's X-Client the request ("transfer 100k WB→WA, T3") plus the namespaces it computed.
3. Org1's X-Client first checks who is asking. The request is signed with Org2's MSP key, and the certificate must chain to Org2's MSP CA (`peers.Org2MSP.msp_ca`). Otherwise it returns `401`.
4. Org1 does **not** trust Org2's computed changes. It **runs the same transfer itself**, as Org2, with the same txID and timestamp, against its own committer's state. That repeats every rule: Org2 owns WB, WB has enough balance, WA is not frozen, `T3` is unused.
5. Org1 compares its result with Org2's byte for byte.
   - **Identical**: Org1 signs every namespace that requires Org1 and returns the signatures.
   - **Different**: Org1 refuses with `409 PROPOSAL_MISMATCH`. Org2 may have tampered with the values ("debit 1, credit 1,000,000"), or one side's view of the ledger is a block behind. Org2 retries once after 500 ms, which covers the lag case.
   - **The rules fail**: Org1 returns the rule's error, e.g. `403 ACCESS_DENIED` if Org2 tries to spend Org1's wallet.
6. Org2 checks that the returned signatures are valid and from Org1, then submits.

Without re-execution a co-signer would sign whatever it is handed. With it, Org1 only signs changes its own copy of the contract would also make. Classic Fabric peers endorse the same way.

**Determinism.** Re-execution only works if both sides compute identical bytes, so the contract never reads the clock or randomness itself. The initiator fixes txID and timestamp (`contract.TxContext`), the simulator sorts namespaces and keys, and JSON field order is fixed by Go structs. A peer rejects requests whose timestamp is more than 5 minutes from its own clock (`engine.MaxClockSkew`).

## Reading state

- **Point reads** go to the organization's own Query Service (`GetRows`). Each node reads from its own committer, never the other org's.
- **Prefix scans** (a wallet's pending credits, all supply deltas) are not offered by the Query Service. Each node subscribes to its sidecar's block stream (fabric-x-sdk `Synchronizer`) and keeps an in-memory index of which keys exist (`store.KeyIndex`). A prefix scan resolves key names from the index and fetches values and versions from the Query Service. The index is rebuilt from block 0 at start. `/healthz` reports `ready: false` until it has caught up.
- **Namespace versions** (part of every `TxNamespace`) come from `GetNamespacePolicies` and are cached for 30 seconds.

## Standalone mode

With `mode: in-memory`, one process holds throwaway keys for both organizations. Instead of the network it uses `store.InMemoryStore`, which commits transactions with the committer's rules: MVCC version checks, duplicate txIDs, no namespace without writes, endorsement count per namespace. It does not check signatures against policies. The full flow, including multi-org endorsement, runs without Docker.

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
