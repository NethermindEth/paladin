/*
 * Copyright © 2026 Kaleido, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotoStellarOnlyABIDispatchable guards against a real bug found live (chapter 18's
// institutional repo demo): deposit/withdraw have a working Go handler
// (domains/noto/internal/noto/handler_deposit.go/handler_withdraw.go) but, before
// NotoStellarOnlyABI existed, were unreachable through the real RPC surface at all -
// noto.go's validateTransactionCommon requires the submitted function to be found via
// NotoABIFunctionsBySolSignature (an exact-signature lookup) or, failing that, a name lookup -
// and neither ever found deposit/withdraw, since they have no EVM/Solidity interface entry.
func TestNotoStellarOnlyABIDispatchable(t *testing.T) {
	depositFn := NotoStellarOnlyABI.Functions()["deposit"]
	require.NotNil(t, depositFn)
	withdrawFn := NotoStellarOnlyABI.Functions()["withdraw"]
	require.NotNil(t, withdrawFn)

	depositSig := depositFn.SolString()
	withdrawSig := withdrawFn.SolString()

	// The exact-signature map validateTransactionCommon checks first must contain both -
	// otherwise a real call falls through to the (also now fixed) name-only path and hits
	// MsgUnexpectedFunctionSignature instead of dispatching cleanly.
	assert.Same(t, depositFn, NotoABIFunctionsBySolSignature[depositSig])
	assert.Same(t, withdrawFn, NotoABIFunctionsBySolSignature[withdrawSig])
}
