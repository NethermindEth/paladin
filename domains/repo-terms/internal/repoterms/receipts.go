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
	"context"
	"encoding/json"

	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
)

// repoTermsReceipt is the minimal receipt shape for a "setTerms" transaction - just the opaque
// output state ID (the real terms are never revealed on the public chain, or in this receipt -
// only the two banks' own private-state distribution ever carries the real values). Deliberately
// minimal, per the task's own guidance that receipt-building is the least design-sensitive part of
// this domain.
type repoTermsReceipt struct {
	TermsStateID string `json:"termsStateId,omitempty"`
}

// BuildReceipt reports the assembled RepoTermsV1 output state's own ID - mirrors
// domains/noto/internal/noto/receipts.go's own BuildReceipt in spirit, drastically simplified (no
// lock-info/transfer/manifest bookkeeping of any kind).
func (r *RepoTerms) BuildReceipt(ctx context.Context, req *prototk.BuildReceiptRequest) (*prototk.BuildReceiptResponse, error) {
	receipt := repoTermsReceipt{}
	for _, state := range req.OutputStates {
		if r.repoTermsSchema != nil && state.SchemaId == r.repoTermsSchema.Id {
			receipt.TermsStateID = state.Id
			break
		}
	}

	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	return &prototk.BuildReceiptResponse{
		ReceiptJson: string(receiptJSON),
	}, nil
}
