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

package repoterms

import (
	"encoding/json"
	"testing"

	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildReceipt(t *testing.T) {
	r := &RepoTerms{repoTermsSchema: testSchema("repoTerms")}
	ctx := t.Context()

	res, err := r.BuildReceipt(ctx, &prototk.BuildReceiptRequest{
		OutputStates: []*prototk.EndorsableState{
			{Id: "0xabc123", SchemaId: r.repoTermsSchema.Id, StateDataJson: `{}`},
		},
	})
	require.NoError(t, err)

	var receipt repoTermsReceipt
	require.NoError(t, json.Unmarshal([]byte(res.ReceiptJson), &receipt))
	assert.Equal(t, "0xabc123", receipt.TermsStateID)
}

func TestBuildReceipt_NoMatchingSchema(t *testing.T) {
	r := &RepoTerms{repoTermsSchema: testSchema("repoTerms")}
	ctx := t.Context()

	res, err := r.BuildReceipt(ctx, &prototk.BuildReceiptRequest{
		OutputStates: []*prototk.EndorsableState{
			{Id: "0xabc123", SchemaId: "someOtherSchema", StateDataJson: `{}`},
		},
	})
	require.NoError(t, err)

	var receipt repoTermsReceipt
	require.NoError(t, json.Unmarshal([]byte(res.ReceiptJson), &receipt))
	assert.Empty(t, receipt.TermsStateID)
}
