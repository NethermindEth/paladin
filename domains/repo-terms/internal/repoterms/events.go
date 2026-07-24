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

	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
)

// HandleEventBatch confirms the RepoTermsV1 output state and completes the transaction whenever a
// `set_terms` event is observed on-chain - much simpler than domains/noto's own HandleEventBatch:
// no V0/V1 variant branching (repo-terms has no versioning at all), no nullifier bookkeeping, no
// merkle tree updates, exactly one event kind to dispatch on.
func (r *RepoTerms) HandleEventBatch(ctx context.Context, req *prototk.HandleEventBatchRequest) (*prototk.HandleEventBatchResponse, error) {
	var res prototk.HandleEventBatchResponse

	for _, ev := range req.Events {
		switch ev.Signature {
		case stellarSetTermsSelector:
			setTerms, err := decodeStellarSetTermsEvent(ctx, ev)
			if err != nil {
				log.L(ctx).Warnf("Ignoring malformed set_terms event in batch %s: %s", req.BatchId, err)
				continue
			}
			log.L(ctx).Infof("Processing 'set_terms' event in batch %s", req.BatchId)
			r.applySetTermsEvent(ev, setTerms, &res)
		default:
			log.L(ctx).Infof("Skipping '%s' event in batch %s", ev.Signature, req.BatchId)
		}
	}
	return &res, nil
}

// applySetTermsEvent marks the transaction complete and confirms the terms state as created -
// mirrors domains/noto/internal/noto/events.go's own applyLockCreatedEvent/recordTransactionInfo,
// simplified: no info states to record (repo-terms has no manifest/data-state concept at all).
func (r *RepoTerms) applySetTermsEvent(ev *prototk.OnChainEvent, setTerms *SetTermsEvent, res *prototk.HandleEventBatchResponse) {
	res.TransactionsComplete = append(res.TransactionsComplete, &prototk.CompletedTransaction{
		TransactionId: setTerms.TxId.String(),
		Location:      ev.Location,
	})
	res.ConfirmedStates = append(res.ConfirmedStates, &prototk.StateUpdate{
		Id:            setTerms.TermsStateId.String(),
		TransactionId: setTerms.TxId.String(),
	})
}
