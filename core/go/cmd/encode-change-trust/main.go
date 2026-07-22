// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

// encode-change-trust builds the raw XDR_CLASSIC_OPS payload for a single ChangeTrust operation
// (core/go/pkg/baseledger/stellar.BuildChangeTrustPayload), so a Java (or any non-Go) caller can
// submit it as a public transaction via ptx_sendTransaction (Type: "public", PublicTxOptions:
// {payloadKind: "XDR_CLASSIC_OPS"}, Data: this program's hex output) - the classic-ops
// raw-passthrough path in core/go/internal/txmgr/transaction_submission.go. A trustline can only
// be created by its own holder, so this must be signed by the holder's own Paladin-managed key
// (Paladin never exports raw keys, so the `stellar` CLI can't submit it directly) - this program
// only builds the payload, it never talks to the network.
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/stellar/go-stellar-sdk/txnbuild"
)

func run(holderAccount, assetCode, assetIssuer string) (string, error) {
	asset := txnbuild.CreditAsset{Code: assetCode, Issuer: assetIssuer}
	payload, err := stellar.BuildChangeTrustPayload(holderAccount, asset, "")
	if err != nil {
		return "", fmt.Errorf("building change trust payload: %w", err)
	}
	return "0x" + hex.EncodeToString(payload), nil
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "usage: %s <holderAccount> <assetCode> <assetIssuer>\n", os.Args[0])
		os.Exit(2)
	}
	hexPayload, err := run(os.Args[1], os.Args[2], os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(hexPayload)
}
