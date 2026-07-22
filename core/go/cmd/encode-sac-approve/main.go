// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

// encode-sac-approve builds the raw XDR call-data payload for a SEP-41/SAC `approve(from,
// spender, amount, expiration_ledger)` invocation, so a Java (or any non-Go) caller can submit it
// as a public transaction via `ptx_sendTransaction` (Type: "public", no ABI, Data: this program's
// hex output) - the Stellar raw-data-passthrough path in
// core/go/internal/txmgr/transaction_submission.go. The approve call must be signed by the
// depositor's own Paladin-managed key (Paladin never exports raw keys, so the `stellar` CLI can't
// submit it directly) - this program only builds the payload, it never talks to the network.
package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func scValAddress(strkeyAddr string) (xdr.ScVal, error) {
	addr, err := scspec.AddressFromStrkey(strkeyAddr)
	if err != nil {
		return xdr.ScVal{}, err
	}
	return xdr.NewScVal(xdr.ScValTypeScvAddress, addr)
}

func scValI128(amount int64) (xdr.ScVal, error) {
	return xdr.NewScVal(xdr.ScValTypeScvI128, xdr.Int128Parts{Hi: 0, Lo: xdr.Uint64(amount)})
}

func scValU32(v uint32) (xdr.ScVal, error) {
	return xdr.NewScVal(xdr.ScValTypeScvU32, xdr.Uint32(v))
}

func run(sacAddress, fromStrkey, spenderStrkey string, amount int64, expirationLedger uint32) (string, error) {
	fromVal, err := scValAddress(fromStrkey)
	if err != nil {
		return "", fmt.Errorf("from %q: %w", fromStrkey, err)
	}
	spenderVal, err := scValAddress(spenderStrkey)
	if err != nil {
		return "", fmt.Errorf("spender %q: %w", spenderStrkey, err)
	}
	amountVal, err := scValI128(amount)
	if err != nil {
		return "", err
	}
	expirationVal, err := scValU32(expirationLedger)
	if err != nil {
		return "", err
	}

	// approve(from, spender, amount, expiration_ledger) - vendored soroban-sdk token.rs trait order.
	vec := xdr.ScVec{fromVal, spenderVal, amountVal, expirationVal}
	var argsXDR bytes.Buffer
	if _, err := xdr.Marshal(&argsXDR, vec); err != nil {
		return "", fmt.Errorf("marshaling approve args: %w", err)
	}

	payload, err := stellar.BuildInvokeHostFunctionXDR(sacAddress, "approve", argsXDR.Bytes())
	if err != nil {
		return "", fmt.Errorf("building host function XDR: %w", err)
	}
	return "0x" + hex.EncodeToString(payload), nil
}

func main() {
	if len(os.Args) != 6 {
		fmt.Fprintf(os.Stderr, "usage: %s <sacAddress> <fromStrkey> <spenderStrkey> <amount> <expirationLedger>\n", os.Args[0])
		os.Exit(2)
	}
	sacAddress, fromStrkey, spenderStrkey := os.Args[1], os.Args[2], os.Args[3]
	amount, err := strconv.ParseInt(os.Args[4], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid amount %q: %v\n", os.Args[4], err)
		os.Exit(2)
	}
	expirationLedger, err := strconv.ParseUint(os.Args[5], 10, 32)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid expirationLedger %q: %v\n", os.Args[5], err)
		os.Exit(2)
	}

	hexPayload, err := run(sacAddress, fromStrkey, spenderStrkey, amount, uint32(expirationLedger))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(hexPayload)
}
