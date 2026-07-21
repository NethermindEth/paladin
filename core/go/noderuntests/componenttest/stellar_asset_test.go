//go:build stellar_quickstart

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

package componenttest

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	baseledgerstellar "github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/core/pkg/stellarclient"
	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldclient"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stretchr/testify/require"
)

// stellarRPCURL/stellarNetworkPassphrase mirror the same values every other helper in this package
// uses (loadStellarFixtures/deploy-stellar-fixtures.sh's own network settings) - the actual
// on-chain plumbing this file exercises (classic ChangeTrust/Payment operations) is otherwise
// entirely independent of any Paladin node's own config. Overridable via STELLAR_RPC_URL/
// STELLAR_NETWORK_PASSPHRASE for a manual testnet run (see stellar_component_test.go's own
// override-point doc comment) - default to local stellar_quickstart's values.
func stellarRPCURL() string {
	if url := os.Getenv("STELLAR_RPC_URL"); url != "" {
		return url
	}
	return "http://localhost:8000/soroban/rpc"
}

func stellarNetworkPassphrase() string {
	if passphrase := os.Getenv("STELLAR_NETWORK_PASSPHRASE"); passphrase != "" {
		return passphrase
	}
	return "Standalone Network ; February 2017"
}

// newStellarRPCClient builds a real Stellar RPC client (and the baseledger.Client wrapping it,
// used only for Submit/GetTransactionResult - LoadAccount is called on the raw rpc client, since
// building a txnbuild.Transaction needs the txnbuild.Account interface that only the raw client
// exposes) against local stellar_quickstart. Not routed through any Paladin domain plugin -
// classic-asset issuance/trustline setup is infrastructure the demo needs before any deposit/
// withdraw transaction can run, not itself a Paladin private transaction.
func newStellarRPCClient(t *testing.T, ctx context.Context) (*rpcclient.Client, *baseledgerstellar.Client) {
	t.Helper()
	rpc, closeFn, err := stellarclient.NewClient(ctx, &pldconf.StellarClientConfig{
		HTTPClientConfig: pldconf.HTTPClientConfig{URL: stellarRPCURL()},
	})
	require.NoError(t, err)
	t.Cleanup(closeFn)
	return rpc, baseledgerstellar.WrapClient(rpc, stellarNetworkPassphrase(), nil)
}

// generateAndFundIssuer creates a fresh ed25519 keypair (external to every Paladin node - the
// asset issuer is not itself a Paladin-managed identity) and funds it via the same quickstart
// friendbot fundRootFunderViaFriendbot already uses for "root".
func generateAndFundIssuer(t *testing.T) *keypair.Full {
	t.Helper()
	issuer, err := keypair.Random()
	require.NoError(t, err)
	fundAddressViaFriendbot(t, issuer.Address())
	return issuer
}

// fundAddressViaFriendbot funds an arbitrary Stellar address via quickstart's friendbot - the same
// mechanism fundRootFunderViaFriendbot uses for "root", needed here for the SAME reason: a
// Paladin-managed identity's own resolved verifier address is never itself the on-chain source of
// any Paladin-submitted transaction (those are all sourced from derived channel accounts - chapter
// 12 §12.2), so it has no on-chain ledger entry at all until explicitly funded - a prerequisite for
// a party to source their own classic ChangeTrust operation.
func fundAddressViaFriendbot(t *testing.T, addr string) {
	t.Helper()
	resp, err := http.Get(stellarFriendbotURL() + "?addr=" + addr)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	alreadyFunded := resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "already funded")
	require.True(t, resp.StatusCode == http.StatusOK || alreadyFunded,
		"failed to fund address %s via friendbot: HTTP %d: %s", addr, resp.StatusCode, body)
}

// submitClassicOpsSignedByIssuer builds a transaction (source = issuer) from ops, signs it
// directly with the issuer's own raw keypair (no Paladin identity involved - the issuer is an
// external account this test fully controls), and submits it, waiting for confirmation.
func submitClassicOpsSignedByIssuer(t *testing.T, ctx context.Context, rpc *rpcclient.Client, blClient *baseledgerstellar.Client, issuer *keypair.Full, ops []txnbuild.Operation) {
	t.Helper()
	account, err := rpc.LoadAccount(ctx, issuer.Address())
	require.NoError(t, err)
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        account,
		IncrementSequenceNum: true,
		Operations:           ops,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
	})
	require.NoError(t, err)
	tx, err = tx.Sign(stellarNetworkPassphrase(), issuer)
	require.NoError(t, err)
	submitSignedStellarTx(t, ctx, blClient, tx)
}

// submitClassicOpsSignedByParty builds a transaction (source = the resolved Stellar address of a
// Paladin-managed identity) from ops, signs its hash via that node's own keymgr_sign RPC (the
// identity's private key never leaves the Paladin node), and submits it, waiting for confirmation.
// This is the only way to get a Paladin-managed identity's signature over an arbitrary classic
// Stellar operation (like ChangeTrust) that Paladin's own domain-transaction pipeline never
// constructs itself.
func submitClassicOpsSignedByParty(t *testing.T, ctx context.Context, rpc *rpcclient.Client, blClient *baseledgerstellar.Client, client pldclient.PaladinClient, identity, trustorAddr string, ops []txnbuild.Operation) {
	t.Helper()
	account, err := rpc.LoadAccount(ctx, trustorAddr)
	require.NoError(t, err)
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        account,
		IncrementSequenceNum: true,
		Operations:           ops,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
	})
	require.NoError(t, err)

	hash, err := tx.Hash(stellarNetworkPassphrase())
	require.NoError(t, err)
	signature, err := client.KeyManager().Sign(ctx, identity, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, signpayloads.OPAQUE_TO_EDDSA, hash[:])
	require.NoError(t, err)

	tx, err = tx.AddSignatureBase64(stellarNetworkPassphrase(), trustorAddr, base64.StdEncoding.EncodeToString(signature))
	require.NoError(t, err)
	submitSignedStellarTx(t, ctx, blClient, tx)
}

func submitSignedStellarTx(t *testing.T, ctx context.Context, blClient *baseledgerstellar.Client, tx *txnbuild.Transaction) {
	t.Helper()
	envelopeB64, err := tx.Base64()
	require.NoError(t, err)
	envelopeBytes, err := base64.StdEncoding.DecodeString(envelopeB64)
	require.NoError(t, err)

	txID, err := blClient.Submit(ctx, envelopeBytes)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		result, err := blClient.GetTransactionResult(ctx, txID)
		if err != nil {
			return false
		}
		require.True(t, result.Success, "classic operation transaction failed on-chain")
		return true
	}, 30*time.Second, 500*time.Millisecond, "classic operation transaction %s was not confirmed", txID)
}

// deploySACForAsset registers the Stellar Asset Contract (SAC) wrapper for a classic asset via the
// `stellar` CLI (the same tool soroban/scripts/deploy-stellar-fixtures.sh already shells out to,
// for the same reason: no exported SDK helper in this repo derives/registers a SAC contract id
// today). Returns the deployed contract's address (the CLI's sole stdout line on success). This is
// the real Soroban contract address SNoto's initialize() needs for its `sac` parameter - not the
// classic issuer/asset-code pair itself.
func deploySACForAsset(t *testing.T, ctx context.Context, issuer *keypair.Full, assetCode string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "stellar", "contract", "asset", "deploy",
		"--asset", assetCode+":"+issuer.Address(),
		"--source-account", issuer.Seed(),
		"--rpc-url", stellarRPCURL(),
		"--network-passphrase", stellarNetworkPassphrase(),
	)
	out, err := cmd.Output()
	require.NoError(t, err, "stellar contract asset deploy failed: %s", exitErrOutput(err))
	sacAddress := strings.TrimSpace(string(out))
	require.NotEmpty(t, sacAddress)
	return sacAddress
}

// invokeSACTransfer calls the SAC's own SEP-41 `transfer(from, to, amount)` function directly via
// the `stellar` CLI, signed by the issuer - used to fund the SNoto contract's own real on-chain SAC
// balance directly (bypassing SNoto's `deposit`, which needs real second-signer Soroban
// authorization that doesn't exist yet - see handler_deposit.go's own doc comment). `to` may be a
// classic G-account (needs its own trustline first) or a Soroban C-contract address (no trustline
// concept applies - SAC balances for contract addresses are tracked in the SAC's own contract
// storage, confirmed by the Rust unit test suite's own deposit tests, which transfer to
// env.current_contract_address() with no trustline setup at all).
func invokeSACTransfer(t *testing.T, ctx context.Context, issuer *keypair.Full, sacAddress, to string, amount int64) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "stellar", "contract", "invoke",
		"--id", sacAddress,
		"--source-account", issuer.Seed(),
		"--rpc-url", stellarRPCURL(),
		"--network-passphrase", stellarNetworkPassphrase(),
		"--", "transfer",
		"--from", issuer.Address(),
		"--to", to,
		"--amount", strconv.FormatInt(amount, 10),
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stellar contract invoke transfer failed: %s", out)
}

func exitErrOutput(err error) string {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(exitErr.Stderr)
	}
	return fmt.Sprintf("%v", err)
}

// establishTrustlineAndFund creates a trustline from party's own Paladin-managed identity
// (trustorAddr - already resolved and funded by the caller via resolveAndFundVerifier, so this
// doesn't repeat that step itself) to asset (via BuildChangeTrustPayload -
// core/go/pkg/baseledger/stellar/classic_ops.go, previously implemented but never called by
// anything - see this session's own tracked follow-up work) and then funds it with an initial
// balance via a classic Payment from the issuer.
func establishTrustlineAndFund(t *testing.T, ctx context.Context, rpc *rpcclient.Client, blClient *baseledgerstellar.Client, client pldclient.PaladinClient, identity, trustorAddr string, asset *txnbuild.CreditAsset, issuer *keypair.Full, amount string) {
	t.Helper()
	payload, err := baseledgerstellar.BuildChangeTrustPayload(trustorAddr, asset, "")
	require.NoError(t, err)
	ops, err := baseledgerstellar.DecodeClassicOperations(payload)
	require.NoError(t, err)
	submitClassicOpsSignedByParty(t, ctx, rpc, blClient, client, identity, trustorAddr, ops)

	submitClassicOpsSignedByIssuer(t, ctx, rpc, blClient, issuer, []txnbuild.Operation{&txnbuild.Payment{
		Destination: trustorAddr,
		Amount:      amount,
		Asset:       asset,
	}})
}
