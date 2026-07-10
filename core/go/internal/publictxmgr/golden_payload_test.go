/*
 * Copyright © 2026 Kaleido, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package publictxmgr

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/stretchr/testify/require"
)

// TestGoldenPublicTxJSONPayload is the golden-payload regression guard chapter 11 §11.8
// (acceptance criterion 2) calls for: it fixes the exact JSON-RPC shape of a public transaction,
// in particular the EVM address (0x-hex) and hash serialization, so that the upcoming
// EthAddress -> ChainAddress internal-manager migration (chapter 11 §11.4, milestone M1 in
// chapter 16) can be verified to leave existing EVM API payloads byte-for-byte unchanged.
//
// If this test fails after a types/serialization change, the fixture in
// testdata/golden/public_tx.json must be reviewed by a human to confirm the change is intentional
// and still byte-identical for EVM callers - it must not be casually regenerated to "make it pass".
func TestGoldenPublicTxJSONPayload(t *testing.T) {
	hash := pldtypes.MustParseBytes32("0x0503bb2e013a6ecfe29c6c7e073d6f0cf834edf6d305606c4e4623c98cb7fa5a")
	nonce := uint64(42)

	ptx := &DBPublicTxn{
		PublicTxnID:     101,
		From:            pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress(),
		Nonce:           &nonce,
		Created:         pldtypes.TimestampFromUnix(1700000000),
		To:              ethAddressChainAddress(pldtypes.MustEthAddress("0x2d0cd5b99d2e2a380e52b4000377dd507c6df754")),
		Gas:             21000,
		FixedGasPricing: pldtypes.RawJSON(`{"maxFeePerGas":"0x3b9aca00","maxPriorityFeePerGas":"0x3b9aca00"}`),
		Value:           pldtypes.Uint64ToUint256(1000000000000000000),
		Data:            pldtypes.HexBytes{0x01, 0x02, 0x03},
		Dispatcher:      "node1",
		Completed: &DBPublicTxnCompletion{
			PublicTxnID:     101,
			Created:         pldtypes.TimestampFromUnix(1700000010),
			TransactionHash: hash,
			Success:         true,
		},
	}

	tx, err := mapPersistedTransaction(ptx)
	require.NoError(t, err)
	actual, err := json.Marshal(tx)
	require.NoError(t, err)

	expected, err := os.ReadFile("testdata/golden/public_tx.json")
	require.NoError(t, err)

	require.JSONEq(t, string(expected), string(actual))
}
