// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package stellar

import (
	"context"
	"testing"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func trustLineLedgerEntryXDR(t *testing.T, accountID xdr.AccountId, asset xdr.TrustLineAsset, balance, limit int64, flags uint32) string {
	entry := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeTrustline,
		TrustLine: &xdr.TrustLineEntry{
			AccountId: accountID,
			Asset:     asset,
			Balance:   xdr.Int64(balance),
			Limit:     xdr.Int64(limit),
			Flags:     xdr.Uint32(flags),
		},
	}
	b64, err := xdr.MarshalBase64(entry)
	require.NoError(t, err)
	return b64
}

func TestCheckTrustlineExistsAndAuthorized(t *testing.T) {
	account := *pldtypes.MustParseChainAddress(keypair.MustRandom().Address())
	issuer := keypair.MustRandom().Address()
	asset := txnbuild.CreditAsset{Code: "USDX", Issuer: issuer}

	accountID, err := xdr.AddressToAccountId(account.String())
	require.NoError(t, err)
	trustLineAsset, err := asset.MustToTrustLineAsset().ToXDR()
	require.NoError(t, err)

	rpc := &fakeRPC{
		getLedgerEntries: func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
			require.Len(t, req.Keys, 1)
			return protocol.GetLedgerEntriesResponse{
				Entries: []protocol.LedgerEntryResult{
					{DataXDR: trustLineLedgerEntryXDR(t, accountID, trustLineAsset, 500, 1000, uint32(xdr.TrustLineFlagsAuthorizedFlag))},
				},
			}, nil
		},
	}

	status, err := CheckTrustline(context.Background(), rpc, account, asset)
	require.NoError(t, err)
	require.True(t, status.Exists)
	require.True(t, status.Authorized)
	require.Equal(t, int64(500), status.LimitHeadroom.Int64())
}

func TestCheckTrustlineUnauthorized(t *testing.T) {
	account := *pldtypes.MustParseChainAddress(keypair.MustRandom().Address())
	issuer := keypair.MustRandom().Address()
	asset := txnbuild.CreditAsset{Code: "USDX", Issuer: issuer}

	accountID, err := xdr.AddressToAccountId(account.String())
	require.NoError(t, err)
	trustLineAsset, err := asset.MustToTrustLineAsset().ToXDR()
	require.NoError(t, err)

	rpc := &fakeRPC{
		getLedgerEntries: func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
			return protocol.GetLedgerEntriesResponse{
				Entries: []protocol.LedgerEntryResult{
					{DataXDR: trustLineLedgerEntryXDR(t, accountID, trustLineAsset, 0, 1000, 0)},
				},
			}, nil
		},
	}

	status, err := CheckTrustline(context.Background(), rpc, account, asset)
	require.NoError(t, err)
	require.True(t, status.Exists)
	require.False(t, status.Authorized)
}

func TestCheckTrustlineMissing(t *testing.T) {
	account := *pldtypes.MustParseChainAddress(keypair.MustRandom().Address())
	asset := txnbuild.CreditAsset{Code: "USDX", Issuer: keypair.MustRandom().Address()}

	rpc := &fakeRPC{
		getLedgerEntries: func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
			return protocol.GetLedgerEntriesResponse{}, nil
		},
	}

	status, err := CheckTrustline(context.Background(), rpc, account, asset)
	require.NoError(t, err)
	require.False(t, status.Exists)
	require.Nil(t, status.LimitHeadroom)
}

func TestClientCheckTrustlineDelegates(t *testing.T) {
	account := *pldtypes.MustParseChainAddress(keypair.MustRandom().Address())
	asset := txnbuild.CreditAsset{Code: "USDX", Issuer: keypair.MustRandom().Address()}

	rpc := &fakeRPC{
		getLedgerEntries: func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
			return protocol.GetLedgerEntriesResponse{}, nil
		},
	}
	c := WrapClient(rpc, "", nil)

	status, err := c.CheckTrustline(context.Background(), account, asset)
	require.NoError(t, err)
	require.False(t, status.Exists)
}
