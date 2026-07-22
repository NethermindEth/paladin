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

// SAtom's Go counterpart to atom_helper.go - the same Create/Execute shape, targeting the
// Soroban SAtomFactory/SAtom contracts instead of Solidity's AtomFactory/Atom. Soroban has no
// Solidity-style ABI to build call data from, so unlike functionBuilder (which resolves args
// against a real abi.Entry), this file hand-encodes each call's args as xdr.ScVal directly -
// mirroring domains/noto/internal/noto/chainio_stellar.go's own scValXYZ helpers, which take the
// same approach for the same reason (no formal contract-spec XDR to drive scspec's generic
// decoder for these ad hoc, known-in-advance shapes).
//
// Submission rides the txmgr Stellar raw-data-passthrough path (core/go/internal/txmgr/
// transaction_submission.go): a Public TxBuilder with no ABI, a Function name, and pre-built XDR
// call data supplied as a hex string via Inputs() - txmgr recognizes a Stellar `to` address and
// passes tx.Data through untouched instead of Solidity-ABI-encoding it.
package helpers

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"testing"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/core/pkg/testbed"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldclient"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

// SAtomOperation mirrors soroban/crates/atom-operation's AtomOperation Rust struct - one
// settlement leg. Args are already-encoded xdr.ScVal values (each caller encodes its own leg's
// arguments for whatever function it targets - AtomOperation itself is agnostic to their shape,
// exactly like Rust's `args: Vec<Val>` is opaque to the settlement contract).
type SAtomOperation struct {
	Contract string // strkey contract address ("C...") of the leg's target contract
	Function string
	Args     []xdr.ScVal
}

type SAtomFactoryHelper struct {
	t       *testing.T
	tb      testbed.Testbed
	pld     pldclient.PaladinClient
	Address string // strkey contract address ("C...") of the deployed SAtomFactory instance
}

type SAtomHelper struct {
	t       *testing.T
	tb      testbed.Testbed
	pld     pldclient.PaladinClient
	Address string // strkey contract address ("C...") of the deployed SAtom instance
}

func InitSAtomFactory(
	t *testing.T,
	tb testbed.Testbed,
	pld pldclient.PaladinClient,
	address string,
) *SAtomFactoryHelper {
	return &SAtomFactoryHelper{t: t, tb: tb, pld: pld, Address: address}
}

// DeploySettlement calls SAtomFactory.deploy_settlement(wasm_hash, operations, parties,
// saladin_factory, tx_id, config), which deploys a new SAtom instance salted on
// sha256(operations.to_xdr()), initializes it, and registers it with SaladinFactory - all in one
// invocation (soroban/contracts/satom-factory/src/lib.rs). The deployed instance's address is
// not returned to the caller here (Soroban surfaces it as the call's return value and as
// SaladinFactory's own "reg" contract event, neither of which this helper decodes - see
// DecodeSAtomRegistrationEvent below for the latter, once the caller has fetched the confirmed
// transaction's contract events).
func (f *SAtomFactoryHelper) DeploySettlement(
	ctx context.Context,
	signer string,
	wasmHash [32]byte,
	operations []*SAtomOperation,
	parties []string,
	saladinFactory string,
	txID [32]byte,
	config []byte,
) *TransactionHelper {
	args, err := deploySettlementArgs(wasmHash, operations, parties, saladinFactory, txID, config)
	require.NoError(f.t, err)
	builder := stellarFunctionBuilder(f.t, ctx, f.pld, f.Address, "deploy_settlement", args)
	return NewTransactionHelper(ctx, f.t, f.tb, builder.From(signer))
}

func (a *SAtomHelper) Execute(ctx context.Context, signer string) *TransactionHelper {
	builder := stellarFunctionBuilder(a.t, ctx, a.pld, a.Address, "execute", nil)
	return NewTransactionHelper(ctx, a.t, a.tb, builder.From(signer))
}

func (a *SAtomHelper) Cancel(ctx context.Context, signer string, canceller string) *TransactionHelper {
	args, err := addressArgs(canceller)
	require.NoError(a.t, err)
	builder := stellarFunctionBuilder(a.t, ctx, a.pld, a.Address, "cancel", args)
	return NewTransactionHelper(ctx, a.t, a.tb, builder.From(signer))
}

// stellarFunctionBuilder builds a Public TxBuilder with no ABI - the Soroban counterpart of
// functionBuilder (which resolves everything against a Solidity ABI, meaningless for a Soroban
// contract). args is the pre-encoded ScVal list for the call; the full HostFunction XDR payload
// is built here via stellar.BuildInvokeHostFunctionXDR and passed through Inputs() as a JSON
// hex string, which txmgr's Stellar raw-passthrough path (see this file's own doc comment)
// submits untouched.
func stellarFunctionBuilder(
	t *testing.T,
	ctx context.Context,
	pld pldclient.PaladinClient,
	contractAddr string,
	functionName string,
	args []xdr.ScVal,
) pldclient.TxBuilder {
	to, err := pldtypes.NewStellarContractAddress(contractAddr)
	require.NoError(t, err)

	argsXDR, err := marshalScVec(args)
	require.NoError(t, err)

	payload, err := stellar.BuildInvokeHostFunctionXDR(contractAddr, functionName, argsXDR)
	require.NoError(t, err)

	return pld.TxBuilder(ctx).Public().Function(functionName).ToChainAddress(&to).
		Inputs(fmt.Sprintf("%q", "0x"+hex.EncodeToString(payload)))
}

func marshalScVec(args []xdr.ScVal) ([]byte, error) {
	var buf bytes.Buffer
	vec := xdr.ScVec(args)
	if _, err := xdr.Marshal(&buf, vec); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func scValSymbol(s string) (xdr.ScVal, error) {
	return xdr.NewScVal(xdr.ScValTypeScvSymbol, xdr.ScSymbol(s))
}

func scValBytes(b []byte) (xdr.ScVal, error) {
	return xdr.NewScVal(xdr.ScValTypeScvBytes, xdr.ScBytes(b))
}

func scValVec(items []xdr.ScVal) (xdr.ScVal, error) {
	vec := xdr.ScVec(items)
	return xdr.NewScVal(xdr.ScValTypeScvVec, &vec)
}

func scValAddress(strkeyAddr string) (xdr.ScVal, error) {
	addr, err := scspec.AddressFromStrkey(strkeyAddr)
	if err != nil {
		return xdr.ScVal{}, err
	}
	return xdr.NewScVal(xdr.ScValTypeScvAddress, addr)
}

// scValStruct builds a #[contracttype] struct's SCVal - a SCMap with entries sorted by field
// name, matching soroban-sdk's own derive macro convention exactly (confirmed against
// sdk/go/pkg/scspec's sortScMap, which does the same for spec-driven UDT structs).
func scValStruct(fields map[string]xdr.ScVal) (xdr.ScVal, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	m := make(xdr.ScMap, len(names))
	for i, name := range names {
		key, err := scValSymbol(name)
		if err != nil {
			return xdr.ScVal{}, err
		}
		m[i] = xdr.ScMapEntry{Key: key, Val: fields[name]}
	}
	return xdr.NewScVal(xdr.ScValTypeScvMap, &m)
}

func atomOperationScVal(op *SAtomOperation) (xdr.ScVal, error) {
	contractVal, err := scValAddress(op.Contract)
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("operation contract %q: %w", op.Contract, err)
	}
	functionVal, err := scValSymbol(op.Function)
	if err != nil {
		return xdr.ScVal{}, fmt.Errorf("operation function %q: %w", op.Function, err)
	}
	argsVal, err := scValVec(op.Args)
	if err != nil {
		return xdr.ScVal{}, err
	}
	return scValStruct(map[string]xdr.ScVal{
		"contract": contractVal,
		"function": functionVal,
		"args":     argsVal,
	})
}

func addressArgs(strkeyAddr string) ([]xdr.ScVal, error) {
	v, err := scValAddress(strkeyAddr)
	if err != nil {
		return nil, err
	}
	return []xdr.ScVal{v}, nil
}

// deploySettlementArgs encodes SAtomFactory.deploy_settlement's positional args in Rust
// declaration order (soroban/contracts/satom-factory/src/lib.rs): wasm_hash, operations,
// parties, saladin_factory, tx_id, config.
func deploySettlementArgs(
	wasmHash [32]byte,
	operations []*SAtomOperation,
	parties []string,
	saladinFactory string,
	txID [32]byte,
	config []byte,
) ([]xdr.ScVal, error) {
	wasmHashVal, err := scValBytes(wasmHash[:])
	if err != nil {
		return nil, err
	}

	opVals := make([]xdr.ScVal, len(operations))
	for i, op := range operations {
		v, err := atomOperationScVal(op)
		if err != nil {
			return nil, fmt.Errorf("operation %d: %w", i, err)
		}
		opVals[i] = v
	}
	operationsVal, err := scValVec(opVals)
	if err != nil {
		return nil, err
	}

	partyVals := make([]xdr.ScVal, len(parties))
	for i, party := range parties {
		v, err := scValAddress(party)
		if err != nil {
			return nil, fmt.Errorf("party %d %q: %w", i, party, err)
		}
		partyVals[i] = v
	}
	partiesVal, err := scValVec(partyVals)
	if err != nil {
		return nil, err
	}

	saladinFactoryVal, err := scValAddress(saladinFactory)
	if err != nil {
		return nil, fmt.Errorf("saladin_factory %q: %w", saladinFactory, err)
	}

	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, err
	}

	configVal, err := scValBytes(config)
	if err != nil {
		return nil, err
	}

	return []xdr.ScVal{wasmHashVal, operationsVal, partiesVal, saladinFactoryVal, txIDVal, configVal}, nil
}

// DecodeSAtomRegistrationEvent extracts the deployed SAtom instance's address from
// SaladinFactory's "reg" contract event (soroban/contracts/factory/src/lib.rs's Registration
// event: topics = ["reg", tx_id], data = [instance, config], data_format = "vec"). Callers fetch
// the confirmed transaction's contract events themselves (e.g. via the Stellar RPC
// getTransaction/GetTransactionEvents call the demo script already needs for its stellar.expert
// links) and pass the matching event's raw topic/data XDR in here - this file has no ledger
// client of its own to fetch them with.
func DecodeSAtomRegistrationEvent(topicsXDR [][]byte, dataXDR []byte) (instanceAddr string, config []byte, err error) {
	if len(topicsXDR) < 2 {
		return "", nil, fmt.Errorf("registration event has %d topics, expected at least 2 (name, tx_id)", len(topicsXDR))
	}
	var nameVal xdr.ScVal
	if _, err := xdr.Unmarshal(bytes.NewReader(topicsXDR[0]), &nameVal); err != nil {
		return "", nil, fmt.Errorf("decoding topic[0]: %w", err)
	}
	name, ok := nameVal.GetSym()
	if !ok || string(name) != "reg" {
		return "", nil, fmt.Errorf("event topic[0] is not the \"reg\" symbol")
	}

	var dataVal xdr.ScVal
	if _, err := xdr.Unmarshal(bytes.NewReader(dataXDR), &dataVal); err != nil {
		return "", nil, fmt.Errorf("decoding event data: %w", err)
	}
	vec, ok := dataVal.GetVec()
	if !ok || vec == nil || len(*vec) != 2 {
		return "", nil, fmt.Errorf("registration event data is not a 2-element vec")
	}
	instanceAddrVal, ok := (*vec)[0].GetAddress()
	if !ok {
		return "", nil, fmt.Errorf("registration instance is not an address")
	}
	instanceAddr, err = scspec.AddressToStrkey(instanceAddrVal)
	if err != nil {
		return "", nil, fmt.Errorf("decoding registration instance address: %w", err)
	}
	configBytes, ok := (*vec)[1].GetBytes()
	if !ok {
		return "", nil, fmt.Errorf("registration config is not bytes")
	}
	return instanceAddr, []byte(configBytes), nil
}
