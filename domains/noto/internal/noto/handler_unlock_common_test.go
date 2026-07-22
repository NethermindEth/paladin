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

package noto

import (
	"testing"

	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckAllowedQualifiesAgainstTransactionFromNode is a regression test for a real bug found
// live in the chapter 18 institutional-repo demo: prepareUnlock is submitted by the lock owner
// (bankA@node2), but endorsed on the notary's own node (node1) in basic notary mode - checkAllowed
// used to qualify the bare "from" param against *this* node's own name (the node Endorse() happens
// to run on), which is node1 here, producing "bankA@node1" - a permanent mismatch against
// tx.Transaction.From ("bankA@node2") that made every retry fail identically forever (a silent
// infinite assemble/endorse loop, not a clean rejection). checkAllowed must qualify "from" against
// tx.Transaction.From's own node instead, since "from" names the same party.
func TestCheckAllowedQualifiesAgainstTransactionFromNode(t *testing.T) {
	h := &lockCommon{noto: &Noto{}}
	basicConfig := &types.NotoParsedConfig{NotaryMode: types.NotaryModeBasic.Enum()}

	t.Run("bare from resolves against tx.Transaction.From's own node, not the local node", func(t *testing.T) {
		tx := &types.ParsedTransaction{
			Transaction:  &prototk.TransactionSpecification{From: "bankA@node2"},
			DomainConfig: basicConfig,
		}
		err := h.checkAllowed(t.Context(), tx, "bankA")
		require.NoError(t, err)
	})

	t.Run("already-qualified from matching a different node is rejected", func(t *testing.T) {
		tx := &types.ParsedTransaction{
			Transaction:  &prototk.TransactionSpecification{From: "bankA@node2"},
			DomainConfig: basicConfig,
		}
		err := h.checkAllowed(t.Context(), tx, "bankA@node1")
		assert.ErrorContains(t, err, "PD200031")
		assert.ErrorContains(t, err, "expected=bankA@node2")
		assert.ErrorContains(t, err, "actual=bankA@node1")
	})

	t.Run("different identity entirely is rejected", func(t *testing.T) {
		tx := &types.ParsedTransaction{
			Transaction:  &prototk.TransactionSpecification{From: "bankA@node2"},
			DomainConfig: basicConfig,
		}
		err := h.checkAllowed(t.Context(), tx, "bankB")
		assert.ErrorContains(t, err, "PD200031")
	})

	t.Run("hooks notary mode skips the check entirely", func(t *testing.T) {
		tx := &types.ParsedTransaction{
			Transaction:  &prototk.TransactionSpecification{From: "bankA@node2"},
			DomainConfig: &types.NotoParsedConfig{NotaryMode: types.NotaryModeHooks.Enum()},
		}
		err := h.checkAllowed(t.Context(), tx, "someoneElse")
		require.NoError(t, err)
	})
}
