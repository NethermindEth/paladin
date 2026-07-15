// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package stellar

import (
	"bytes"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

const testContractStrkey = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"

func TestBuildInvokeHostFunctionXDR(t *testing.T) {
	args := xdr.ScVec{xdr.ScVal{Type: xdr.ScValTypeScvBool, B: (*bool)(nil)}}
	trueVal := true
	args[0].B = &trueVal
	var argsBuf bytes.Buffer
	_, err := xdr.Marshal(&argsBuf, args)
	require.NoError(t, err)

	payload, err := BuildInvokeHostFunctionXDR(testContractStrkey, "transfer", argsBuf.Bytes())
	require.NoError(t, err)

	// Round-trip via the exact decode path buildStellarTx (submission side) uses.
	var hostFunction xdr.HostFunction
	require.NoError(t, hostFunction.UnmarshalBinary(payload))
	require.Equal(t, xdr.HostFunctionTypeHostFunctionTypeInvokeContract, hostFunction.Type)
	require.NotNil(t, hostFunction.InvokeContract)
	require.Equal(t, xdr.ScSymbol("transfer"), hostFunction.InvokeContract.FunctionName)
	require.Equal(t, xdr.ScAddressTypeScAddressTypeContract, hostFunction.InvokeContract.ContractAddress.Type)
	require.Len(t, hostFunction.InvokeContract.Args, 1)
	require.Equal(t, xdr.ScValTypeScvBool, hostFunction.InvokeContract.Args[0].Type)
	require.True(t, *hostFunction.InvokeContract.Args[0].B)
}

func TestBuildInvokeHostFunctionXDR_InvalidContractID(t *testing.T) {
	_, err := BuildInvokeHostFunctionXDR("not-a-strkey", "transfer", nil)
	require.ErrorContains(t, err, "invalid contract address")
}

func TestBuildInvokeHostFunctionXDR_InvalidArgs(t *testing.T) {
	_, err := BuildInvokeHostFunctionXDR(testContractStrkey, "transfer", []byte{0xff, 0xff})
	require.ErrorContains(t, err, "invalid host function args")
}
