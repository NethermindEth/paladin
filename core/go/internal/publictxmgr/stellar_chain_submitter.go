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
// foundational slice, §12.1/§12.2, plus §12.3's classic operations and §12.2's restore-preamble
// stage). Like evmChainSubmitter, it wraps the owning pubTxManager to reuse its already-constructed
// baseLedger client and key manager.
//
// Deliberately out of scope for this slice (see chapter 12's "Implementation status" callout):
// channel-account pooling (one signing identity = one source account today - see PrepareRestore's
// doc comment on the nonce-bump implication) and fee-bump transactions.
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

// signAndSerializeStellarTx signs tx via the KeyManager (never locally - keypair.ParseAddress only
// needs the public StrKey to compute the signature hint) and returns the wire-ready raw bytes and
// the network-ID-qualified signature hash. Shared by PrepareSubmission and PrepareRestore - both
// build a txnbuild.Transaction sourced from the same account and need identical signing.
func (s *stellarChainSubmitter) signAndSerializeStellarTx(ctx context.Context, from pldtypes.ChainAddress, tx *txnbuild.Transaction) ([]byte, pldtypes.Bytes32, error) {
	networkPassphrase := s.ptm.baseLedger.ChainInfo().NetworkID
	hash, err := tx.Hash(networkPassphrase)
	if err != nil {
		return nil, pldtypes.Bytes32{}, err
	}

	resolvedKey, err := s.ptm.keymgr.ReverseKeyLookup(ctx, s.ptm.p.NOTX(), algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, from.String())
	if err != nil {
		log.L(ctx).Errorf("signing failed to resolve key %s for signing: %s", from, err)
		return nil, pldtypes.Bytes32{}, err
	}
	signature, err := s.ptm.keymgr.Sign(ctx, resolvedKey, signpayloads.OPAQUE_TO_EDDSA, pldtypes.HexBytes(hash[:]))
	if err != nil {
		log.L(ctx).Errorf("signing failed with keyHandle %s (addr=%s): %s", resolvedKey.KeyHandle, resolvedKey.Verifier.Verifier, err)
		return nil, pldtypes.Bytes32{}, err
	}
	fromAddr, err := keypair.ParseAddress(from.String())
	if err != nil {
		return nil, pldtypes.Bytes32{}, err
	}
	tx, err = tx.AddSignatureDecorated(xdr.NewDecoratedSignature(signature, fromAddr.Hint()))
	if err != nil {
		return nil, pldtypes.Bytes32{}, err
	}
	rawTransaction, err := tx.MarshalBinary()
	if err != nil {
		return nil, pldtypes.Bytes32{}, err
	}
	return rawTransaction, pldtypes.Bytes32(hash), nil
}

// PrepareSubmission re-simulates Soroban invocations fresh on every call (not just once at
// transaction creation): the footprint, resource fee, and auth-entry data simulateTransaction
// returns are only valid against current ledger state, so reusing a stale simulation across
// retries would either fail on submission or silently omit an entry that's since been evicted
// (chapter 12 §12.2's canonical invocation pipeline). If the fresh simulation reports a needed
// entry is archived, this returns RequiresRestore=true instead of building the real transaction -
// the caller (the orchestrator's signing stage) is expected to route to the restore-preamble stage
// and call PrepareRestore instead. Classic operations (no simulation - see classic_ops.go) skip
// this entirely and use the caller-supplied resourceEstimate as before.
func (s *stellarChainSubmitter) PrepareSubmission(ctx context.Context, ptx *DBPublicTxn, resourceEstimate *baseledger.ResourceEstimate) (*PreparedSubmission, error) {
	payloadKind := ptx.PayloadKind.V()
	if payloadKind == "" {
		payloadKind = pldapi.PublicTxPayloadKindXDRInvokeContractArgs
	}
	if payloadKind != pldapi.PublicTxPayloadKindXDRClassicOps {
		var err error
		resourceEstimate, err = s.ptm.baseLedger.EstimateResources(ctx, &baseledger.UnsignedChainTx{
			From:        ptx.From,
			PayloadKind: baseledger.PayloadEncoding(payloadKind),
			Payload:     ptx.Data,
		})
		if err != nil {
			return nil, err
		}
		if resourceEstimate.Soroban != nil && resourceEstimate.Soroban.RequiresRestore {
			return &PreparedSubmission{
				PublicTxnID:     ptx.PublicTxnID,
				RequiresRestore: true,
				RestoreSoroban:  resourceEstimate.Soroban,
			}, nil
		}
	}

	tx, err := buildStellarTx(ptx, resourceEstimate)
	if err != nil {
		return nil, err
	}
	rawTransaction, txHash, err := s.signAndSerializeStellarTx(ctx, ptx.From, tx)
	if err != nil {
		return nil, err
	}
	return &PreparedSubmission{
		PublicTxnID:     ptx.PublicTxnID,
		RawTransaction:  rawTransaction,
		TransactionHash: &txHash,
	}, nil
}

// PrepareRestore builds and signs a standalone RestoreFootprintOp transaction from the footprint
// simulateTransaction reported as archived (soroban.RestorePreambleTransactionDataXDR), sourced
// from the same account and consuming the sequence number ptx.Nonce reserved for the real
// transaction (chapter 12 §12.2's restore-preamble stage). The caller is responsible for bumping
// the real transaction's persisted nonce by one once this restore transaction confirms - safe
// under today's one-in-flight-transaction-per-account model (channel-account pooling, which
// restores true per-account parallelism, is chapter 12 §12.2's separate, not-yet-built phase).
func (s *stellarChainSubmitter) PrepareRestore(ctx context.Context, ptx *DBPublicTxn, soroban *baseledger.SorobanResources) (*PreparedSubmission, error) {
	if ptx.Nonce == nil {
		return nil, fmt.Errorf("a sequence number (nonce) is required to build a restore transaction")
	}
	if soroban == nil || len(soroban.RestorePreambleTransactionDataXDR) == 0 {
		return nil, fmt.Errorf("no restore preamble available to build a restore transaction")
	}
	var sorobanData xdr.SorobanTransactionData
	if err := sorobanData.UnmarshalBinary(soroban.RestorePreambleTransactionDataXDR); err != nil {
		return nil, fmt.Errorf("invalid restore preamble transaction data: %w", err)
	}

	fromAddr := ptx.From.String()
	account := txnbuild.NewSimpleAccount(fromAddr, int64(*ptx.Nonce)-1) //nolint:gosec // sequence numbers are always positive
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &account,
		IncrementSequenceNum: true,
		Operations: []txnbuild.Operation{&txnbuild.RestoreFootprint{
			SourceAccount: fromAddr,
			Ext:           xdr.TransactionExt{V: 1, SorobanData: &sorobanData},
		}},
		BaseFee:       txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
	})
	if err != nil {
		return nil, err
	}
	rawTransaction, txHash, err := s.signAndSerializeStellarTx(ctx, ptx.From, tx)
	if err != nil {
		return nil, err
	}
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
// mirroring evmChainSubmitter's own unconditional rebuild behavior. StaleActionSubmitRestoreThen is
// deliberately not returned here: a rebuild routes back through PrepareSubmission, which always
// re-simulates fresh and will itself detect and react to RequiresRestore (see PrepareSubmission's
// doc comment) - there's no need for ActionOnStale to pre-empt that with a stale simulation of its
// own.
func (s *stellarChainSubmitter) ActionOnStale(_ context.Context, _ *DBPublicTxn) (StaleAction, error) {
	return StaleActionRebuild, nil
}
