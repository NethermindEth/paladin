// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package saladintypes

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type vector struct {
	Name               string `json:"name"`
	NetworkPassphrase  string `json:"network_passphrase"`
	ContractID         string `json:"contract_id"`
	TypeName           string `json:"type_name"`
	PayloadSCValXDRB64 string `json:"payload_scval_xdr_base64"`
	DigestHex          string `json:"digest_hex"`
}

func TestDigestMatchesSharedVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		payload, err := base64.StdEncoding.DecodeString(v.PayloadSCValXDRB64)
		require.NoError(t, err, v.Name)
		digest, err := DigestXDR(v.NetworkPassphrase, v.ContractID, v.TypeName, payload)
		require.NoError(t, err, v.Name)
		require.Equal(t, v.DigestHex, hex.EncodeToString(digest[:]), v.Name)
	}
}

func TestDigestRejectsInvalidContractID(t *testing.T) {
	_, err := DigestXDR("test", "not-a-contract", "snoto.Transfer", []byte{0x01})
	require.Regexp(t, "invalid stellar contract ID", err)
}

func TestDigestRejectsEmptyTypeName(t *testing.T) {
	_, err := DigestXDR("test", "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM", "", []byte{0x01})
	require.EqualError(t, err, "type name is required")
}

func TestDigestRejectsEmptyPayload(t *testing.T) {
	_, err := DigestXDR("test", "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM", "snoto.Transfer", nil)
	require.EqualError(t, err, "payload XDR is required")
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "testdata", "saladin", "saladin_typed_data_v0_vectors.json")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var vectors []vector
	require.NoError(t, json.Unmarshal(b, &vectors))
	return vectors
}
