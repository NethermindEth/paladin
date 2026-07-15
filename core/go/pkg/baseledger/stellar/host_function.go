// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package stellar

import (
	"bytes"
	"fmt"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// BuildInvokeHostFunctionXDR builds the XDR encoding of a Soroban InvokeContract host function
// invocation, for a domain plugin's chain-neutral prototk.SorobanInvoke - the counterpart to
// buildStellarTx's decode of the same shape (ptx.Data -> xdr.HostFunction). argsXDR is the XDR
// encoding of an xdr.ScVec (SorobanInvoke.ArgsXdr, produced the same way domains/noto's
// chainio_stellar.go encodes call arguments).
func BuildInvokeHostFunctionXDR(contractID, functionName string, argsXDR []byte) ([]byte, error) {
	contractAddress, err := scspec.AddressFromStrkey(contractID)
	if err != nil {
		return nil, fmt.Errorf("invalid contract address %q: %w", contractID, err)
	}
	var args xdr.ScVec
	if _, err := xdr.Unmarshal(bytes.NewReader(argsXDR), &args); err != nil {
		return nil, fmt.Errorf("invalid host function args: %w", err)
	}
	hostFunction := xdr.HostFunction{
		Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
		InvokeContract: &xdr.InvokeContractArgs{
			ContractAddress: contractAddress,
			FunctionName:    xdr.ScSymbol(functionName),
			Args:            args,
		},
	}
	payload, err := hostFunction.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to encode host function: %w", err)
	}
	return payload, nil
}
