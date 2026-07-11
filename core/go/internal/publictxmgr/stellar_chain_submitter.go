// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package publictxmgr

import (
	"context"
	"errors"
	"fmt"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/core/internal/msgs"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	baseledgerstellar "github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// stellarChainSubmitter is the Stellar/Soroban implementation of ChainSubmitter (chapter 12
// foundational slice, §12.1/§12.2, plus §12.3's classic operations). Like evmChainSubmitter, it
// wraps the owning pubTxManager to reuse its already-constructed baseLedger client and key
// manager.
//
// Deliberately out of scope for this slice (see chapter 12's "Implementation status" callout):
// channel-account pooling (one signing identity = one source account today), fee-bump
// transactions, and the restore-preamble submission stage (see ActionOnStale below).
type stellarChainSubmitter struct {
	ptm *pubTxManager
}

func newStellarChainSubmitter(ptm *pubTxManager) ChainSubmitter {
	return &stellarChainSubmitter{ptm: ptm}
}

func (s *stellarChainSubmitter) AssignOrderingKey(ctx context.Context, from pldtypes.ChainAddress) (uint64, error) {
	info, err := s.ptm.baseLedger.GetAccountInfo(ctx, from)
	if err != nil {
		return 0, err
	}
	if info.OrderingKey == nil {
		return 0, i18n.NewError(ctx, msgs.MsgInvalidStateMissingTXHash)
	}
	return info.OrderingKey.Uint64(), nil
}

// buildStellarTx builds the unsigned transaction envelope for ptx. ptx.Nonce is used directly as
// the transaction's sequence number (the same "assigned nonce is the value to use, not a value to
// increment from" convention buildEthTX follows) by seeding SimpleAccount one below it and letting
// IncrementSequenceNum do the +1 arithmetic, rather than hand-computing it here and in
// AssignOrderingKey.
//
// Two payload kinds are supported (chapter 12 §12.3): a single Soroban InvokeHostFunction
// (pldapi.PublicTxPayloadKindXDRInvokeContractArgs, the default when ptx.PayloadKind is unset -
// every Stellar public tx before classic-ops support implicitly used this kind) or a plain XDR
// array of classic operations (pldapi.PublicTxPayloadKindXDRClassicOps), decoded via
// baseledgerstellar.DecodeClassicOperations - the same codec baseledger/stellar.Client.
// buildTransaction uses, exported specifically so this isn't a second, independent implementation.
func buildStellarTx(ptx *DBPublicTxn, resourceEstimate *baseledger.ResourceEstimate) (*txnbuild.Transaction, error) {
	if ptx.Nonce == nil {
		return nil, fmt.Errorf("a sequence number (nonce) is required to build a stellar transaction")
	}
	fromAddr := ptx.From.String()
	payloadKind := ptx.PayloadKind.V()
	if payloadKind == "" {
		payloadKind = pldapi.PublicTxPayloadKindXDRInvokeContractArgs
	}

	var ops []txnbuild.Operation
	switch payloadKind {
	case pldapi.PublicTxPayloadKindXDRInvokeContractArgs:
		var hostFunction xdr.HostFunction
		if err := hostFunction.UnmarshalBinary(ptx.Data); err != nil {
			return nil, fmt.Errorf("invalid host function payload: %w", err)
		}
		op := &txnbuild.InvokeHostFunction{
			HostFunction:  hostFunction,
			SourceAccount: fromAddr,
		}
		if resourceEstimate != nil && resourceEstimate.Soroban != nil {
			soroban := resourceEstimate.Soroban
			if len(soroban.TransactionDataXDR) > 0 {
				var sorobanData xdr.SorobanTransactionData
				if err := sorobanData.UnmarshalBinary(soroban.TransactionDataXDR); err != nil {
					return nil, fmt.Errorf("invalid soroban transaction data: %w", err)
				}
				op.Ext = xdr.TransactionExt{V: 1, SorobanData: &sorobanData}
			}
			if len(soroban.AuthEntriesXDR) > 0 {
				auth := make([]xdr.SorobanAuthorizationEntry, len(soroban.AuthEntriesXDR))
				for i, raw := range soroban.AuthEntriesXDR {
					if err := auth[i].UnmarshalBinary(raw); err != nil {
						return nil, fmt.Errorf("invalid soroban auth entry %d: %w", i, err)
					}
				}
				op.Auth = auth
			}
		}
		ops = []txnbuild.Operation{op}
	case pldapi.PublicTxPayloadKindXDRClassicOps:
		classicOps, err := baseledgerstellar.DecodeClassicOperations(ptx.Data)
		if err != nil {
			return nil, err
		}
		ops = classicOps
	default:
		return nil, fmt.Errorf("unsupported stellar payload kind %q", payloadKind)
	}

	account := txnbuild.NewSimpleAccount(fromAddr, int64(*ptx.Nonce)-1) //nolint:gosec // sequence numbers are always positive
	return txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &account,
		IncrementSequenceNum: true,
		Operations:           ops,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
	})
}

func (s *stellarChainSubmitter) PrepareSubmission(ctx context.Context, ptx *DBPublicTxn, resourceEstimate *baseledger.ResourceEstimate) (*PreparedSubmission, error) {
	tx, err := buildStellarTx(ptx, resourceEstimate)
	if err != nil {
		return nil, err
	}
	networkPassphrase := s.ptm.baseLedger.ChainInfo().NetworkID
	hash, err := tx.Hash(networkPassphrase)
	if err != nil {
		return nil, err
	}

	// Signing goes through the KeyManager - never locally. keypair.ParseAddress only needs the
	// public StrKey to compute the signature hint; the private key never enters this process.
	resolvedKey, err := s.ptm.keymgr.ReverseKeyLookup(ctx, s.ptm.p.NOTX(), algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, ptx.From.String())
	if err != nil {
		log.L(ctx).Errorf("signing failed to resolve key %s for signing: %s", ptx.From, err)
		return nil, err
	}
	signature, err := s.ptm.keymgr.Sign(ctx, resolvedKey, signpayloads.OPAQUE_TO_EDDSA, pldtypes.HexBytes(hash[:]))
	if err != nil {
		log.L(ctx).Errorf("signing failed with keyHandle %s (addr=%s): %s", resolvedKey.KeyHandle, resolvedKey.Verifier.Verifier, err)
		return nil, err
	}
	fromAddr, err := keypair.ParseAddress(ptx.From.String())
	if err != nil {
		return nil, err
	}
	tx, err = tx.AddSignatureDecorated(xdr.NewDecoratedSignature(signature, fromAddr.Hint()))
	if err != nil {
		return nil, err
	}
	rawTransaction, err := tx.MarshalBinary()
	if err != nil {
		return nil, err
	}
	txHash := pldtypes.Bytes32(hash)
	return &PreparedSubmission{
		PublicTxnID:     ptx.PublicTxnID,
		RawTransaction:  rawTransaction,
		TransactionHash: &txHash,
	}, nil
}

func (s *stellarChainSubmitter) Submit(ctx context.Context, ps *PreparedSubmission) (*SubmitResult, error) {
	txHash, err := s.ptm.baseLedger.Submit(ctx, baseledger.SignedChainTx(ps.RawTransaction))
	if err == nil {
		hash := txHash
		return &SubmitResult{TxHash: &hash, Outcome: SubmissionOutcomeSubmittedNew}, nil
	}

	var resultHash *pldtypes.Bytes32
	if ps.TransactionHash != nil {
		resultHash = ps.TransactionHash
	}

	var rejected *baseledgerstellar.SubmissionRejectedError
	if !errors.As(err, &rejected) {
		// A JSON-RPC/network-level error, not a structured rejection from stellar-core - the
		// direct analogue of ethclient.MapError's own default (unrecognized error) branch.
		log.L(ctx).Errorf("Submission error for transaction with hash %s (requires retry): %s", resultHash, err)
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeFailedRequiresRetry, Retry: true}, err
	}

	var result xdr.TransactionResult
	if unmarshalErr := xdr.SafeUnmarshalBase64(rejected.ErrorResultXDR, &result); unmarshalErr != nil {
		log.L(ctx).Errorf("Submission rejected for transaction with hash %s, and the error result could not be decoded: %s", resultHash, unmarshalErr)
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeFailedRequiresRetry, Retry: true}, err
	}

	code := result.Result.Code
	switch code {
	case xdr.TransactionResultCodeTxBadSeq:
		// txBAD_SEQ means the submitted sequence number didn't match the source account's expected
		// value - the closest Stellar analogue to EVM's "nonce too low", though (unlike EVM) it
		// doesn't distinguish "already used" from "too high": there is no mempool nonce-gap
		// recovery on Stellar to wait out, so a rebuild (re-fetch sequence, re-sign) is the correct
		// unconditional response either way.
		log.L(ctx).Debugf("Sequence number mismatch for transaction with recorded transaction hash: %s", resultHash)
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeNonceTooLow}, nil
	case xdr.TransactionResultCodeTxInsufficientFee:
		log.L(ctx).Debugf("Transaction fee too low")
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeFailedRequiresRetry, ErrorReason: code.String()}, err
	case xdr.TransactionResultCodeTxTooLate:
		// The transaction's time bounds precondition has expired - a rebuild (fresh time bounds)
		// is required, same as EVM's underpriced/reverted-on-estimate cases.
		log.L(ctx).Debugf("Transaction time bounds expired")
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeFailedRequiresRetry, ErrorReason: code.String()}, err
	default:
		log.L(ctx).Errorf("Submission error for transaction with hash %s (requires retry): %s (%s)", resultHash, err, code)
		return &SubmitResult{TxHash: resultHash, Outcome: SubmissionOutcomeFailedRequiresRetry, ErrorReason: code.String(), Retry: true}, err
	}
}

// ActionOnStale always rebuilds (re-simulate + re-sign + re-submit with a fresh sequence number),
// mirroring evmChainSubmitter's own unconditional rebuild behavior. StaleActionSubmitRestoreThen
// is not reachable yet: knowing a restore is required needs the ResourceEstimate.Soroban.
// RequiresRestore flag from the *last* EstimateResources call, and that flag isn't persisted
// anywhere on DBPublicTxn today - only the chain-neutral ChainSubmitter interface and
// PrepareSubmission see it transiently. Persisting it (and implementing the restore-preamble
// submission stage itself) is follow-up work alongside channel-account pooling and fee-bump.
func (s *stellarChainSubmitter) ActionOnStale(_ context.Context, _ *DBPublicTxn) (StaleAction, error) {
	return StaleActionRebuild, nil
}
