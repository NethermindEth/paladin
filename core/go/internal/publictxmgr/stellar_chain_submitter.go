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
	"github.com/LFDT-Paladin/paladin/config/pkg/confutil"
	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/core/internal/msgs"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	baseledgerstellar "github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/retry"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// stellarChainSubmitter is the Stellar/Soroban implementation of ChainSubmitter (chapter 12
// backend implementation, §12.1/§12.2 including channel-account pooling, plus §12.3's classic
// operations and §12.2's restore-preamble stage). Like evmChainSubmitter, it wraps the owning
// pubTxManager to reuse its already-constructed baseLedger client and key manager.
//
// Deliberately out of scope for this slice (see chapter 12's "Implementation status" callout):
// fee-bump transactions.
type stellarChainSubmitter struct {
	ptm             *pubTxManager
	channelAccounts *pldconf.ChannelAccountsConfig
	// fundingConfirmationRetry bounds how long a single account-funding CreateAccountOp submission
	// is polled for confirmation before it's given up on as lost (see fundingRebuildRetry) - a
	// one-time cost per channel account's lifetime, so this isn't exposed as its own config knob
	// (unlike restoreConfirmationRetry, which is on the orchestrator's hot resubmit path).
	fundingConfirmationRetry *retry.Retry
	// fundingRebuildRetry bounds how many times ensureAccountFunded will rebuild (re-fetch the
	// funder's sequence number, re-sign, re-submit) and await confirmation again, after a prior
	// submission either failed outright or never confirmed within fundingConfirmationRetry's
	// budget. A submission can go unconfirmed for reasons beyond an outright rejection: stellar-rpc
	// only synchronously rejects a submission whose sequence is ALREADY stale against its current
	// ledger view: two concurrent submissions can each read the same "current sequence" before
	// either has been included in a ledger, both get accepted as pending, and whichever loses gets
	// silently dropped from the mempool once the winner advances the account's sequence - it never
	// surfaces as a rejection, only as a confirmation that never arrives (the same class of
	// "transaction... since 'lost' the transaction" case the orchestrator's own stale-transaction
	// handling documents, in_flight_transaction_stage_controller.go).
	fundingRebuildRetry *retry.Retry
}

func newStellarChainSubmitter(ptm *pubTxManager, channelAccounts *pldconf.ChannelAccountsConfig) ChainSubmitter {
	if channelAccounts == nil {
		channelAccounts = &pldconf.ChannelAccountsConfig{}
	}
	return &stellarChainSubmitter{
		ptm:             ptm,
		channelAccounts: channelAccounts,
		fundingConfirmationRetry: retry.NewRetryLimited(&pldconf.RetryConfigWithMax{
			RetryConfig: pldconf.RetryConfig{InitialDelay: confutil.P("1s"), MaxDelay: confutil.P("2s"), Factor: confutil.P(1.0)},
			MaxAttempts: confutil.P(5),
		}),
		fundingRebuildRetry: retry.NewRetryLimited(&pldconf.RetryConfigWithMax{
			RetryConfig: pldconf.RetryConfig{InitialDelay: confutil.P("100ms"), MaxDelay: confutil.P("1s"), Factor: confutil.P(1.5)},
			MaxAttempts: confutil.P(10),
		}),
	}
}

// AssignOrderingKeys resolves (deriving new keys if not already known), and if necessary bootstraps
// on-chain (creating and funding via CreateAccountOp from the configured funder identity), from's
// channel-account pool (chapter 12 §12.2) - one derived sub-key per pool member
// (m/…/<identifier>/channel/<i>), returning each member's current sequence number. The caller
// (allocateNonces) is responsible for distributing individual transactions across the returned
// slots; this always resolves and returns the full pool, not a single assignment.
func (s *stellarChainSubmitter) AssignOrderingKeys(ctx context.Context, from pldtypes.ChainAddress) ([]ChannelOrderingKey, error) {
	poolSize := confutil.IntMin(s.channelAccounts.PoolSize, 1, *pldconf.StellarClientDefaults.ChannelAccounts.PoolSize)

	identity, err := s.ptm.keymgr.ReverseKeyLookup(ctx, s.ptm.p.NOTX(), algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, from.String())
	if err != nil {
		return nil, err
	}
	identifiers := make([]string, poolSize)
	for i := range identifiers {
		identifiers[i] = fmt.Sprintf("%s.channel.%d", identity.Identifier, i)
	}
	channelKeys, err := s.ptm.keymgr.ResolveBatchNewDatabaseTX(ctx, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, identifiers)
	if err != nil {
		return nil, err
	}

	keys := make([]ChannelOrderingKey, len(channelKeys))
	for i, ck := range channelKeys {
		channelAccount, err := pldtypes.ParseChainAddress(ck.Verifier.Verifier)
		if err != nil {
			return nil, fmt.Errorf("invalid channel account address %q: %w", ck.Verifier.Verifier, err)
		}
		if err := s.ensureChannelAccountFunded(ctx, *channelAccount); err != nil {
			return nil, err
		}
		info, err := s.ptm.baseLedger.GetAccountInfo(ctx, *channelAccount)
		if err != nil {
			return nil, err
		}
		if info.OrderingKey == nil {
			return nil, i18n.NewError(ctx, msgs.MsgInvalidStateMissingTXHash)
		}
		keys[i] = ChannelOrderingKey{OrderingKey: info.OrderingKey.Uint64(), ChannelAccount: channelAccount}
	}
	return keys, nil
}

// ensureChannelAccountFunded is a no-op if channelAccount already exists on chain. Otherwise it
// submits (and waits for confirmation of) a CreateAccountOp funded from the configured funder
// identity - an explicit operational decision (chapter 12 §12.2): there is no automatic/faucet
// funding, so a missing funder configuration is a hard failure here, not a silent skip.
func (s *stellarChainSubmitter) ensureChannelAccountFunded(ctx context.Context, channelAccount pldtypes.ChainAddress) error {
	return s.ensureAccountFunded(ctx, channelAccount)
}

// EnsureFromAccountFunded ensures a transaction's own business signing identity (ptx.From - the
// notary/party identity a domain resolves, not one of AssignOrderingKeys' channel sub-keys) exists
// on chain, using the exact same funder/CreateAccountOp mechanism as ensureChannelAccountFunded.
// This identity is never the envelope's source account at submission time (buildStellarTx always
// sources from ptx.ChannelAccount), but ValidateTransaction's gas-estimation call still needs
// stellar-rpc's simulateTransaction to LoadAccount it as the simulated transaction's own source
// account, and that runs before AssignOrderingKeys/channel-account bootstrap ever happens for a
// brand new identity - so without this, the very first transaction from a not-yet-seen identity
// fails gas estimation with "failed to find ledger entry for account G...", even though channel
// accounts are (and always were) auto-funded correctly.
func (s *stellarChainSubmitter) EnsureFromAccountFunded(ctx context.Context, from pldtypes.ChainAddress) error {
	return s.ensureAccountFunded(ctx, from)
}

// ensureAccountFunded is a no-op if account already exists on chain. Otherwise it submits (and
// waits for confirmation of) a CreateAccountOp funded from the configured channel-account funder
// identity - shared by ensureChannelAccountFunded (channel sub-keys) and EnsureFromAccountFunded
// (a transaction's own business signing identity), since both need exactly the same bootstrap.
//
// Any GetAccountInfo error (not just a distinguishable "not found") is treated as "assume missing,
// attempt to create" - stellar-rpc has no typed not-found error surfaced through baseledger.Client
// today, and a genuine transient RPC error simply makes the subsequent CreateAccountOp submission
// fail too, which bubbles up and is retried on the orchestrator's next poll tick like any other
// AssignOrderingKeys failure.
func (s *stellarChainSubmitter) ensureAccountFunded(ctx context.Context, account pldtypes.ChainAddress) error {
	if _, err := s.ptm.baseLedger.GetAccountInfo(ctx, account); err == nil {
		return nil
	}
	funderIdentifier := confutil.StringNotEmpty(s.channelAccounts.Funder, "")
	if funderIdentifier == "" {
		return i18n.NewError(ctx, msgs.MsgPublicTxMgrChannelAccountFunderNotConfigured, account)
	}
	funder, err := s.ptm.keymgr.ResolveKeyNewDatabaseTX(ctx, funderIdentifier, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS)
	if err != nil {
		return fmt.Errorf("failed to resolve channel account funder %q: %w", funderIdentifier, err)
	}
	funderAddr, err := pldtypes.ParseChainAddress(funder.Verifier.Verifier)
	if err != nil {
		return fmt.Errorf("invalid channel account funder address %q: %w", funder.Verifier.Verifier, err)
	}

	startingBalance := confutil.StringNotEmpty(s.channelAccounts.StartingBalance, *pldconf.StellarClientDefaults.ChannelAccounts.StartingBalance)

	// Multiple channel-account pools can need bootstrapping from the SAME funder identity at the
	// same time (e.g. a deploy's own identity plus a fresh per-deploy nonce identity - see
	// AssignOrderingKeys' own doc comment) with no in-process coordination over the funder's
	// sequence number. Two concurrent submissions built from the same "current sequence" collide.
	// stellar-rpc only synchronously rejects (txBAD_SEQ, Submit's own SubmissionOutcomeNonceTooLow
	// translation) a submission whose sequence is ALREADY stale against its current ledger view -
	// if both submissions are built before either has been included in a ledger, both look valid at
	// submission time and are accepted as pending; the eventual loser is then silently dropped from
	// the mempool once the winner's inclusion advances the funder's sequence, never surfacing as a
	// rejection, only as a confirmation that never arrives. So rebuilding has to be driven by BOTH
	// an outright submission rejection AND a confirmation that doesn't show up in time - reproduced
	// by TestStellarEnsureAccountFundedDoesNotRetryOnBadSeq (rejection case) and
	// TestStellarEnsureAccountFundedRebuildsWhenConfirmationNeverArrives (silently-lost case).
	return s.fundingRebuildRetry.Do(ctx, func(attempt int) (bool, error) {
		if _, err := s.ptm.baseLedger.GetAccountInfo(ctx, account); err == nil {
			// Created either by an earlier attempt of this same loop, or by a concurrent bootstrap
			// of the same account, since we last checked.
			return false, nil
		}

		funderInfo, err := s.ptm.baseLedger.GetAccountInfo(ctx, *funderAddr)
		if err != nil {
			return true, fmt.Errorf("failed to look up channel account funder %s: %w", funderAddr, err)
		}
		if funderInfo.OrderingKey == nil {
			return false, i18n.NewError(ctx, msgs.MsgInvalidStateMissingTXHash)
		}
		funderAccount := txnbuild.NewSimpleAccount(funderAddr.String(), int64(funderInfo.OrderingKey.Uint64())-1) //nolint:gosec // sequence numbers are always positive
		tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount:        &funderAccount,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{&txnbuild.CreateAccount{
				SourceAccount: funderAddr.String(),
				Destination:   account.String(),
				Amount:        startingBalance,
			}},
			BaseFee:       txnbuild.MinBaseFee,
			Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
		})
		if err != nil {
			return false, err
		}
		rawTransaction, txHash, err := s.signAndSerializeStellarTx(ctx, *funderAddr, tx)
		if err != nil {
			return false, err
		}
		result, err := s.Submit(ctx, &PreparedSubmission{RawTransaction: rawTransaction, TransactionHash: &txHash})
		if err != nil {
			return true, fmt.Errorf("failed to submit account funding transaction for %s: %w", account, err)
		}
		if result.Outcome == SubmissionOutcomeNonceTooLow {
			return true, fmt.Errorf("account funding transaction for %s rejected: funder %s sequence number already used by a concurrent submission", account, funderAddr)
		}
		if result.TxHash == nil {
			return false, i18n.NewError(ctx, msgs.MsgInvalidStateMissingTXHash)
		}

		confirmErr := s.fundingConfirmationRetry.Do(ctx, func(attempt int) (bool, error) {
			txResult, err := s.ptm.baseLedger.GetTransactionResult(ctx, *result.TxHash)
			if err != nil {
				return true, err
			}
			if !txResult.Success {
				return false, i18n.NewError(ctx, msgs.MsgPublicTxMgrRestoreTransactionFailed, *result.TxHash)
			}
			return false, nil
		})
		if confirmErr != nil {
			// Retryable: rebuild with a freshly-read funder sequence rather than continuing to poll
			// a hash that may have been silently lost to a concurrent submission (see doc comment
			// above).
			return true, confirmErr
		}
		return false, nil
	})
}

// buildStellarTx builds the unsigned transaction envelope for ptx. ptx.Nonce is used directly as
// the transaction's sequence number (the same "assigned nonce is the value to use, not a value to
// increment from" convention buildEthTX follows) by seeding SimpleAccount one below it and letting
// IncrementSequenceNum do the +1 arithmetic, rather than hand-computing it here and in
// AssignOrderingKeys.
//
// The transaction's envelope (sequence number, inclusion fee, signature) is sourced from
// ptx.ChannelAccount when set (chapter 12 §12.2's channel-account pooling), NOT ptx.From - the
// business identity the InvokeHostFunction operation still names as its own SourceAccount, since
// that's what a contract's require_auth checks against. This is exactly the "third-party
// pre-signed auth entries" split chapter 12 §12.1 already established (transaction source ≠
// invocation authorizer) - channel-account pooling just makes it the default for every invocation,
// not only ones with a genuine third-party authorizer.
//
// Two payload kinds are supported (chapter 12 §12.3): a single Soroban InvokeHostFunction
// (pldapi.PublicTxPayloadKindXDRInvokeContractArgs, the default when ptx.PayloadKind is unset -
// every Stellar public tx before classic-ops support implicitly used this kind) or a plain XDR
// array of classic operations (pldapi.PublicTxPayloadKindXDRClassicOps), decoded via
// baseledgerstellar.DecodeClassicOperations - the same codec baseledger/stellar.Client.
// buildTransaction uses, exported specifically so this isn't a second, independent implementation.
// Classic operations carry their own explicit SourceAccount (see classic_ops.go's
// BuildChangeTrustPayload/BuildSetTrustLineFlagsPayload doc comments), so diverging the envelope's
// source from ptx.From doesn't affect which account a ChangeTrust/SetTrustLineFlags acts on.
func buildStellarTx(ptx *DBPublicTxn, resourceEstimate *baseledger.ResourceEstimate) (*txnbuild.Transaction, error) {
	if ptx.Nonce == nil {
		return nil, fmt.Errorf("a sequence number (nonce) is required to build a stellar transaction")
	}
	fromAddr := ptx.From.String()
	envelopeSourceAddr := fromAddr
	if ptx.ChannelAccount != nil {
		envelopeSourceAddr = ptx.ChannelAccount.String()
	}
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

	account := txnbuild.NewSimpleAccount(envelopeSourceAddr, int64(*ptx.Nonce)-1) //nolint:gosec // sequence numbers are always positive
	return txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &account,
		IncrementSequenceNum: true,
		Operations:           ops,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
	})
}

// envelopeSigningAddress returns the account whose key must sign ptx's transaction envelope -
// ptx.ChannelAccount when set (chapter 12 §12.2), else ptx.From.
func envelopeSigningAddress(ptx *DBPublicTxn) pldtypes.ChainAddress {
	if ptx.ChannelAccount != nil {
		return *ptx.ChannelAccount
	}
	return ptx.From
}

// signAndSerializeStellarTx signs tx via the KeyManager (never locally - keypair.ParseAddress only
// needs the public StrKey to compute the signature hint) and returns the wire-ready raw bytes and
// the network-ID-qualified signature hash. Shared by PrepareSubmission and PrepareRestore - both
// build a txnbuild.Transaction sourced from the same account and need identical signing.
//
// Stellar requires a signature from every distinct operation-level source account, not just the
// envelope's own account (chapter 12 §12.2's channel-account pooling routinely creates this split:
// the envelope is sourced from the channel account, but buildStellarTx's InvokeHostFunction op is
// still sourced from the business identity in ptx.From, since that's what a contract's
// require_auth checks against) - stellar-core rejects the operation with opBadAuth otherwise. So
// envelopeSigner always signs, plus any additional operation source accounts found on tx.
func (s *stellarChainSubmitter) signAndSerializeStellarTx(ctx context.Context, envelopeSigner pldtypes.ChainAddress, tx *txnbuild.Transaction) ([]byte, pldtypes.Bytes32, error) {
	networkPassphrase := s.ptm.baseLedger.ChainInfo().NetworkID
	hash, err := tx.Hash(networkPassphrase)
	if err != nil {
		return nil, pldtypes.Bytes32{}, err
	}

	signers := []string{envelopeSigner.String()}
	for _, op := range tx.Operations() {
		opSource := op.GetSourceAccount()
		if opSource == "" || opSource == signers[0] {
			continue
		}
		alreadyIncluded := false
		for _, existing := range signers {
			if existing == opSource {
				alreadyIncluded = true
				break
			}
		}
		if !alreadyIncluded {
			signers = append(signers, opSource)
		}
	}

	for _, signer := range signers {
		resolvedKey, err := s.ptm.keymgr.ReverseKeyLookup(ctx, s.ptm.p.NOTX(), algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, signer)
		if err != nil {
			log.L(ctx).Errorf("signing failed to resolve key %s for signing: %s", signer, err)
			return nil, pldtypes.Bytes32{}, err
		}
		signature, err := s.ptm.keymgr.Sign(ctx, resolvedKey, signpayloads.OPAQUE_TO_EDDSA, pldtypes.HexBytes(hash[:]))
		if err != nil {
			log.L(ctx).Errorf("signing failed with keyHandle %s (addr=%s): %s", resolvedKey.KeyHandle, resolvedKey.Verifier.Verifier, err)
			return nil, pldtypes.Bytes32{}, err
		}
		signerAddr, err := keypair.ParseAddress(signer)
		if err != nil {
			return nil, pldtypes.Bytes32{}, err
		}
		tx, err = tx.AddSignatureDecorated(xdr.NewDecoratedSignature(signature, signerAddr.Hint()))
		if err != nil {
			return nil, pldtypes.Bytes32{}, err
		}
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
	rawTransaction, txHash, err := s.signAndSerializeStellarTx(ctx, envelopeSigningAddress(ptx), tx)
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

	// RestoreFootprintOp has no auth semantics tied to a specific business identity - unlike
	// buildStellarTx's InvokeHostFunction handling, both the envelope and the operation itself are
	// sourced from the channel account (chapter 12 §12.2) when one is assigned.
	envelopeAddr := envelopeSigningAddress(ptx).String()
	account := txnbuild.NewSimpleAccount(envelopeAddr, int64(*ptx.Nonce)-1) //nolint:gosec // sequence numbers are always positive
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &account,
		IncrementSequenceNum: true,
		Operations: []txnbuild.Operation{&txnbuild.RestoreFootprint{
			SourceAccount: envelopeAddr,
			Ext:           xdr.TransactionExt{V: 1, SorobanData: &sorobanData},
		}},
		BaseFee:       txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
	})
	if err != nil {
		return nil, err
	}
	rawTransaction, txHash, err := s.signAndSerializeStellarTx(ctx, envelopeSigningAddress(ptx), tx)
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
