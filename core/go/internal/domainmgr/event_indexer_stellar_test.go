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

package domainmgr

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/core/pkg/blockindexer"
	"github.com/LFDT-Paladin/paladin/core/pkg/persistence"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/google/uuid"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildStellarRegistrationEventData XDR-encodes a SaladinFactory.register event's topics/data
// exactly as decodeContractEvent (core/go/pkg/baseledger/stellar/ingestor.go) would have produced
// them from the real soroban/contracts/factory Registration event, then JSON-shapes the result the
// way stellarEventPayload.dataJSON() delivers it - so this fixture exercises the exact wire shape
// decodeSaladinFactoryRegistration parses, not just its own inverse.
func buildStellarRegistrationEventData(t *testing.T, txID uuid.UUID, instanceContractID [32]byte, config []byte) pldtypes.RawJSON {
	t.Helper()

	sym := xdr.ScSymbol("reg")
	topic0 := xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}

	txIDBytes32 := pldtypes.Bytes32UUIDFirst16(txID)
	txIDScBytes := xdr.ScBytes(txIDBytes32[:])
	topic1 := xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &txIDScBytes}

	contractID := xdr.ContractId(instanceContractID)
	scAddr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID}
	instanceVal := xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &scAddr}

	configScBytes := xdr.ScBytes(config)
	configVal := xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &configScBytes}

	vec := xdr.ScVec{instanceVal, configVal}
	vecPtr := &vec
	dataVal := xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}

	return jsonTopicsData(t, []xdr.ScVal{topic0, topic1}, dataVal)
}

func jsonTopicsData(t *testing.T, topics []xdr.ScVal, data xdr.ScVal) pldtypes.RawJSON {
	t.Helper()
	topicStrs := make([]string, len(topics))
	for i, topic := range topics {
		b, err := topic.MarshalBinary()
		require.NoError(t, err)
		topicStrs[i] = pldtypes.HexBytes(b).String()
	}
	dataBytes, err := data.MarshalBinary()
	require.NoError(t, err)
	return pldtypes.JSONString(struct {
		Topics []string `json:"topics"`
		Data   string   `json:"data"`
	}{
		Topics: topicStrs,
		Data:   pldtypes.HexBytes(dataBytes).String(),
	})
}

func randStellarContractIDAndAddress(t *testing.T) (contractID [32]byte, chainAddr pldtypes.ChainAddress) {
	t.Helper()
	copy(contractID[:], pldtypes.RandBytes(32))
	s, err := strkey.Encode(strkey.VersionByteContract, contractID[:])
	require.NoError(t, err)
	addr, err := pldtypes.ParseChainAddress(s)
	require.NoError(t, err)
	return contractID, *addr
}

// registerFakeStellarDomain registers a minimal domain object directly into dm's maps, bypassing
// the full ConfigureDomain/InitDomain plugin handshake - registrationIndexer only needs d.name and
// the domainsByAddress lookup, not a live domain plugin.
func registerFakeStellarDomain(t *testing.T, dm *domainManager, name string) (d *domain, registryAddr pldtypes.ChainAddress) {
	t.Helper()
	_, registryAddr = randStellarContractIDAndAddress(t)
	d = dm.newDomain(name, &pldconf.DomainConfig{RegistryAddress: registryAddr.String()}, nil)
	// This bypasses the full ConfigureDomain/InitDomain plugin handshake (d.init()), so initDone
	// must be closed directly - d.close() (run by dm.Stop() in the test's cleanup) blocks on it
	// otherwise, since nothing else ever closes it.
	close(d.initDone)
	dm.domainsByName[name] = d
	dm.domainsByAddress[*d.RegistryAddress()] = d
	return d, registryAddr
}

func TestRegistrationIndexerStellarSuccess(t *testing.T) {
	_, dm, _, done := newTestDomainManager(t, false, &pldconf.DomainManagerInlineConfig{}, func(mc *mockComponents) {
		mc.db.ExpectBegin()
		mc.db.ExpectExec("INSERT.*private_smart_contracts").WillReturnResult(sqlmock.NewResult(1, 1))
		mc.db.ExpectCommit()
	})
	defer done()

	_, registryAddr := registerFakeStellarDomain(t, dm, "test1")

	txID := uuid.New()
	instanceContractID, instanceChainAddr := randStellarContractIDAndAddress(t)
	config := []byte{0xfe, 0xed, 0xbe, 0xef}
	eventData := buildStellarRegistrationEventData(t, txID, instanceContractID, config)

	var unprocessedEvents []*pldapi.EventWithData
	var txCompletions txCompletionsOrdered
	err := dm.persistence.Transaction(context.Background(), func(ctx context.Context, dbTX persistence.DBTX) (err error) {
		unprocessedEvents, txCompletions, err = dm.registrationIndexer(ctx, dbTX, &blockindexer.EventDeliveryBatch{
			BatchID: uuid.New(),
			Events: []*pldapi.EventWithData{
				{
					AddressChain: &registryAddr,
					IndexedEvent: &pldapi.IndexedEvent{
						BlockNumber:      100,
						TransactionIndex: 1,
						LogIndex:         0,
						TransactionHash:  pldtypes.NewBytes32FromSlice(pldtypes.RandBytes(32)),
						Signature:        stellarRegisterSelector,
					},
					Data: eventData,
				},
			},
		})
		return err
	})
	require.NoError(t, err)
	assert.Empty(t, unprocessedEvents)
	require.Len(t, txCompletions, 1)
	completion := txCompletions[0]
	assert.Equal(t, txID, completion.TransactionID)
	assert.Equal(t, "test1", completion.Domain)
	require.NotNil(t, completion.ContractAddress)
	assert.Equal(t, instanceChainAddr, *completion.ContractAddress)
	assert.Nil(t, completion.OnChain.Source) // OnChainLocation.Source is EVM-only, not this task's scope
	assert.Nil(t, completion.PSC)            // unset for deployments, matching the EVM path
}

func TestRegistrationIndexerStellarUnknownRegistry(t *testing.T) {
	_, dm, _, done := newTestDomainManager(t, false, &pldconf.DomainManagerInlineConfig{}, func(mc *mockComponents) {
		mc.db.ExpectBegin()
		mc.db.ExpectCommit()
	})
	defer done()

	_, unknownRegistryAddr := randStellarContractIDAndAddress(t)
	txID := uuid.New()
	instanceContractID, _ := randStellarContractIDAndAddress(t)
	eventData := buildStellarRegistrationEventData(t, txID, instanceContractID, []byte{0x01})

	var unprocessedEvents []*pldapi.EventWithData
	var txCompletions txCompletionsOrdered
	err := dm.persistence.Transaction(context.Background(), func(ctx context.Context, dbTX persistence.DBTX) (err error) {
		unprocessedEvents, txCompletions, err = dm.registrationIndexer(ctx, dbTX, &blockindexer.EventDeliveryBatch{
			BatchID: uuid.New(),
			Events: []*pldapi.EventWithData{
				{
					AddressChain: &unknownRegistryAddr,
					IndexedEvent: &pldapi.IndexedEvent{Signature: stellarRegisterSelector},
					Data:         eventData,
				},
			},
		})
		return err
	})
	require.NoError(t, err)
	assert.Empty(t, txCompletions)
	// Same behavior as the EVM path for an unrecognized registry: dropped entirely (not
	// forwarded as an unprocessed event either), since it never came from a registry we trust.
	assert.Empty(t, unprocessedEvents)
}

func TestRegistrationIndexerStellarSelectorMismatch(t *testing.T) {
	_, dm, _, done := newTestDomainManager(t, false, &pldconf.DomainManagerInlineConfig{}, func(mc *mockComponents) {
		mc.db.ExpectBegin()
		mc.db.ExpectCommit()
	})
	defer done()

	_, registryAddr := registerFakeStellarDomain(t, dm, "test1")

	ev := &pldapi.EventWithData{
		AddressChain: &registryAddr,
		IndexedEvent: &pldapi.IndexedEvent{Signature: pldtypes.Bytes32{0x01}}, // not the "reg" selector
		Data:         pldtypes.RawJSON(`{"topics":[],"data":"0x"}`),
	}

	var unprocessedEvents []*pldapi.EventWithData
	var txCompletions txCompletionsOrdered
	err := dm.persistence.Transaction(context.Background(), func(ctx context.Context, dbTX persistence.DBTX) (err error) {
		unprocessedEvents, txCompletions, err = dm.registrationIndexer(ctx, dbTX, &blockindexer.EventDeliveryBatch{
			BatchID: uuid.New(),
			Events:  []*pldapi.EventWithData{ev},
		})
		return err
	})
	require.NoError(t, err)
	assert.Empty(t, txCompletions)
	require.Len(t, unprocessedEvents, 1)
	assert.Same(t, ev, unprocessedEvents[0])
}

func TestRegistrationIndexerStellarBadPayload(t *testing.T) {
	_, dm, _, done := newTestDomainManager(t, false, &pldconf.DomainManagerInlineConfig{}, func(mc *mockComponents) {
		mc.db.ExpectBegin()
		mc.db.ExpectCommit()
	})
	defer done()

	_, registryAddr := registerFakeStellarDomain(t, dm, "test1")

	ev := &pldapi.EventWithData{
		AddressChain: &registryAddr,
		IndexedEvent: &pldapi.IndexedEvent{Signature: stellarRegisterSelector},
		Data:         pldtypes.RawJSON(`{"topics": "not an array", "data": "0x"}`),
	}

	var unprocessedEvents []*pldapi.EventWithData
	var txCompletions txCompletionsOrdered
	err := dm.persistence.Transaction(context.Background(), func(ctx context.Context, dbTX persistence.DBTX) (err error) {
		unprocessedEvents, txCompletions, err = dm.registrationIndexer(ctx, dbTX, &blockindexer.EventDeliveryBatch{
			BatchID: uuid.New(),
			Events:  []*pldapi.EventWithData{ev},
		})
		return err
	})
	require.NoError(t, err)
	assert.Empty(t, txCompletions)
	// Unlike the "unknown registry" case (which continues, dropping the event outright), a
	// decode failure from a *trusted* registry just logs and falls through - matching the EVM
	// path's own json.Unmarshal-failure handling (event_indexer.go) - so the event still comes
	// back as unprocessed rather than being silently swallowed.
	require.Len(t, unprocessedEvents, 1)
	assert.Same(t, ev, unprocessedEvents[0])
}

func TestDecodeSaladinFactoryRegistration(t *testing.T) {
	ctx := context.Background()
	txID := uuid.New()
	instanceContractID, instanceChainAddr := randStellarContractIDAndAddress(t)
	config := []byte{0xde, 0xad, 0xbe, 0xef}

	t.Run("success", func(t *testing.T) {
		ev := &pldapi.EventWithData{Data: buildStellarRegistrationEventData(t, txID, instanceContractID, config)}
		gotTxID, gotInstance, gotConfig, err := decodeSaladinFactoryRegistration(ctx, ev)
		require.NoError(t, err)
		assert.Equal(t, txID, gotTxID)
		assert.Equal(t, instanceChainAddr, gotInstance)
		assert.Equal(t, config, gotConfig)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		ev := &pldapi.EventWithData{Data: pldtypes.RawJSON(`not json`)}
		_, _, _, err := decodeSaladinFactoryRegistration(ctx, ev)
		assert.ErrorContains(t, err, "invalid event payload")
	})

	t.Run("too few topics", func(t *testing.T) {
		ev := &pldapi.EventWithData{Data: pldtypes.RawJSON(`{"topics":["0x00"],"data":"0x00"}`)}
		_, _, _, err := decodeSaladinFactoryRegistration(ctx, ev)
		assert.ErrorContains(t, err, "expected at least 2 topics")
	})

	t.Run("tx_id topic not bytes", func(t *testing.T) {
		sym := xdr.ScSymbol("reg")
		topic0 := xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
		notBytesTopic := xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: new(xdr.Uint32)}
		dataVal := xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &xdr.ScBytes{}}
		ev := &pldapi.EventWithData{Data: jsonTopicsData(t, []xdr.ScVal{topic0, notBytesTopic}, dataVal)}
		_, _, _, err := decodeSaladinFactoryRegistration(ctx, ev)
		assert.ErrorContains(t, err, "tx_id topic")
	})

	t.Run("data not a vec", func(t *testing.T) {
		sym := xdr.ScSymbol("reg")
		topic0 := xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
		txIDBytes32 := pldtypes.Bytes32UUIDFirst16(txID)
		txIDScBytes := xdr.ScBytes(txIDBytes32[:])
		topic1 := xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &txIDScBytes}
		notVecData := xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: new(xdr.Uint32)}
		ev := &pldapi.EventWithData{Data: jsonTopicsData(t, []xdr.ScVal{topic0, topic1}, notVecData)}
		_, _, _, err := decodeSaladinFactoryRegistration(ctx, ev)
		assert.ErrorContains(t, err, "expected a Vec")
	})

	t.Run("vec wrong length", func(t *testing.T) {
		sym := xdr.ScSymbol("reg")
		topic0 := xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
		txIDBytes32 := pldtypes.Bytes32UUIDFirst16(txID)
		txIDScBytes := xdr.ScBytes(txIDBytes32[:])
		topic1 := xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &txIDScBytes}
		configScBytes := xdr.ScBytes(config)
		vec := xdr.ScVec{{Type: xdr.ScValTypeScvBytes, Bytes: &configScBytes}}
		vecPtr := &vec
		dataVal := xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}
		ev := &pldapi.EventWithData{Data: jsonTopicsData(t, []xdr.ScVal{topic0, topic1}, dataVal)}
		_, _, _, err := decodeSaladinFactoryRegistration(ctx, ev)
		assert.ErrorContains(t, err, "expected 2 elements")
	})

	t.Run("vec[0] not an address", func(t *testing.T) {
		sym := xdr.ScSymbol("reg")
		topic0 := xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
		txIDBytes32 := pldtypes.Bytes32UUIDFirst16(txID)
		txIDScBytes := xdr.ScBytes(txIDBytes32[:])
		topic1 := xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &txIDScBytes}
		configScBytes := xdr.ScBytes(config)
		vec := xdr.ScVec{
			{Type: xdr.ScValTypeScvU32, U32: new(xdr.Uint32)},
			{Type: xdr.ScValTypeScvBytes, Bytes: &configScBytes},
		}
		vecPtr := &vec
		dataVal := xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}
		ev := &pldapi.EventWithData{Data: jsonTopicsData(t, []xdr.ScVal{topic0, topic1}, dataVal)}
		_, _, _, err := decodeSaladinFactoryRegistration(ctx, ev)
		assert.ErrorContains(t, err, "expected an Address value")
	})

	t.Run("vec[1] not bytes", func(t *testing.T) {
		sym := xdr.ScSymbol("reg")
		topic0 := xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
		txIDBytes32 := pldtypes.Bytes32UUIDFirst16(txID)
		txIDScBytes := xdr.ScBytes(txIDBytes32[:])
		topic1 := xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &txIDScBytes}
		contractID := xdr.ContractId(instanceContractID)
		scAddr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID}
		vec := xdr.ScVec{
			{Type: xdr.ScValTypeScvAddress, Address: &scAddr},
			{Type: xdr.ScValTypeScvU32, U32: new(xdr.Uint32)},
		}
		vecPtr := &vec
		dataVal := xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}
		ev := &pldapi.EventWithData{Data: jsonTopicsData(t, []xdr.ScVal{topic0, topic1}, dataVal)}
		_, _, _, err := decodeSaladinFactoryRegistration(ctx, ev)
		assert.ErrorContains(t, err, "expected a Bytes value")
	})
}
