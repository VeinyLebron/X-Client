#!/usr/bin/env bash
# Helper script to copy crypto material from fabric-x-samples/devnet into X-Client

set -euo pipefail

DEVNET_DIR="${1:-$HOME/Projects/repos-bench/fabric-x-samples/devnet}"
CRYPTO_DIR="$DEVNET_DIR/crypto"

if [ ! -d "$CRYPTO_DIR" ]; then
    echo "Error: Devnet crypto directory not found at $CRYPTO_DIR"
    echo "Usage: ./scripts/copy-crypto.sh [path-to-fabric-x-samples/devnet]"
    exit 1
fi

DEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/crypto"

echo "Copying crypto materials from: $CRYPTO_DIR"
echo "Destination directory: $DEST_DIR"

# 1. Bank A (Org1MSP)
echo "Setting up Bank A (Org1MSP)..."
mkdir -p "$DEST_DIR/bank_a"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/User1@org1.example.com/msp/signcerts/*-cert.pem "$DEST_DIR/bank_a/cert.pem"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/User1@org1.example.com/msp/keystore/*_sk "$DEST_DIR/bank_a/priv_sk"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/User1@org1.example.com/tls/ca.crt "$DEST_DIR/bank_a/ca.crt"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/User1@org1.example.com/tls/client.crt "$DEST_DIR/bank_a/tls_client.crt"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/User1@org1.example.com/tls/client.key "$DEST_DIR/bank_a/tls_client.key"

# 2. Bank B (Org2MSP)
echo "Setting up Bank B (Org2MSP)..."
mkdir -p "$DEST_DIR/bank_b"
cp "$CRYPTO_DIR"/peerOrganizations/org2.example.com/users/User1@org2.example.com/msp/signcerts/*-cert.pem "$DEST_DIR/bank_b/cert.pem"
cp "$CRYPTO_DIR"/peerOrganizations/org2.example.com/users/User1@org2.example.com/msp/keystore/*_sk "$DEST_DIR/bank_b/priv_sk"
cp "$CRYPTO_DIR"/peerOrganizations/org2.example.com/users/User1@org2.example.com/tls/ca.crt "$DEST_DIR/bank_b/ca.crt"
cp "$CRYPTO_DIR"/peerOrganizations/org2.example.com/users/User1@org2.example.com/tls/client.crt "$DEST_DIR/bank_b/tls_client.crt"
cp "$CRYPTO_DIR"/peerOrganizations/org2.example.com/users/User1@org2.example.com/tls/client.key "$DEST_DIR/bank_b/tls_client.key"

# 3. Central Bank (CentralBankMSP / Admin)
echo "Setting up Central Bank (CentralBankMSP)..."
mkdir -p "$DEST_DIR/centralbank"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/Admin@org1.example.com/msp/signcerts/Admin@org1.example.com-cert.pem "$DEST_DIR/centralbank/cert.pem"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/Admin@org1.example.com/msp/keystore/*_sk "$DEST_DIR/centralbank/priv_sk"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/User1@org1.example.com/tls/ca.crt "$DEST_DIR/centralbank/ca.crt"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/User1@org1.example.com/tls/client.crt "$DEST_DIR/centralbank/tls_client.crt"
cp "$CRYPTO_DIR"/peerOrganizations/org1.example.com/users/User1@org1.example.com/tls/client.key "$DEST_DIR/centralbank/tls_client.key"

echo "All crypto materials copied successfully!"
