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

package signers

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signerapi"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestEdDSASigner(t *testing.T, seed []byte) (context.Context, *eddsaSigner) {
	ctx := context.Background()
	signerFactory := NewEdDSASignerFactory[*signerapi.ConfigNoExt]()
	signer, err := signerFactory.NewSigner(ctx, &signerapi.ConfigNoExt{})
	require.NoError(t, err)
	return ctx, signer.(*eddsaSigner)
}

func TestEdDSASignAndVerifyMatchesStellarSDK(t *testing.T) {
	seed := pldtypes.RandBytes(ed25519.SeedSize)
	ctx, signer := newTestEdDSASigner(t, seed)

	// The Stellar address this signer derives must match the SDK's own derivation from the same seed.
	kp, err := keypair.FromRawSeed([32]byte(seed))
	require.NoError(t, err)

	address, err := signer.GetVerifier(ctx, "eddsa:ed25519", verifiers.STELLAR_ADDRESS, seed)
	require.NoError(t, err)
	assert.Equal(t, kp.Address(), address)

	payload := pldtypes.RandBytes(32)
	sig, err := signer.Sign(ctx, "eddsa:ed25519", signpayloads.OPAQUE_TO_EDDSA, seed, payload)
	require.NoError(t, err)
	require.Len(t, sig, ed25519.SignatureSize)

	// The signature must verify both against the raw ed25519 public key and via the SDK's keypair.
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	assert.True(t, ed25519.Verify(pub, payload, sig))
	require.NoError(t, kp.Verify(payload, sig))
}

func TestEdDSASignerErrors(t *testing.T) {
	seed := pldtypes.RandBytes(ed25519.SeedSize)
	ctx, signer := newTestEdDSASigner(t, seed)

	_, err := signer.Sign(ctx, "eddsa:unknown", "", nil, nil)
	assert.Regexp(t, "PD020828", err)

	_, err = signer.Sign(ctx, "eddsa:ed25519", "wrong", seed, []byte{0x01})
	assert.Regexp(t, "PD020824", err)

	_, err = signer.Sign(ctx, "eddsa:ed25519", signpayloads.OPAQUE_TO_EDDSA, seed, nil)
	assert.Regexp(t, "PD020825", err)

	_, err = signer.GetVerifier(ctx, "eddsa:unknown", "", nil)
	assert.Regexp(t, "PD020828", err)

	_, err = signer.GetVerifier(ctx, "eddsa:ed25519", "wrong", seed)
	assert.Regexp(t, "PD020823", err)

	_, err = signer.GetMinimumKeyLen(ctx, "eddsa:unknown")
	assert.Regexp(t, "PD020828", err)

	minLen, err := signer.GetMinimumKeyLen(ctx, "eddsa:ed25519")
	require.NoError(t, err)
	assert.Equal(t, ed25519.SeedSize, minLen)
}
