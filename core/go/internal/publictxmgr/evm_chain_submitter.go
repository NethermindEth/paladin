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
	"time"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/core/internal/msgs"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/core/pkg/ethclient"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/hyperledger/firefly-signer/pkg/ethsigner"
	"github.com/hyperledger/firefly-signer/pkg/secp256k1"
)

// evmChainSubmitter is the EVM implementation of ChainSubmitter (chapter 11 §11.3). It wraps the
// owning pubTxManager to reuse its already-constructed baseLedger client, key manager, persistence,
// and metrics, rather than duplicating a parallel set of dependencies.
type evmChainSubmitter struct {
	ptm *pubTxManager
}

func newEVMChainSubmitter(ptm *pubTxManager) ChainSubmitter {
	return &evmChainSubmitter{ptm: ptm}
}

func (s *evmChainSubmitter) AssignOrderingKey(ctx context.Context, from pldtypes.ChainAddress) (uint64, error) {
	info, err := s.ptm.baseLedger.GetAccountInfo(ctx, from)
	if err != nil {
		return 0, err
	}
	if info.OrderingKey == nil {
		return 0, i18n.NewError(ctx, msgs.MsgInvalidStateMissingTXHash)
	}
	return info.OrderingKey.Uint64(), nil
}

func (s *evmChainSubmitter) PrepareSubmission(ctx context.Context, ptx *DBPublicTxn, resourceEstimate *baseledger.ResourceEstimate) (*PreparedSubmission, error) {
	var gasPricing *pldapi.PublicTxGasPricing
	if resourceEstimate != nil {
		gasPricing = resourceEstimate.GasPricing
	}
	if gasPricing == nil {
		gasPricing = &pldapi.PublicTxGasPricing{}
	}
	from, err := ptx.From.EthAddress()
	if err != nil {
		return nil, err
	}
	var to *pldtypes.EthAddress
	if ptx.To != nil {
		if to, err = ptx.To.EthAddress(); err != nil {
			return nil, err
		}
	}
	ethTx := buildEthTX(*from, ptx.Nonce, to, ptx.Data, &pldapi.PublicTxOptions{
		Gas:                (*pldtypes.HexUint64)(&ptx.Gas),
		Value:              ptx.Value,
		PublicTxGasPricing: *gasPricing,
	})
	signedMessage, txHash, err := s.signTx(ctx, *from, ethTx)
	if err != nil {
		return nil, err
	}
	return &PreparedSubmission{
		PublicTxnID:     ptx.PublicTxnID,
		RawTransaction:  signedMessage,
		TransactionHash: txHash,
		GasPricing:      gasPricing,
	}, nil
}

// signTx moved here from transaction_signing.go: it is EVM-specific (secp256k1, RLP/EIP-1559
// finalization) and belongs behind the ChainSubmitter seam, not in the chain-neutral stage controller.
func (s *evmChainSubmitter) signTx(ctx context.Context, from pldtypes.EthAddress, ethTx *ethsigner.Transaction) ([]byte, *pldtypes.Bytes32, error) {
	log.L(ctx).Debugf("signTx entry")
	signStart := time.Now()

	resolvedKey, err := s.ptm.keymgr.ReverseKeyLookup(ctx, s.ptm.p.NOTX(), algorithms.ECDSA_SECP256K1, verifiers.ETH_ADDRESS, from.String())
	if err != nil {
		log.L(ctx).Errorf("signing failed to resolve key %s for signing: %s", from.String(), err)
		s.ptm.thMetrics.RecordOperationMetrics(ctx, string(InFlightTxOperationSign), string(GenericStatusFail), time.Since(signStart).Seconds())
		return nil, nil, err
	}
	sigPayload := ethTx.SignaturePayloadEIP1559(s.ptm.baseLedger.ChainInfo().EVMChainID)
	sigPayloadHash := calculateTransactionHash(sigPayload.Bytes())
	var signatureRSV []byte
	signatureRSV, err = s.ptm.keymgr.Sign(ctx, resolvedKey, signpayloads.OPAQUE_TO_RSV, pldtypes.HexBytes(sigPayloadHash[:]))
	var sig *secp256k1.SignatureData
	if err == nil {
		sig, err = secp256k1.DecodeCompactRSV(ctx, signatureRSV)
	}
	var signedMessage []byte
	if err == nil {
		signedMessage, err = ethTx.FinalizeEIP1559WithSignature(sigPayload, sig)
	}
	if err != nil {
		log.L(ctx).Errorf("signing failed with keyHandle %s (addr=%s): %s", resolvedKey.KeyHandle, resolvedKey.Verifier.Verifier, err)
		s.ptm.thMetrics.RecordOperationMetrics(ctx, string(InFlightTxOperationSign), string(GenericStatusFail), time.Since(signStart).Seconds())
		return nil, nil, err
	}
	calculatedHash := calculateTransactionHash(signedMessage)
	log.L(ctx).Debugf("Calculated Hash %s of transaction %s:%d", calculatedHash, ethTx.From, ethTx.Nonce.Uint64())
	s.ptm.thMetrics.RecordOperationMetrics(ctx, string(InFlightTxOperationSign), string(GenericStatusSuccess), time.Since(signStart).Seconds())
	return signedMessage, calculatedHash, err
}

func (s *evmChainSubmitter) Submit(ctx context.Context, ps *PreparedSubmission) (*SubmitResult, error) {
	txHash, err := s.ptm.baseLedger.Submit(ctx, baseledger.SignedChainTx(ps.RawTransaction))
	if err == nil {
		if ps.TransactionHash != nil && txHash.String() != ps.TransactionHash.String() {
			// TODO: Investigate why under high concurrency load with Besu we are triggering this, and the returned hash is for
			//       a DIFFERENT NONCE that is submitted at an extremely close time.
			log.L(ctx).Warnf("Received response for transaction, but calculated transaction hash %s is different from the response %s.", ps.TransactionHash, txHash)
			// Matches original behavior: this is a retryable condition, not a terminal one - the
			// caller's bounded submission retry attempts again immediately (Retry: true).
			return &SubmitResult{Outcome: SubmissionOutcomeFailedRequiresRetry, Retry: true}, i18n.NewError(ctx, msgs.MsgSubmitFailedWrongHashReturned, ps.TransactionHash, txHash)
		}
		hash := txHash
		return &SubmitResult{TxHash: &hash, Outcome: SubmissionOutcomeSubmittedNew}, nil
	}

	var resultHash *pldtypes.Bytes32
	if ps.TransactionHash != nil {
		resultHash = ps.TransactionHash
	}
	errorReason := ethclient.MapError(err)
	switch errorReason {
	case ethclient.ErrorReasonTransactionUnderpriced:
		log.L(ctx).Debugf("Transaction underpriced")
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeFailedRequiresRetry, ErrorReason: string(errorReason)}, err
	case ethclient.ErrorReasonTransactionReverted:
		// transaction could be reverted due to gas limit estimate too low
		log.L(ctx).Debugf("Transaction reverted")
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeFailedRequiresRetry, ErrorReason: string(errorReason)}, err
	case ethclient.ErrorKnownTransaction:
		// check mined transaction also returns this error code; KnownTransaction means it's in the mempool
		log.L(ctx).Debugf("Transaction known with hash: %s (previous=%s)", resultHash, err)
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeAlreadyKnown}, nil
	case ethclient.ErrorReasonNonceTooLow:
		// NonceTooLow means a transaction with same nonce is already mined, this could mean:
		//   1. we have a nonce conflict
		//   2. our transaction is completed and we are waiting for the confirmation
		// otherwise, we revert back to track the old hash
		log.L(ctx).Debugf("Nonce too low for transaction with recorded transaction hash: %s", resultHash)
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeNonceTooLow}, nil
	default:
		log.L(ctx).Errorf("Submission error for transaction with hash %s (requires retry): %s", resultHash, err)
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeFailedRequiresRetry, ErrorReason: string(errorReason), Retry: true}, err
	}
}

// ActionOnStale reflects EVM's current, unconditional behavior: when the resubmit interval is
// exceeded, always re-price, re-sign, and re-submit with the most up-to-date gas price (see
// in_flight_transaction_stage_controller.go's startNewStage doc comment for the detailed rationale).
func (s *evmChainSubmitter) ActionOnStale(_ context.Context, _ *DBPublicTxn) (StaleAction, error) {
	return StaleActionRebuild, nil
}
