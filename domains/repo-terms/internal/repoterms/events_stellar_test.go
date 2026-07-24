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

package repoterms

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonTopicsDataStellar builds the {"topics":[...],"data":"0x.."} JSON shape stellarEventPayload
// mirrors - copied from domains/noto/internal/noto/events_stellar_test.go's own identical helper.
func jsonTopicsDataStellar(t *testing.T, topics []xdr.ScVal, data xdr.ScVal) string {
	t.Helper()
	topicStrs := make([]string, len(topics))
	for i, topic := range topics {
		b, err := topic.MarshalBinary()
		require.NoError(t, err)
		topicStrs[i] = pldtypes.HexBytes(b).String()
	}
	dataBytes, err := data.MarshalBinary()
	require.NoError(t, err)
	payload := stellarEventPayload{Topics: topicStrs, Data: pldtypes.HexBytes(dataBytes).String()}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(b)
}

func scSymbol(sym string) xdr.ScVal {
	s := xdr.ScSymbol(sym)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &s}
}

func scBytes32Val(b pldtypes.Bytes32) xdr.ScVal {
	bs := xdr.ScBytes(b[:])
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &bs}
}

func scVecVal(items ...xdr.ScVal) xdr.ScVal {
	vec := xdr.ScVec(items)
	vecPtr := &vec
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}
}

func randBytes32(t *testing.T) pldtypes.Bytes32 {
	t.Helper()
	var b pldtypes.Bytes32
	copy(b[:], pldtypes.RandBytes(32))
	return b
}

func TestStellarSetTermsSelectorMatchesExpectedName(t *testing.T) {
	assert.Equal(t, stellarEventSelector("set_terms").String(), stellarSetTermsSelector)
}

func TestDecodeStellarSetTermsEvent(t *testing.T) {
	ctx := context.Background()
	txID := randBytes32(t)
	termsStateID := randBytes32(t)

	t.Run("success", func(t *testing.T) {
		ev := &prototk.OnChainEvent{
			Signature: stellarSetTermsSelector,
			DataJson: jsonTopicsDataStellar(t,
				[]xdr.ScVal{scSymbol("set_terms"), scBytes32Val(txID)},
				scVecVal(scBytes32Val(termsStateID)),
			),
		}
		got, err := decodeStellarSetTermsEvent(ctx, ev)
		require.NoError(t, err)
		assert.Equal(t, txID, got.TxId)
		assert.Equal(t, termsStateID, got.TermsStateId)
	})

	t.Run("missing topics", func(t *testing.T) {
		ev := &prototk.OnChainEvent{
			DataJson: jsonTopicsDataStellar(t, []xdr.ScVal{scSymbol("set_terms")}, scVecVal(scBytes32Val(termsStateID))),
		}
		_, err := decodeStellarSetTermsEvent(ctx, ev)
		assert.ErrorContains(t, err, "topics")
	})

	t.Run("wrong data length", func(t *testing.T) {
		ev := &prototk.OnChainEvent{
			DataJson: jsonTopicsDataStellar(t,
				[]xdr.ScVal{scSymbol("set_terms"), scBytes32Val(txID)},
				scVecVal(scBytes32Val(termsStateID), scBytes32Val(txID)),
			),
		}
		_, err := decodeStellarSetTermsEvent(ctx, ev)
		assert.ErrorContains(t, err, "expected 1 elements")
	})

	t.Run("malformed json", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: "not json"}
		_, err := decodeStellarSetTermsEvent(ctx, ev)
		assert.Error(t, err)
	})
}

// TestHandleEventBatch proves HandleEventBatch confirms the right state ID from a synthetic
// set_terms event, and completes the transaction.
func TestHandleEventBatch(t *testing.T) {
	r := &RepoTerms{}
	ctx := t.Context()

	txID := randBytes32(t)
	termsStateID := randBytes32(t)

	ev := &prototk.OnChainEvent{
		Signature: stellarSetTermsSelector,
		Location:  &prototk.OnChainEventLocation{TransactionHash: "0xabc"},
		DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("set_terms"), scBytes32Val(txID)},
			scVecVal(scBytes32Val(termsStateID)),
		),
	}

	res, err := r.HandleEventBatch(ctx, &prototk.HandleEventBatchRequest{
		BatchId: "batch1",
		Events:  []*prototk.OnChainEvent{ev},
	})
	require.NoError(t, err)

	require.Len(t, res.TransactionsComplete, 1)
	assert.Equal(t, txID.String(), res.TransactionsComplete[0].TransactionId)

	require.Len(t, res.ConfirmedStates, 1)
	assert.Equal(t, termsStateID.String(), res.ConfirmedStates[0].Id)
	assert.Equal(t, txID.String(), res.ConfirmedStates[0].TransactionId)
}

func TestHandleEventBatch_UnknownEventIgnored(t *testing.T) {
	r := &RepoTerms{}
	ctx := t.Context()

	ev := &prototk.OnChainEvent{Signature: "0xdeadbeef"}
	res, err := r.HandleEventBatch(ctx, &prototk.HandleEventBatchRequest{
		BatchId: "batch1",
		Events:  []*prototk.OnChainEvent{ev},
	})
	require.NoError(t, err)
	assert.Empty(t, res.TransactionsComplete)
	assert.Empty(t, res.ConfirmedStates)
}

func TestHandleEventBatch_MalformedEventIgnored(t *testing.T) {
	r := &RepoTerms{}
	ctx := t.Context()

	ev := &prototk.OnChainEvent{Signature: stellarSetTermsSelector, DataJson: "not json"}
	res, err := r.HandleEventBatch(ctx, &prototk.HandleEventBatchRequest{
		BatchId: "batch1",
		Events:  []*prototk.OnChainEvent{ev},
	})
	require.NoError(t, err)
	assert.Empty(t, res.TransactionsComplete)
	assert.Empty(t, res.ConfirmedStates)
}
