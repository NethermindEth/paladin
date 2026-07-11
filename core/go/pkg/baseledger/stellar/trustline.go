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

package stellar

import (
	"context"
	"fmt"
	"math/big"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// TrustlineStatus is CheckTrustline's result (chapter 12 §12.3): called by domains at assembly
// time so an unshield to a missing/frozen/full trustline fails fast with an actionable error
// instead of an on-chain failure burning fees.
type TrustlineStatus struct {
	Exists        bool
	Authorized    bool
	LimitHeadroom *big.Int // Limit - Balance; nil if Exists is false
}

// CheckTrustline looks up account's trustline to asset via getLedgerEntries - a direct ledger-key
// read, no simulateTransaction/footprint involved (classic ledger entries, not Soroban state).
// account.Kind() is expected to be stellar_account (G...); asset is a native.NativeAsset or a
// txnbuild.CreditAsset. A missing entry is reported as TrustlineStatus{Exists: false}, not an
// error - native XLM trivially "exists" and is always authorized (no trustline concept applies),
// so callers should skip this check entirely for txnbuild.NativeAsset.
func CheckTrustline(ctx context.Context, rpc rpcClient, account pldtypes.ChainAddress, asset txnbuild.Asset) (*TrustlineStatus, error) {
	accountID, err := xdr.AddressToAccountId(account.String())
	if err != nil {
		return nil, fmt.Errorf("invalid account address: %w", err)
	}
	trustLineAsset, err := asset.ToTrustLineAsset()
	if err != nil {
		return nil, fmt.Errorf("invalid asset: %w", err)
	}
	xdrTrustLineAsset, err := trustLineAsset.ToXDR()
	if err != nil {
		return nil, fmt.Errorf("invalid asset: %w", err)
	}

	ledgerKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeTrustline,
		TrustLine: &xdr.LedgerKeyTrustLine{
			AccountId: accountID,
			Asset:     xdrTrustLineAsset,
		},
	}
	keyXDR, err := xdr.MarshalBase64(ledgerKey)
	if err != nil {
		return nil, fmt.Errorf("failed to build trustline ledger key: %w", err)
	}

	resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{keyXDR}})
	if err != nil {
		return nil, err
	}
	if len(resp.Entries) == 0 {
		return &TrustlineStatus{Exists: false}, nil
	}

	var entryData xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(resp.Entries[0].DataXDR, &entryData); err != nil {
		return nil, fmt.Errorf("invalid trustline ledger entry: %w", err)
	}
	if entryData.TrustLine == nil {
		return nil, fmt.Errorf("ledger entry for the requested key is not a trustline")
	}
	trustLine := entryData.TrustLine
	headroom := new(big.Int).Sub(big.NewInt(int64(trustLine.Limit)), big.NewInt(int64(trustLine.Balance)))
	return &TrustlineStatus{
		Exists:        true,
		Authorized:    xdr.TrustLineFlags(trustLine.Flags).IsAuthorized(),
		LimitHeadroom: headroom,
	}, nil
}

// CheckTrustline is a convenience method calling the package-level CheckTrustline with this
// Client's own rpc connection.
func (c *Client) CheckTrustline(ctx context.Context, account pldtypes.ChainAddress, asset txnbuild.Asset) (*TrustlineStatus, error) {
	return CheckTrustline(ctx, c.rpc, account, asset)
}
