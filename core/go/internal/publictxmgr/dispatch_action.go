/*
 * Copyright © 2024 Kaleido, Inc.
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

package publictxmgr

import (
	"context"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
)

type AsyncRequestType int

const (
	ActionSuspend AsyncRequestType = iota
	ActionResume
)

func (ptm *pubTxManager) persistSuspendedFlag(ctx context.Context, from pldtypes.ChainAddress, nonce uint64, suspended bool) error {
	log.L(ctx).Infof("Setting suspend status to '%t' for transaction %s:%d", suspended, from, nonce)
	return ptm.p.DB().
		WithContext(ctx).
		Table("public_txns").
		Where(`"from" = ?`, from).
		Where("nonce = ?", nonce).
		Where("dispatcher = ? OR dispatcher = ''", ptm.nodeName).
		UpdateColumn("suspended", suspended).
		Error
}

// dispatchCompletedAction notifies whichever in-flight orchestrator currently holds this
// transaction that it has confirmed on-chain. A chain confirmation only tells us the on-chain
// envelope's own source account - for Stellar that's the channel account (see
// stellar_chain_submitter.go's ptx.ChannelAccount doc comment), which is a different address than
// the business/signing address ptm.inFlightOrchestrators is keyed by, so we can't route this by
// address the way ActionSuspend/ActionResume do below. We route by PublicTxnID (the pub_txn_id
// primary key) instead, checking each in-flight orchestrator rather than keying off an address we
// can't correctly resolve here.
func (ptm *pubTxManager) dispatchCompletedAction(ctx context.Context, pubTxnID uint64) {
	ptm.inFlightOrchestratorMux.Lock()
	orchestrators := make([]*orchestrator, 0, len(ptm.inFlightOrchestrators))
	for _, oc := range ptm.inFlightOrchestrators {
		orchestrators = append(orchestrators, oc)
	}
	ptm.inFlightOrchestratorMux.Unlock()

	for _, oc := range orchestrators {
		if oc.dispatchCompletedAction(ctx, pubTxnID) {
			return
		}
	}
}

func (oc *orchestrator) dispatchCompletedAction(ctx context.Context, pubTxnID uint64) bool {
	oc.inFlightTxsMux.Lock()
	defer oc.inFlightTxsMux.Unlock()
	for _, inflight := range oc.inFlightTxs {
		if inflight.stateManager.GetPubTxnID() == pubTxnID {
			log.L(ctx).Infof("Dispatching 'completed' action to active orchestrator for %s (pubTxnID=%d)", oc.signingAddress, pubTxnID)
			_, _ = inflight.NotifyStatusUpdate(ctx, InFlightStatusConfirmReceived)
			oc.MarkInFlightTxStale()
			return true
		}
	}
	return false
}

// TODO: this code needs to stop using from and nonce as the way of identifying a transaction. It didn't get edited
// with the move to delayed nonce assignment, where pubTXID became the primary key for a public transaction instead
// of from and nonce as a composite primary key. This isn't a problem for suspend/resume since those are user-driven
// requests that already know the correct signing/business address to target - see dispatchCompletedAction above
// for why a chain-confirmation notification can't rely on the same from-address routing.
func (ptm *pubTxManager) dispatchAction(ctx context.Context, from pldtypes.ChainAddress, nonce uint64, action AsyncRequestType) error {
	ptm.inFlightOrchestratorMux.Lock()
	defer ptm.inFlightOrchestratorMux.Unlock()
	inFlightOrchestrator, orchestratorInFlight := ptm.inFlightOrchestrators[from]
	switch action {
	case ActionSuspend, ActionResume:
		suspended := false
		if action == ActionSuspend {
			suspended = true
		}
		if !orchestratorInFlight {
			// no in-flight orchestrator for the signing address, it's OK to update the DB directly
			log.L(ctx).Infof("No orchestrator in-flight for %s so persisting suspended=%t flag", from, suspended)
			return ptm.persistSuspendedFlag(ctx, from, nonce, suspended)
		}
		// has to be done in the context of the orchestrator
		log.L(ctx).Infof("Dispatching suspended=%t action to active orchestrator for %s", suspended, from)
		return inFlightOrchestrator.dispatchAction(ctx, nonce, action)
	}
	return nil
}

func (oc *orchestrator) dispatchAction(ctx context.Context, nonce uint64, action AsyncRequestType) (err error) {
	oc.inFlightTxsMux.Lock()
	defer oc.inFlightTxsMux.Unlock()
	var pending *inFlightTransactionStageController
	for _, inflight := range oc.inFlightTxs {
		if inflight.stateManager.GetNonce() == nonce {
			pending = inflight
			break
		}
	}
	if pending != nil {
		switch action {
		case ActionResume, ActionSuspend:
			// ActionResume...
			suspendedFlag := false
			newStatus := InFlightStatusPending
			// .. or ActionSuspend
			if action == ActionSuspend {
				suspendedFlag = true
				newStatus = InFlightStatusSuspending
			}
			_, _ = pending.NotifyStatusUpdate(ctx, newStatus)
			// Ok we've now got the lock that means we can write to the DB
			// No optimization of this write, as it's a user action from the side of normal processing
			err = oc.persistSuspendedFlag(ctx, oc.signingAddress, nonce, suspendedFlag)
		}
		oc.MarkInFlightTxStale()
	}
	return err
}

func (ptm *pubTxManager) dispatchUpdate(update *transactionUpdate) {
	// updateMux must be locked by the called
	ptm.updates = append(ptm.updates, update)
	ptm.MarkInFlightOrchestratorsStale()
}

func (oc *orchestrator) dispatchUpdate(update *transactionUpdate) {
	oc.updateMux.Lock()
	defer oc.updateMux.Unlock()
	oc.updates = append(oc.updates, update)
	oc.MarkInFlightTxStale()
}
