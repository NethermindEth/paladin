/*
 * Copyright © 2024 Kaleido, Inc.
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

package noto

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"slices"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/domains/noto/internal/msgs"
	notosmt "github.com/LFDT-Paladin/paladin/domains/noto/internal/noto/smt"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/query"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/smt"
	"github.com/LFDT-Paladin/smt/pkg/utxo"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/hyperledger/firefly-signer/pkg/eip712"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
)

var EIP712DomainName = "noto"
var EIP712DomainVersion = "0.0.1"
var EIP712DomainType = eip712.Type{
	{Name: "name", Type: "string"},
	{Name: "version", Type: "string"},
	{Name: "chainId", Type: "uint256"},
	{Name: "verifyingContract", Type: "address"},
}

var NotoCoinType = eip712.Type{
	{Name: "salt", Type: "bytes32"},
	{Name: "owner", Type: "address"},
	{Name: "amount", Type: "uint256"},
}

var NotoLockedCoinType = eip712.Type{
	{Name: "salt", Type: "bytes32"},
	{Name: "lockId", Type: "bytes32"},
	{Name: "owner", Type: "address"},
	{Name: "amount", Type: "uint256"},
}

var NotoTransferUnmaskedTypeSet = eip712.TypeSet{
	"Transfer": {
		{Name: "inputs", Type: "Coin[]"},
		{Name: "outputs", Type: "Coin[]"},
	},
	"Coin":              NotoCoinType,
	eip712.EIP712Domain: EIP712DomainType,
}

var NotoTransferMaskedTypeSet = eip712.TypeSet{
	"Transfer": {
		{Name: "inputs", Type: "bytes32[]"},
		{Name: "outputs", Type: "bytes32[]"},
		{Name: "data", Type: "bytes"},
	},
	eip712.EIP712Domain: EIP712DomainType,
}

var NotoLockTypeSet = eip712.TypeSet{
	"Lock": {
		{Name: "inputs", Type: "Coin[]"},
		{Name: "outputs", Type: "Coin[]"},
		{Name: "lockedOutputs", Type: "LockedCoin[]"},
	},
	"LockedCoin":        NotoLockedCoinType,
	"Coin":              NotoCoinType,
	eip712.EIP712Domain: EIP712DomainType,
}

var NotoUnlockTypeSet = eip712.TypeSet{
	"Unlock": {
		{Name: "lockedInputs", Type: "LockedCoin[]"},
		{Name: "lockedOutputs", Type: "LockedCoin[]"},
		{Name: "outputs", Type: "Coin[]"},
	},
	"LockedCoin":        NotoLockedCoinType,
	"Coin":              NotoCoinType,
	eip712.EIP712Domain: EIP712DomainType,
}

var NotoUnlockMaskedTypeSet_V0 = eip712.TypeSet{
	"Unlock": {
		{Name: "lockedInputs", Type: "bytes32[]"},
		{Name: "lockedOutputs", Type: "bytes32[]"},
		{Name: "outputs", Type: "bytes32[]"},
		{Name: "data", Type: "bytes"},
	},
	eip712.EIP712Domain: EIP712DomainType,
}

var NotoUnlockMaskedTypeSet_V1 = eip712.TypeSet{
	"Unlock": {
		{Name: "txId", Type: "bytes32"},
		{Name: "lockedInputs", Type: "bytes32[]"},
		{Name: "outputs", Type: "bytes32[]"},
		{Name: "data", Type: "bytes"},
	},
	eip712.EIP712Domain: EIP712DomainType,
}

var NotoDelegateLockTypeSet = eip712.TypeSet{
	"DelegateLock": {
		{Name: "lockId", Type: "bytes32"},
		{Name: "delegate", Type: "address"},
		{Name: "data", Type: "bytes"},
	},
	eip712.EIP712Domain: EIP712DomainType,
}

func (n *Noto) unmarshalCoin(stateData string) (*types.NotoCoin, error) {
	var coin types.NotoCoin
	err := json.Unmarshal([]byte(stateData), &coin)
	return &coin, err
}

func (n *Noto) unmarshalLockedCoin(stateData string) (*types.NotoLockedCoin, error) {
	var coin types.NotoLockedCoin
	err := json.Unmarshal([]byte(stateData), &coin)
	return &coin, err
}

func (n *Noto) unmarshalInfo(stateData string) (*types.TransactionData, error) {
	var info types.TransactionData
	err := json.Unmarshal([]byte(stateData), &info)
	return &info, err
}

func (n *Noto) unmarshalLockV0(stateData string) (*types.NotoLockInfo_V0, error) {
	var lock types.NotoLockInfo_V0
	err := json.Unmarshal([]byte(stateData), &lock)
	return &lock, err
}

func (n *Noto) unmarshalLockV1(stateData string) (*types.NotoLockInfo_V1, error) {
	var lock types.NotoLockInfo_V1
	err := json.Unmarshal([]byte(stateData), &lock)
	return &lock, err
}

func (n *Noto) makeNewCoinState(coin *types.NotoCoin, distributionList []string) (*prototk.NewState, error) {
	coinJSON, err := json.Marshal(coin)
	if err != nil {
		return nil, err
	}
	return &prototk.NewState{
		SchemaId:         n.coinSchema.Id,
		StateDataJson:    string(coinJSON),
		DistributionList: distributionList,
	}, nil
}

func (n *Noto) makeNewLockedCoinState(coin *types.NotoLockedCoin, distributionList []string) (*prototk.NewState, error) {
	coinJSON, err := json.Marshal(coin)
	if err != nil {
		return nil, err
	}
	return &prototk.NewState{
		SchemaId:         n.lockedCoinSchema.Id,
		StateDataJson:    string(coinJSON),
		DistributionList: distributionList,
	}, nil
}

func (n *Noto) makeNewInfoState(info *types.TransactionData, variant pldtypes.HexUint64, distributionList []string) (*prototk.NewState, error) {
	infoJSON, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	switch variant {
	case types.NotoVariantV0:
		return &prototk.NewState{
			SchemaId:         n.dataSchemaV0.Id,
			StateDataJson:    string(infoJSON),
			DistributionList: distributionList,
		}, nil
	case types.NotoVariantV1:
		// TransactionDataABI_V1 (unlike V2) has no "from" component - the only branch here that
		// works when info.From is nil, which it always is for a party with no resolvable EVM
		// address (e.g. a Stellar-only party - discovered by hitting this live: previously this
		// function had no V1 case at all and fell through to the V2 schema below, which then
		// failed ABI-encoding with "Input map missing key 'from' required for tuple component
		// .from" for exactly that reason).
		return &prototk.NewState{
			SchemaId:         n.dataSchemaV1.Id,
			StateDataJson:    string(infoJSON),
			DistributionList: distributionList,
		}, nil
	default:
		return &prototk.NewState{
			SchemaId:         n.dataSchemaV2.Id,
			StateDataJson:    string(infoJSON),
			DistributionList: distributionList,
		}, nil
	}
}

func (n *Noto) makeNewManifestInfoState(manifest *types.NotoManifest, distributionList []string) (*prototk.NewState, error) {
	infoJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return &prototk.NewState{
		SchemaId:         n.manifestSchema.Id,
		StateDataJson:    string(infoJSON),
		DistributionList: distributionList,
	}, nil
}

func (n *Noto) makeNewLockState_V0(lock *types.NotoLockInfo_V0, distributionList []string) (*prototk.NewState, error) {
	lockJSON, err := json.Marshal(lock)
	if err != nil {
		return nil, err
	}
	return &prototk.NewState{
		SchemaId:         n.lockInfoSchemaV0.Id,
		StateDataJson:    string(lockJSON),
		DistributionList: distributionList,
	}, nil
}

func (n *Noto) makeNewLockState_V1(lock *types.NotoLockInfo_V1, distributionList []string) (*prototk.NewState, error) {
	lockJSON, err := json.Marshal(lock)
	if err != nil {
		return nil, err
	}
	return &prototk.NewState{
		SchemaId:         n.lockInfoSchemaV1.Id,
		StateDataJson:    string(lockJSON),
		DistributionList: distributionList,
	}, nil
}

type preparedInputs struct {
	coins  []*types.NotoCoin
	states []*prototk.StateRef
	total  *big.Int
}

type preparedLockedInputs struct {
	coins  []*types.NotoLockedCoin
	states []*prototk.StateRef
	total  *big.Int
}

type preparedOutputs struct {
	distributions []identityList
	coins         []*types.NotoCoin
	states        []*prototk.NewState
}

type preparedLockedOutputs struct {
	distributions []identityList
	coins         []*types.NotoLockedCoin
	states        []*prototk.NewState
}

type preparedLockInfo struct {
	distribution identityList
	stateV0      *types.NotoLockInfo_V0
	stateV1      *types.NotoLockInfo_V1
	state        *prototk.NewState
}

type identityPair struct {
	identifier string
	// address is EVM-only, used pervasively by lock/unlock/burn/transfer code this pass doesn't
	// touch - nil when resolved by stellarChainIO.
	address *pldtypes.EthAddress
	// chainAddress is the chain-neutral representation (step 4), populated by every chainIO
	// implementer - this is what NotoCoin.Owner is now sourced from (see prepareOutputs).
	chainAddress pldtypes.ChainAddress
}

type identityList []*identityPair

// gets the paladin identities, with de-duplication
func (idl identityList) identities() []string {
	al := make([]string, 0, len(idl))
skipDuplicate:
	for _, id := range idl {
		for _, existing := range al {
			if existing == id.identifier {
				continue skipDuplicate
			}
		}
		al = append(al, id.identifier)
	}
	return al
}

// gets the ethereum addresses, with de-duplication
func (idl identityList) addresses() []*pldtypes.EthAddress {
	al := make([]*pldtypes.EthAddress, 0, len(idl))
skipDuplicate:
	for _, id := range idl {
		for _, existing := range al {
			if existing.Equals(id.address) {
				continue skipDuplicate
			}
		}
		al = append(al, id.address)
	}
	return al
}

// chainAddressStrings gets each identity's chain-neutral verifier string (id.chainAddress,
// populated by every chainIO implementer - see identityPair's own doc comment), with
// de-duplication - the manifest's own "participants" list (manifest.go's finalizeNewState) needs
// this rather than addresses() above, since a Stellar-only identity has no EthAddress at all.
func (idl identityList) chainAddressStrings() []string {
	al := make([]string, 0, len(idl))
skipDuplicate:
	for _, id := range idl {
		s := id.chainAddress.String()
		for _, existing := range al {
			if existing == s {
				continue skipDuplicate
			}
		}
		al = append(al, s)
	}
	return al
}

func (n *Noto) prepareInputs(ctx context.Context, stateQueryContext string, owner *identityPair, amount *pldtypes.HexUint256, useNullifiers bool) (inputs *preparedInputs, revert bool, err error) {
	var lastStateTimestamp int64
	total := big.NewInt(0)
	stateRefs := []*prototk.StateRef{}
	coins := []*types.NotoCoin{}
	// Supports being called with a zero transfer
	for total.Cmp(amount.Int()) < 0 {
		// TODO: make the coin selection configurable - currently selects oldest coins
		queryBuilder := query.NewQueryBuilder().
			Limit(10).
			Sort(".created").
			// owner.chainAddress (chain-neutral) not owner.address (EVM-only, nil for a
			// Stellar-resolved identity) - safe for existing EVM coins, since a ChainAddress's
			// EVM-kind text is exactly EthAddress.String() (see NotoCoin.Owner's doc comment).
			Equal("owner", owner.chainAddress.String())

		if lastStateTimestamp > 0 {
			queryBuilder.GreaterThan(".created", lastStateTimestamp)
		}

		log.L(ctx).Debugf("State query: %s", queryBuilder.Query())
		states, err := n.findAvailableStates(ctx, stateQueryContext, n.coinSchema.Id, queryBuilder.Query().String(), useNullifiers)
		if err != nil {
			return nil, false, err
		}
		if len(states) == 0 {
			return nil, true, i18n.NewError(ctx, msgs.MsgInsufficientFunds, total.Text(10))
		}
		for _, state := range states {
			lastStateTimestamp = state.CreatedAt
			coin, err := n.unmarshalCoin(state.DataJson)
			if err != nil {
				return nil, false, i18n.NewError(ctx, msgs.MsgInvalidStateData, state.Id, err)
			}
			total = total.Add(total, coin.Amount.Int())
			stateRefs = append(stateRefs, &prototk.StateRef{
				SchemaId: state.SchemaId,
				Id:       state.Id,
			})
			coins = append(coins, coin)
			log.L(ctx).Debugf("Selecting coin %s value=%s total=%s required=%s)", state.Id, coin.Amount.Int().Text(10), total.Text(10), amount.Int().Text(10))
		}
	}
	return &preparedInputs{
		coins:  coins,
		states: stateRefs,
		total:  total,
	}, false, nil
}

// Select from available locked states for a given lock ID and owner,
// ensuring the total amount is at least the specified amount.
// If selectAll is true, ALL available states will be found selected.
func (n *Noto) prepareLockedInputs(ctx context.Context, stateQueryContext string, lockID pldtypes.Bytes32, owner *pldtypes.ChainAddress, amount *big.Int, selectAll bool) (inputs *preparedLockedInputs, revert bool, err error) {
	var lastStateTimestamp int64
	total := big.NewInt(0)
	stateRefs := []*prototk.StateRef{}
	coins := []*types.NotoLockedCoin{}
	limit := 10

	for {
		queryBuilder := query.NewQueryBuilder().
			Limit(limit).
			Sort(".created").
			Equal("lockId", lockID).
			Equal("owner", owner.String())

		if lastStateTimestamp > 0 {
			queryBuilder.GreaterThan(".created", lastStateTimestamp)
		}

		log.L(ctx).Debugf("State query: %s", queryBuilder.Query())
		states, err := n.findAvailableStates(ctx, stateQueryContext, n.lockedCoinSchema.Id, queryBuilder.Query().String(), false)
		if err != nil {
			return nil, false, err
		}
		for _, state := range states {
			lastStateTimestamp = state.CreatedAt
			coin, err := n.unmarshalLockedCoin(state.DataJson)
			if err != nil {
				return nil, false, i18n.NewError(ctx, msgs.MsgInvalidStateData, state.Id, err)
			}
			total = total.Add(total, coin.Amount.Int())
			stateRefs = append(stateRefs, &prototk.StateRef{
				SchemaId: state.SchemaId,
				Id:       state.Id,
			})
			coins = append(coins, coin)
			log.L(ctx).Debugf("Selecting coin %s value=%s total=%s required=%s)", state.Id, coin.Amount.Int().Text(10), total.Text(10), amount.Text(10))
			if total.Cmp(amount) >= 0 && !selectAll {
				// total achieved - stop here unless we need to select all states
				break
			}
		}

		if len(states) < limit {
			// no more states to select
			break
		}
	}
	if total.Cmp(amount) >= 0 {
		return &preparedLockedInputs{
			coins:  coins,
			states: stateRefs,
			total:  total,
		}, false, nil
	}
	return nil, true, i18n.NewError(ctx, msgs.MsgInsufficientFunds, total.Text(10))
}

func (n *Noto) prepareOutputs(owner *identityPair, amount *pldtypes.HexUint256, distributionList identityList) (*preparedOutputs, error) {
	// Always produce a single coin for the entire output amount
	// TODO: make this configurable
	newCoin := &types.NotoCoin{
		Salt:   pldtypes.RandBytes32(),
		Owner:  &owner.chainAddress,
		Amount: amount,
	}
	newState, err := n.makeNewCoinState(newCoin, distributionList.identities())
	return &preparedOutputs{
		distributions: []identityList{distributionList},
		coins:         []*types.NotoCoin{newCoin},
		states:        []*prototk.NewState{newState},
	}, err
}

func (n *Noto) prepareLockedOutputs(id pldtypes.Bytes32, owner *identityPair, amount *pldtypes.HexUint256, distributionList identityList) (*preparedLockedOutputs, error) {

	// No outputs if we're preparing an empty lock
	if amount.Int().Sign() <= 0 {
		return &preparedLockedOutputs{
			distributions: []identityList{},
			coins:         []*types.NotoLockedCoin{},
			states:        []*prototk.NewState{},
		}, nil
	}

	// Always produce a single coin for the entire output amount
	// TODO: make this configurable
	newCoin := &types.NotoLockedCoin{
		Salt:   pldtypes.RandBytes32(),
		LockID: id,
		Owner:  &owner.chainAddress,
		Amount: amount,
	}
	newState, err := n.makeNewLockedCoinState(newCoin, distributionList.identities())
	return &preparedLockedOutputs{
		distributions: []identityList{distributionList},
		coins:         []*types.NotoLockedCoin{newCoin},
		states:        []*prototk.NewState{newState},
	}, err
}

func (n *Noto) prepareDataInfo(ctx context.Context, data pldtypes.HexBytes, variant pldtypes.HexUint64, distributionList []string, transaction *prototk.TransactionSpecification, verifiers []*prototk.ResolvedVerifier) ([]*prototk.NewState, error) {
	newData := &types.TransactionData{
		Salt:    pldtypes.RandBytes32(),
		Data:    data,
		Variant: variant,
	}
	fromAddr, err := n.findEthAddressVerifier(ctx, "from", transaction.From, verifiers)
	if err == nil && fromAddr != nil {
		newData.From = fromAddr.address
	}
	newState, err := n.makeNewInfoState(newData, variant, distributionList)
	return []*prototk.NewState{newState}, err
}

// evmChainAddressPtr wraps an EVM address as a *pldtypes.ChainAddress, or nil if addr is nil -
// used wherever an EVM-only request parameter (e.g. DelegateLockParams.Delegate) needs to flow
// into a now chain-neutral field.
func evmChainAddressPtr(addr *pldtypes.EthAddress) *pldtypes.ChainAddress {
	if addr == nil {
		return nil
	}
	ca := pldtypes.NewEVMChainAddress(*addr)
	return &ca
}

func (n *Noto) prepareLockInfo_V0(lockID pldtypes.Bytes32, owner, delegate *pldtypes.ChainAddress, distributionList identityList) (*preparedLockInfo, error) {
	if delegate == nil {
		// V0 is EVM-only (never extended to Stellar - chapter 14 lock/unlock phase). A zero-value
		// ChainAddress{} marshals as an empty JSON string, which then fails to round-trip back
		// (ChainAddress.UnmarshalJSON rejects ""), unlike the old zero *pldtypes.EthAddress{},
		// whose zero value ("0x000...0") round-trips fine - so default to the equivalent EVM zero
		// address instead of a bare zero ChainAddress.
		delegate = evmChainAddressPtr(&pldtypes.EthAddress{})
	}
	newLockInfo := &types.NotoLockInfo_V0{
		Salt:     pldtypes.RandBytes32(),
		LockID:   lockID,
		Owner:    owner,
		Delegate: delegate,
	}
	lockState, err := n.makeNewLockState_V0(newLockInfo, distributionList.identities())
	if err != nil {
		return nil, err
	}
	return &preparedLockInfo{
		stateV0:      newLockInfo,
		state:        lockState,
		distribution: distributionList,
	}, nil
}

func (n *Noto) prepareLockInfo_V1(newLockInfo *types.NotoLockInfo_V1, distributionList identityList) (*preparedLockInfo, error) {
	lockState, err := n.makeNewLockState_V1(newLockInfo, distributionList.identities())
	if err != nil {
		return nil, err
	}
	return &preparedLockInfo{
		stateV1:      newLockInfo,
		state:        lockState,
		distribution: distributionList,
	}, nil
}

func (n *Noto) filterSchema(states []*prototk.EndorsableState, schemas []string) (filtered []*prototk.EndorsableState) {
	for _, state := range states {
		if slices.Contains(schemas, state.SchemaId) {
			filtered = append(filtered, state)
		}
	}
	return filtered
}

func (n *Noto) splitStates(states []*prototk.EndorsableState) (unlocked []*prototk.EndorsableState, locked []*prototk.EndorsableState) {
	return n.filterSchema(states, []string{n.coinSchema.Id}), n.filterSchema(states, []string{n.lockedCoinSchema.Id})
}

func (n *Noto) getStates(ctx context.Context, stateQueryContext, schemaId string, ids []string) ([]*prototk.StoredState, error) {
	req := &prototk.GetStatesByIDRequest{
		StateQueryContext: stateQueryContext,
		SchemaId:          schemaId,
		StateIds:          ids,
	}
	res, err := n.Callbacks.GetStatesByID(ctx, req)
	if err != nil {
		return nil, err
	}
	return res.States, nil
}

func (n *Noto) findAvailableStates(ctx context.Context, stateQueryContext, schemaId, query string, useNullifiers bool) ([]*prototk.StoredState, error) {
	req := &prototk.FindAvailableStatesRequest{
		StateQueryContext: stateQueryContext,
		SchemaId:          schemaId,
		QueryJson:         query,
		UseNullifiers:     &useNullifiers,
	}
	res, err := n.Callbacks.FindAvailableStates(ctx, req)
	if err != nil {
		return nil, err
	}
	return res.States, nil
}

func encodedStateIDs(states []*pldapi.StateEncoded) []string {
	inputs := make([]string, len(states))
	for i, state := range states {
		inputs[i] = state.ID.String()
	}
	return inputs
}

func endorsableStateIDs(ctx context.Context, states []*prototk.EndorsableState, useNullifier bool) []string {
	inputs := make([]string, len(states))
	for i, state := range states {
		if !useNullifier {
			inputs[i] = state.Id
		} else {
			// Use the nullifier as the ID
			var coin types.NotoCoin
			var hashBytes *pldtypes.Bytes32
			err := json.Unmarshal([]byte(state.StateDataJson), &coin)
			if err == nil {
				hashBytes, err = calculateNullifier(&coin)
			}
			if err != nil {
				log.L(ctx).Errorf("error calculating nullifier for state %s: %v", state.Id, err)
				return nil
			}
			inputs[i] = hashBytes.HexString()
		}
	}
	return inputs
}

// IDs must previously have been allocated
func newStateAllocatedIDs(states []*prototk.NewState) []pldtypes.Bytes32 {
	inputs := make([]pldtypes.Bytes32, len(states))
	for i, state := range states {
		inputs[i] = pldtypes.MustParseBytes32(*state.Id)
	}
	return inputs
}

func stringIDs(ids []pldtypes.Bytes32) []string {
	result := make([]string, len(ids))
	for i, id := range ids {
		result[i] = id.String()
	}
	return result
}

func stringToAny(ids []string) []any {
	result := make([]any, len(ids))
	for i, id := range ids {
		result[i] = id
	}
	return result
}

// Thin delegations to n.chainIO (chapter 14 step 3) - see chainio.go/chainio_evm.go for the
// actual EIP-712 encoding logic, moved there verbatim.

func (n *Noto) encodeTransferUnmasked(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*types.NotoCoin) (ethtypes.HexBytes0xPrefix, error) {
	return n.getChainIO().EncodeTransferUnmasked(ctx, contract, inputs, outputs)
}

func (n *Noto) encodeTransferMasked(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*pldapi.StateEncoded, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return n.getChainIO().EncodeTransferMasked(ctx, contract, inputs, outputs, data)
}

func (n *Noto) encodeLock(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*types.NotoCoin, lockedOutputs []*types.NotoLockedCoin) (ethtypes.HexBytes0xPrefix, error) {
	return n.getChainIO().EncodeLock(ctx, contract, inputs, outputs, lockedOutputs)
}

func (n *Noto) encodeUnlock(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs []*types.NotoLockedCoin, outputs []*types.NotoCoin) (ethtypes.HexBytes0xPrefix, error) {
	return n.getChainIO().EncodeUnlock(ctx, contract, lockedInputs, lockedOutputs, outputs)
}

func (n *Noto) unlockHashFromIDs_V0(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs, outputs []string, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return n.getChainIO().UnlockHashFromIDsV0(ctx, contract, lockedInputs, lockedOutputs, outputs, data)
}

func (n *Noto) unlockHashFromIDs_V1(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, txId string, lockedInputs, outputs []string, data pldtypes.HexBytes, purpose string, realContractID string) (pldtypes.Bytes32, error) {
	return n.getChainIO().UnlockHashFromIDsV1(ctx, contract, lockID, txId, lockedInputs, outputs, data, purpose, realContractID)
}

func (n *Noto) encodeDelegateLock(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, delegate *pldtypes.ChainAddress, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return n.getChainIO().EncodeDelegateLock(ctx, contract, lockID, delegate, data)
}

func (n *Noto) getAccountBalance(ctx context.Context, stateQueryContext string, owner *pldtypes.EthAddress, useNullifiers bool) (totalStates int, totalBalance *big.Int, overflow, revert bool, err error) {
	totalBalance = big.NewInt(0)
	queryBuilder := query.NewQueryBuilder().
		Limit(1000).
		Equal("owner", owner.String())

	log.L(ctx).Debugf("State query: %s", queryBuilder.Query())
	states, err := n.findAvailableStates(ctx, stateQueryContext, n.coinSchema.Id, queryBuilder.Query().String(), useNullifiers)
	if err != nil {
		return 0, nil, false, false, err
	}
	for _, state := range states {
		coin, err := n.unmarshalCoin(state.DataJson)
		if err != nil {
			return 0, nil, false, false, i18n.NewError(ctx, msgs.MsgInvalidStateData, state.Id, err)
		}
		totalBalance = totalBalance.Add(totalBalance, coin.Amount.Int())
	}
	if len(states) == 1000 {
		// We only return the first 1000 coins, so we warn that the balance may be higher
		return len(states), totalBalance, true, false, nil
	}

	return len(states), totalBalance, false, false, nil
}

func (n *Noto) encodeRootAndSignature(ctx context.Context, txContractAddress, stateQueryContext string, payload []byte) ([]byte, error) {
	// for nullifier variants, the "signature" parameter includes both the signature and the root
	smtName := notosmt.MerkleTreeName(txContractAddress)
	smtType := smt.StatesTree
	hasher := utxo.NewKeccak256Hasher()
	mt, err := smt.NewMerkleTreeSpec(ctx, smtName, smtType, notosmt.SMT_HEIGHT_UTXO, hasher, true, n.Callbacks, n.merkleTreeRootSchema.Id, n.merkleTreeNodeSchema.Id, stateQueryContext)
	if err != nil {
		return nil, err
	}
	root := mt.Tree.Root()
	jsonObj := map[string]interface{}{
		"root":      "0x" + root.BigInt().Text(16),
		"signature": "0x" + hex.EncodeToString(payload),
	}
	jsonBytes, err := json.Marshal(jsonObj)
	if err != nil {
		return nil, err
	}
	args := abi.ParameterArray{
		{Name: "root", Type: "uint256"},
		{Name: "signature", Type: "bytes"},
	}
	encoded, err := args.EncodeABIDataJSON(jsonBytes)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func calculateNullifier(coin *types.NotoCoin) (*pldtypes.Bytes32, error) {
	// the nullifier is keccak256(salt, amount)
	// first abi encode the salt and amount
	paramTypes := abi.ParameterArray{
		&abi.Parameter{
			Type: "uint256",
			Name: "amount",
		},
		&abi.Parameter{
			Type: "bytes32",
			Name: "salt",
		},
	}
	paramValues := map[string]any{
		"amount": coin.Amount.Int(),
		"salt":   coin.Salt,
	}

	jsonData, err := json.Marshal(paramValues)
	if err != nil {
		return nil, err
	}

	encoded, err := paramTypes.EncodeABIDataJSON(jsonData)
	if err != nil {
		return nil, err
	}
	// then keccak256 the result
	ret := pldtypes.Bytes32Keccak(encoded)
	return &ret, nil
}

func (n *Noto) allocateStateIDs(ctx context.Context, stateQueryContext string, stateLists ...[]*prototk.NewState) error {
	var allStates []*prototk.NewState
	for _, stateList := range stateLists {
		allStates = append(allStates, stateList...)
	}

	// Send them to Paladin to validate and generate the IDs
	validatedStates, err := n.Callbacks.ValidateStates(ctx, &prototk.ValidateStatesRequest{
		StateQueryContext: stateQueryContext,
		States:            allStates,
	})
	if err != nil {
		return err
	}

	// Store the IDs back into the objects - we do this because it means we'll send them down
	// to Paladin as a result of the assemble, and that
	for i, vs := range validatedStates.States {
		generatedID := vs.Id
		allStates[i].Id = &generatedID
	}
	return nil
}
