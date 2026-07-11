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
	"encoding/hex"
	"time"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/config/pkg/confutil"
	"github.com/LFDT-Paladin/paladin/core/internal/msgs"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/core/pkg/ethclient"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"golang.org/x/crypto/sha3"
)

func calculateTransactionHash(rawTxnData []byte) *pldtypes.Bytes32 {
	if rawTxnData == nil {
		return nil
	}
	msgHash := sha3.NewLegacyKeccak256()
	msgHash.Write(rawTxnData)
	hashBytes := pldtypes.MustParseBytes32(hex.EncodeToString(msgHash.Sum(nil)))
	return &hashBytes
}

// submitTX is the chain-neutral submission wrapper: it owns retry/cancellation/timing/metrics
// orchestration (chapter 11's "~80% chain-neutral" observation), delegating the actual submit-and-classify
// step to the ChainSubmitter (chain_submitter.go), which knows how to interpret chain-specific errors.
func (it *inFlightTransactionStageController) submitTX(ctx context.Context, ps *PreparedSubmission, signerNonce string, lastSubmitTime *pldtypes.Timestamp, cancelled func(context.Context) bool) (*pldtypes.Bytes32, *pldtypes.Timestamp, ethclient.ErrorReason, SubmissionOutcome, error) {
	var txHash *pldtypes.Bytes32
	sendStart := time.Now()
	if ps.TransactionHash == nil {
		return nil, nil, ethclient.ErrorReasonInvalidInputs, SubmissionOutcomeFailedRequiresRetry, i18n.NewError(ctx, msgs.MsgInvalidStateMissingTXHash)
	}
	calculatedTxHash := ps.TransactionHash
	log.L(ctx).Debugf("Sending raw transaction %s (lastSubmit=%s), Hash=%s", signerNonce, lastSubmitTime, calculatedTxHash)

	submissionTime := confutil.P(pldtypes.TimestampNow())
	var submissionErrorReason ethclient.ErrorReason
	var submissionOutcome SubmissionOutcome
	var submissionError error

	retryError := it.transactionSubmissionRetry.Do(ctx, func(attempt int) ( /*retry*/ bool, error) {
		if cancelled(ctx) {
			return false, nil
		}
		result, err := it.chainSubmitter.Submit(ctx, ps)
		submissionError = err
		txHash = nil
		if result != nil {
			txHash = result.TxHash
			submissionOutcome = result.Outcome
			submissionErrorReason = ethclient.ErrorReason(result.ErrorReason)
		}
		if submissionError == nil {
			it.thMetrics.RecordOperationMetrics(ctx, string(InFlightTxOperationTransactionSend), string(GenericStatusSuccess), time.Since(sendStart).Seconds())
			log.L(ctx).Debugf("Submitted %s successfully with hash=%s", signerNonce, txHash)
			log.L(ctx).Infof("Transaction %s submitted. Hash: %s", signerNonce, calculatedTxHash)
			return false, nil
		}
		it.thMetrics.RecordOperationMetrics(ctx, string(InFlightTxOperationTransactionSend), string(GenericStatusFail), time.Since(sendStart).Seconds())
		if result != nil && result.Retry {
			return true, submissionError
		}
		return false, nil
	})

	if retryError != nil {
		return nil, submissionTime, submissionErrorReason, SubmissionOutcomeFailedRequiresRetry, retryError
	}

	return txHash, submissionTime, submissionErrorReason, submissionOutcome, submissionError
}

// restoreTX is the chain-neutral restore-preamble wrapper (chapter 12 §12.2, Stellar only): it owns
// submission and confirmation-polling orchestration, delegating build/sign to
// ChainSubmitter.PrepareRestore and submit/classify to the already-implemented ChainSubmitter.Submit
// (the same split submitTX uses for the real transaction). Confirmation is polled directly via
// baseledger.Client.GetTransactionResult rather than through the block-indexer confirmation-matching
// path, since GetTransactionResult is already a direct hash lookup for Stellar with no indexer
// dependency (see baseledger/stellar.Client's doc comment).
func (it *inFlightTransactionStageController) restoreTX(ctx context.Context, ptx *DBPublicTxn, soroban *baseledger.SorobanResources, cancelled func(context.Context) bool) (*pldtypes.Bytes32, error) {
	prepared, err := it.chainSubmitter.PrepareRestore(ctx, ptx, soroban)
	if err != nil {
		return nil, err
	}
	result, err := it.chainSubmitter.Submit(ctx, prepared)
	if err != nil {
		return nil, err
	}
	if result.TxHash == nil {
		return nil, i18n.NewError(ctx, msgs.MsgInvalidStateMissingTXHash)
	}
	txHash := *result.TxHash

	var confirmed bool
	retryErr := it.restoreConfirmationRetry.Do(ctx, func(attempt int) ( /*retry*/ bool, error) {
		if cancelled(ctx) {
			return false, nil
		}
		txResult, resErr := it.baseLedger.GetTransactionResult(ctx, txHash)
		if resErr != nil {
			// not found yet (or a transient RPC error) - keep polling until the retry budget is spent
			return true, resErr
		}
		if !txResult.Success {
			return false, i18n.NewError(ctx, msgs.MsgPublicTxMgrRestoreTransactionFailed, txHash)
		}
		confirmed = true
		return false, nil
	})
	if retryErr != nil {
		return &txHash, retryErr
	}
	if !confirmed {
		return &txHash, i18n.NewError(ctx, msgs.MsgPublicTxMgrRestoreTransactionTimedOut, txHash)
	}
	return &txHash, nil
}
