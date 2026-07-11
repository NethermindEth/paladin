// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package saladintypes

import (
	"crypto/sha256"
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
)

const schemeTag = "SALADIN_TYPED_DATA_V0"

func DigestXDR(networkPassphrase, contractID, typeName string, payloadXDR []byte) ([32]byte, error) {
	var zero [32]byte
	if networkPassphrase == "" {
		return zero, fmt.Errorf("network passphrase is required")
	}
	if contractID == "" {
		return zero, fmt.Errorf("contract ID is required")
	}
	if typeName == "" {
		return zero, fmt.Errorf("type name is required")
	}
	if len(payloadXDR) == 0 {
		return zero, fmt.Errorf("payload XDR is required")
	}
	contractBytes, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return zero, fmt.Errorf("invalid stellar contract ID %q: %w", contractID, err)
	}
	if len(contractBytes) != 32 {
		return zero, fmt.Errorf("invalid stellar contract ID length %d", len(contractBytes))
	}

	h := sha256.New()
	networkHash := sum256([]byte(networkPassphrase))
	typeHash := sum256([]byte(typeName))
	payloadHash := sum256(payloadXDR)
	h.Write([]byte(schemeTag))
	h.Write(networkHash[:])
	h.Write(contractBytes)
	h.Write(typeHash[:])
	h.Write(payloadHash[:])

	copy(zero[:], h.Sum(nil))
	return zero, nil
}

func sum256(data []byte) [32]byte {
	return sha256.Sum256(data)
}
