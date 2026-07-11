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

// classic_ops.go implements chapter 12 §12.3's baseledger.PayloadEncodingXDRClassicOps codec, and
// the small set of admin utilities it exists to serve: establishing/approving/freezing trustlines
// for native-asset (SAC) support. Deliberately narrow, per the book's own scope-creep warning
// (risk R22): this is account/trustline plumbing, not a gateway to classic-Stellar features -
// payment channels, offers/DEX, and claimable balances stay out of the BLI.
//
// Exported so core/go/internal/publictxmgr's stellarChainSubmitter (a different package) can share
// this exact codec rather than re-implementing it, unlike the XDR_INVOKE_CONTRACT_ARGS path, whose
// decode logic is duplicated between Client.buildTransaction and stellarChainSubmitter.buildStellarTx.
package stellar

import (
	"bytes"
	"fmt"

	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// DecodeClassicOperations decodes an XDR_CLASSIC_OPS payload: a plain XDR array of xdr.Operation
// (the same shape a transaction's own operations<> field carries, without the enclosing envelope).
func DecodeClassicOperations(payload []byte) ([]txnbuild.Operation, error) {
	var xdrOps []xdr.Operation
	if _, err := xdr.Unmarshal(bytes.NewReader(payload), &xdrOps); err != nil {
		return nil, fmt.Errorf("invalid classic operations payload: %w", err)
	}
	if len(xdrOps) == 0 {
		return nil, fmt.Errorf("at least one classic operation is required")
	}
	ops := make([]txnbuild.Operation, len(xdrOps))
	for i, xdrOp := range xdrOps {
		op, err := decodeClassicOperation(xdrOp)
		if err != nil {
			return nil, fmt.Errorf("operation %d: %w", i, err)
		}
		ops[i] = op
	}
	return ops, nil
}

func decodeClassicOperation(op xdr.Operation) (txnbuild.Operation, error) {
	var built txnbuild.Operation
	switch op.Body.Type {
	case xdr.OperationTypeCreateAccount:
		built = &txnbuild.CreateAccount{}
	case xdr.OperationTypePayment:
		built = &txnbuild.Payment{}
	case xdr.OperationTypeChangeTrust:
		built = &txnbuild.ChangeTrust{}
	case xdr.OperationTypeSetTrustLineFlags:
		built = &txnbuild.SetTrustLineFlags{}
	default:
		return nil, fmt.Errorf("unsupported classic operation type %q - only CreateAccount, Payment, ChangeTrust, and SetTrustLineFlags are supported", op.Body.Type)
	}
	if err := built.FromXDR(op); err != nil {
		return nil, fmt.Errorf("failed to decode %s operation: %w", op.Body.Type, err)
	}
	return built, nil
}

// EncodeClassicOperations is the inverse of DecodeClassicOperations - used by callers assembling
// an XDR_CLASSIC_OPS baseledger.UnsignedChainTx.Payload.
func EncodeClassicOperations(ops []txnbuild.Operation) ([]byte, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("at least one classic operation is required")
	}
	xdrOps := make([]xdr.Operation, len(ops))
	for i, op := range ops {
		xdrOp, err := op.BuildXDR()
		if err != nil {
			return nil, fmt.Errorf("operation %d: %w", i, err)
		}
		xdrOps[i] = xdrOp
	}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, xdrOps); err != nil {
		return nil, fmt.Errorf("failed to encode classic operations: %w", err)
	}
	return buf.Bytes(), nil
}

// BuildChangeTrustPayload builds an XDR_CLASSIC_OPS payload for a single ChangeTrust operation -
// e.g. a local identity establishing a trustline to a regulated asset before an unshield to a
// fresh account (chapter 12 §12.3). A trustline can only be created by its own holder, so the
// resulting UnsignedChainTx.From must be that holder's own address. limit="" uses
// txnbuild.MaxTrustlineLimit (no cap).
func BuildChangeTrustPayload(asset txnbuild.Asset, limit string) ([]byte, error) {
	changeTrustAsset, err := asset.ToChangeTrustAsset()
	if err != nil {
		return nil, fmt.Errorf("invalid asset: %w", err)
	}
	return EncodeClassicOperations([]txnbuild.Operation{&txnbuild.ChangeTrust{Line: changeTrustAsset, Limit: limit}})
}

// BuildSetTrustLineFlagsPayload builds an XDR_CLASSIC_OPS payload for an issuer to
// approve/freeze/clawback-enable a holder's trustline (chapter 12 §12.3) - trustor is the G...
// account whose trustline is being modified; the resulting UnsignedChainTx.From must be the
// asset's issuer account.
func BuildSetTrustLineFlagsPayload(trustor string, asset txnbuild.Asset, setFlags, clearFlags []txnbuild.TrustLineFlag) ([]byte, error) {
	return EncodeClassicOperations([]txnbuild.Operation{&txnbuild.SetTrustLineFlags{
		Trustor:    trustor,
		Asset:      asset,
		SetFlags:   setFlags,
		ClearFlags: clearFlags,
	}})
}
