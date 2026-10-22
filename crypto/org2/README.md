# Org2MSP crypto material

Populated by `./scripts/copy-crypto.sh` from `fabric-x-samples/devnet/crypto/peerOrganizations/org2.example.com`:

| File | Source | Used for |
|---|---|---|
| `cert.pem`, `priv_sk` | `users/User1@org2.example.com/msp/signcerts`, `keystore` | endorsements and envelope signatures |
| `tls/ca.crt` | `users/User1@org2.example.com/tls/ca.crt` | verifying this org's committer (Query Service, sidecar) |
| `tls/client.crt`, `tls/client.key` | `users/User1@org2.example.com/tls/` | mTLS to the committer and the Arma routers |

The public MSP CA of each organization goes to `crypto/cas/` and the orderer TLS CAs to `crypto/orderer-tls/`. All of it is git-ignored.
