// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package stellarregistry

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/google/uuid"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testContractAddress = "CBBEGRCFIZDUQSKKJNGE2TSPKBIVEU2UKVLFOWCZLJNVYXK6L5QGDRBB"
const testOwnerAddress = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

type testCallbacks struct {
	upsertRegistryRecords func(ctx context.Context, req *prototk.UpsertRegistryRecordsRequest) (*prototk.UpsertRegistryRecordsResponse, error)
}

func (tc *testCallbacks) UpsertRegistryRecords(ctx context.Context, req *prototk.UpsertRegistryRecordsRequest) (*prototk.UpsertRegistryRecordsResponse, error) {
	return tc.upsertRegistryRecords(ctx, req)
}

func TestPluginLifecycle(t *testing.T) {
	pb := NewPlugin(context.Background())
	assert.NotNil(t, pb)
}

func TestBadConfigJSON(t *testing.T) {
	callbacks := &testCallbacks{}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	_, err := r.ConfigureRegistry(r.bgCtx, &prototk.ConfigureRegistryRequest{
		Name:       "stellar",
		ConfigJson: `{!!!!`,
	})
	assert.Regexp(t, "PD080001", err)
}

func TestMissingContractAddress(t *testing.T) {
	callbacks := &testCallbacks{}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	_, err := r.ConfigureRegistry(r.bgCtx, &prototk.ConfigureRegistryRequest{
		Name:       "stellar",
		ConfigJson: `{}`,
	})
	require.Regexp(t, "PD080003", err)
}

func TestInvalidContractAddress(t *testing.T) {
	callbacks := &testCallbacks{}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	_, err := r.ConfigureRegistry(r.bgCtx, &prototk.ConfigureRegistryRequest{
		Name:       "stellar",
		ConfigJson: `{"contractAddress": "not-a-real-address"}`,
	})
	require.Regexp(t, "PD080001", err)
}

func TestGoodConfigJSON(t *testing.T) {
	var upsertReq *prototk.UpsertRegistryRecordsRequest
	callbacks := &testCallbacks{
		upsertRegistryRecords: func(ctx context.Context, req *prototk.UpsertRegistryRecordsRequest) (*prototk.UpsertRegistryRecordsResponse, error) {
			upsertReq = req
			return &prototk.UpsertRegistryRecordsResponse{}, nil
		},
	}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	res, err := r.ConfigureRegistry(r.bgCtx, &prototk.ConfigureRegistryRequest{
		Name:       "stellar",
		ConfigJson: fmt.Sprintf(`{"contractAddress": "%s"}`, testContractAddress),
	})
	require.NoError(t, err)

	require.Len(t, res.RegistryConfig.EventSources, 1)
	src := res.RegistryConfig.EventSources[0]
	assert.Equal(t, testContractAddress, src.ContractAddress)
	assert.Equal(t, []string{identityRegisteredTopic0, propertySetTopic0}, src.EventSymbols)
	assert.Equal(t, "identity-registry", src.ContractSpecName)
	assert.Empty(t, src.AbiEventsJson)

	// ConfigureRegistry must have self-registered a $specName property immediately, so the
	// resolver cache is populated before any real on-chain event is ever processed.
	chainAddr, err := pldtypes.ParseChainAddress(testContractAddress)
	require.NoError(t, err)
	wantEntryID := hex.EncodeToString([]byte(chainAddr.String()))

	require.NotNil(t, upsertReq)
	require.Len(t, upsertReq.Entries, 1)
	assert.Equal(t, wantEntryID, upsertReq.Entries[0].Id)
	assert.Equal(t, "identity-registry", upsertReq.Entries[0].Name)
	assert.True(t, upsertReq.Entries[0].Active)

	require.Len(t, upsertReq.Properties, 1)
	assert.Equal(t, wantEntryID, upsertReq.Properties[0].EntryId)
	assert.Equal(t, "$specName", upsertReq.Properties[0].Name)
	assert.Equal(t, "identity-registry", upsertReq.Properties[0].Value)
	assert.True(t, upsertReq.Properties[0].PluginReserved)
}

func TestSpecNameSelfRegisterFail(t *testing.T) {
	callbacks := &testCallbacks{
		upsertRegistryRecords: func(ctx context.Context, req *prototk.UpsertRegistryRecordsRequest) (*prototk.UpsertRegistryRecordsResponse, error) {
			return nil, assert.AnError
		},
	}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	_, err := r.ConfigureRegistry(r.bgCtx, &prototk.ConfigureRegistryRequest{
		Name:       "stellar",
		ConfigJson: fmt.Sprintf(`{"contractAddress": "%s"}`, testContractAddress),
	})
	require.Regexp(t, "PD080004", err)
}

func TestHandleEventBatchOk(t *testing.T) {
	var identity, parent [32]byte
	copy(identity[:], []byte("identity-hash-32-bytes-long!!!!"))
	copy(parent[:], []byte("parent-hash-32-bytes-long!!!!!!"))

	topics, data := buildIdentityRegisteredPayload(t, identity, parent, "node1", testOwnerAddress)
	propTopics, propData := buildPropertySetPayload(t, identity, "transport.grpc", []byte(`{"endpoint":"details"}`))

	callbacks := &testCallbacks{
		upsertRegistryRecords: func(ctx context.Context, req *prototk.UpsertRegistryRecordsRequest) (*prototk.UpsertRegistryRecordsResponse, error) {
			require.Len(t, req.Entries, 1)
			require.Equal(t, &prototk.RegistryEntry{
				Id:       hex.EncodeToString(identity[:]),
				ParentId: hex.EncodeToString(parent[:]),
				Name:     "node1",
				Active:   true,
				Location: &prototk.OnChainEventLocation{TransactionHash: "tx1", BlockNumber: 100, TransactionIndex: 10, LogIndex: 5},
			}, req.Entries[0])

			require.Len(t, req.Properties, 2)
			require.Equal(t, &prototk.RegistryProperty{
				EntryId:        hex.EncodeToString(identity[:]),
				Name:           "$owner",
				Value:          testOwnerAddress,
				PluginReserved: true,
				Active:         true,
				Location:       &prototk.OnChainEventLocation{TransactionHash: "tx1", BlockNumber: 100, TransactionIndex: 10, LogIndex: 5},
			}, req.Properties[0])
			require.Equal(t, &prototk.RegistryProperty{
				EntryId:  hex.EncodeToString(identity[:]),
				Name:     "transport.grpc",
				Value:    `{"endpoint":"details"}`,
				Active:   true,
				Location: &prototk.OnChainEventLocation{TransactionHash: "tx2", BlockNumber: 200, TransactionIndex: 20, LogIndex: 10},
			}, req.Properties[1])

			return &prototk.UpsertRegistryRecordsResponse{}, nil
		},
	}

	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	res, err := r.HandleRegistryEvents(r.bgCtx, &prototk.HandleRegistryEventsRequest{
		BatchId: uuid.New().String(),
		Events: []*prototk.OnChainEvent{
			{
				Location:  &prototk.OnChainEventLocation{TransactionHash: "tx1", BlockNumber: 100, TransactionIndex: 10, LogIndex: 5},
				Signature: computeEventSelectorWithSpec("identity-registry", identityRegisteredTopic0).String(),
				DataJson:  envelopeJSON(topics, data),
			},
			{
				Location:  &prototk.OnChainEventLocation{TransactionHash: "tx2", BlockNumber: 200, TransactionIndex: 20, LogIndex: 10},
				Signature: computeEventSelectorWithSpec("identity-registry", propertySetTopic0).String(),
				DataJson:  envelopeJSON(propTopics, propData),
			},
		},
	})
	require.NoError(t, err)

	// Simulate what registrymgr would actually do with this response.
	_, err = callbacks.upsertRegistryRecords(context.Background(), &prototk.UpsertRegistryRecordsRequest{
		Entries:    res.Entries,
		Properties: res.Properties,
	})
	require.NoError(t, err)
}

func TestHandleEventUnknownSig(t *testing.T) {
	callbacks := &testCallbacks{}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	res, err := r.HandleRegistryEvents(r.bgCtx, &prototk.HandleRegistryEventsRequest{
		BatchId: uuid.New().String(),
		Events: []*prototk.OnChainEvent{
			{
				Location:  &prototk.OnChainEventLocation{TransactionHash: "tx1", BlockNumber: 100, TransactionIndex: 10, LogIndex: 5},
				Signature: computeEventSelectorWithSpec("some-other-spec", "unrelated_event").String(),
				DataJson:  `{"topics":[],"data":"00"}`,
			},
		},
	})
	require.NoError(t, err)
	require.Empty(t, res.Entries)
	require.Empty(t, res.Properties)
}

func TestHandleEventBadSig(t *testing.T) {
	callbacks := &testCallbacks{}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	_, err := r.HandleRegistryEvents(r.bgCtx, &prototk.HandleRegistryEventsRequest{
		BatchId: uuid.New().String(),
		Events: []*prototk.OnChainEvent{
			{Signature: "not-hex"},
		},
	})
	require.Regexp(t, "PD080002", err)
}

func TestHandleEventBadPayloadJSON(t *testing.T) {
	callbacks := &testCallbacks{}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	_, err := r.HandleRegistryEvents(r.bgCtx, &prototk.HandleRegistryEventsRequest{
		BatchId: uuid.New().String(),
		Events: []*prototk.OnChainEvent{
			{
				Location:  &prototk.OnChainEventLocation{},
				Signature: computeEventSelectorWithSpec("identity-registry", identityRegisteredTopic0).String(),
				DataJson:  `{!!! not json`,
			},
		},
	})
	require.Regexp(t, "PD080002", err)
}

func TestHandleEventBadEntryName(t *testing.T) {
	var identity, parent [32]byte
	copy(identity[:], []byte("identity-hash-32-bytes-long!!!!"))
	topics, data := buildIdentityRegisteredPayload(t, identity, parent, "___ wrong", testOwnerAddress)

	callbacks := &testCallbacks{}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	res, err := r.HandleRegistryEvents(r.bgCtx, &prototk.HandleRegistryEventsRequest{
		BatchId: uuid.New().String(),
		Events: []*prototk.OnChainEvent{
			{
				Location:  &prototk.OnChainEventLocation{TransactionHash: "tx1"},
				Signature: computeEventSelectorWithSpec("identity-registry", identityRegisteredTopic0).String(),
				DataJson:  envelopeJSON(topics, data),
			},
		},
	})
	require.NoError(t, err)
	require.Empty(t, res.Entries)
	require.Empty(t, res.Properties)
}

func TestHandleEventBatchPropBadName(t *testing.T) {
	var identity [32]byte
	copy(identity[:], []byte("identity-hash-32-bytes-long!!!!"))
	topics, data := buildPropertySetPayload(t, identity, "___ wrong", []byte("val"))

	callbacks := &testCallbacks{}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	res, err := r.HandleRegistryEvents(r.bgCtx, &prototk.HandleRegistryEventsRequest{
		BatchId: uuid.New().String(),
		Events: []*prototk.OnChainEvent{
			{
				Location:  &prototk.OnChainEventLocation{TransactionHash: "tx1"},
				Signature: computeEventSelectorWithSpec("identity-registry", propertySetTopic0).String(),
				DataJson:  envelopeJSON(topics, data),
			},
		},
	})
	require.NoError(t, err)
	require.Empty(t, res.Entries)
	require.Empty(t, res.Properties)
}

func TestHandleRootIdentity(t *testing.T) {
	// The root identity's own IdentityRegistered event has parent == identity == the all-zero
	// sentinel - ParentId must come back empty, matching EVM's own root-identity convention.
	var zero [32]byte
	topics, data := buildIdentityRegisteredPayload(t, zero, zero, "root", testOwnerAddress)

	callbacks := &testCallbacks{}
	r := NewStellarRegistry(callbacks).(*stellarRegistry)
	res, err := r.HandleRegistryEvents(r.bgCtx, &prototk.HandleRegistryEventsRequest{
		BatchId: uuid.New().String(),
		Events: []*prototk.OnChainEvent{
			{
				Location:  &prototk.OnChainEventLocation{TransactionHash: "tx1", BlockNumber: 1, TransactionIndex: 0, LogIndex: 0},
				Signature: computeEventSelectorWithSpec("identity-registry", identityRegisteredTopic0).String(),
				DataJson:  envelopeJSON(topics, data),
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)
	assert.Equal(t, "", res.Entries[0].ParentId)
	assert.Equal(t, hex.EncodeToString(zero[:]), res.Entries[0].Id)
	assert.Equal(t, "root", res.Entries[0].Name)
}

// --- test fixture builders: construct real XDR-encoded event payloads via scspec.FromJSON, the
// same codec the production decoder uses, so these tests exercise real wire bytes not a
// hand-rolled approximation.

func buildIdentityRegisteredPayload(t *testing.T, identity, parent [32]byte, name string, ownerStrkey string) (topics []string, data string) {
	t.Helper()
	spec, err := scspec.ParseSpecXDR(nil)
	require.NoError(t, err)

	topic0 := mustScValHex(t, spec, symbolType, fmt.Sprintf("%q", identityRegisteredTopic0))
	topic1 := mustScValHex(t, spec, bytesN32Type, fmt.Sprintf(`"%s"`, hex.EncodeToString(identity[:])))

	dataJSON := fmt.Sprintf(`["%s","%s","%s"]`, hex.EncodeToString(parent[:]), hex.EncodeToString([]byte(name)), ownerStrkey)
	dataHex := mustScValHex(t, spec, identityRegisteredDataType, dataJSON)

	return []string{topic0, topic1}, dataHex
}

func buildPropertySetPayload(t *testing.T, identity [32]byte, propName string, value []byte) (topics []string, data string) {
	t.Helper()
	spec, err := scspec.ParseSpecXDR(nil)
	require.NoError(t, err)

	topic0 := mustScValHex(t, spec, symbolType, fmt.Sprintf("%q", propertySetTopic0))
	topic1 := mustScValHex(t, spec, bytesN32Type, fmt.Sprintf(`"%s"`, hex.EncodeToString(identity[:])))

	dataJSON := fmt.Sprintf(`["%s","%s"]`, propName, hex.EncodeToString(value))
	dataHex := mustScValHex(t, spec, propertySetDataType, dataJSON)

	return []string{topic0, topic1}, dataHex
}

// mustScValHex builds a real ScVal for typeDef from a JSON value (via the same scspec codec the
// production decoder uses) and hex-encodes its XDR - i.e. exactly the bytes a real Soroban event
// would carry, not a hand-rolled approximation.
func mustScValHex(t *testing.T, spec *scspec.Spec, typeDef xdr.ScSpecTypeDef, jsonValue string) string {
	t.Helper()
	val, err := spec.FromJSON(json.RawMessage(jsonValue), typeDef)
	require.NoError(t, err)
	raw, err := val.MarshalBinary()
	require.NoError(t, err)
	return hex.EncodeToString(raw)
}

func envelopeJSON(topics []string, data string) string {
	b, _ := json.Marshal(struct {
		Topics []string `json:"topics"`
		Data   string   `json:"data"`
	}{Topics: topics, Data: data})
	return string(b)
}
