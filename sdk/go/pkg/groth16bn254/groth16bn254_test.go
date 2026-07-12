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

package groth16bn254

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFieldElementBytes(t *testing.T) {
	b, err := FieldElementBytes("1")
	require.NoError(t, err)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000000001", hex.EncodeToString(b[:]))

	b, err = FieldElementBytes("0")
	require.NoError(t, err)
	assert.Equal(t, make([]byte, 32), b[:])

	// 2^255 exceeds the ~254-bit BN254 field but still fits in 32 bytes - only overflow of the
	// byte-length representation is rejected here, not field-membership (the host validates that).
	b, err = FieldElementBytes("256")
	require.NoError(t, err)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000000100", hex.EncodeToString(b[:]))
}

func TestFieldElementBytesRejectsInvalid(t *testing.T) {
	_, err := FieldElementBytes("not-a-number")
	assert.Error(t, err)

	_, err = FieldElementBytes("-1")
	assert.Error(t, err)

	// 2^256 needs 33 bytes - too large.
	tooLarge := "115792089237316195423570985008687907853269984665640564039457584007913129639936"
	_, err = FieldElementBytes(tooLarge)
	assert.Error(t, err)
}

func TestG1Bytes(t *testing.T) {
	b, err := G1Bytes("1", "2")
	require.NoError(t, err)
	require.Len(t, b, 64)
	expected := make([]byte, 64)
	expected[31] = 1
	expected[63] = 2
	assert.Equal(t, expected, b[:])
}

func TestG2BytesOrdersC1BeforeC0(t *testing.T) {
	// x = (c0=1, c1=2), y = (c0=3, c1=4) -> Soroban wants x.c1||x.c0||y.c1||y.c0
	b, err := G2Bytes("1", "2", "3", "4")
	require.NoError(t, err)
	require.Len(t, b, 128)
	expected := make([]byte, 128)
	expected[31] = 2  // x.c1
	expected[63] = 1  // x.c0
	expected[95] = 4  // y.c1
	expected[127] = 3 // y.c0
	assert.Equal(t, expected, b[:])
}

func TestProofBytesRealSnarkjsShape(t *testing.T) {
	a := []string{"10", "20", "1"}
	b := [][]string{{"1", "2"}, {"3", "4"}, {"1", "0"}}
	c := []string{"30", "40", "1"}

	out, err := ProofBytes(a, b, c)
	require.NoError(t, err)

	wantA, _ := G1Bytes("10", "20")
	wantB, _ := G2Bytes("1", "2", "3", "4")
	wantC, _ := G1Bytes("30", "40")

	assert.Equal(t, wantA[:], out[0:64])
	assert.Equal(t, wantB[:], out[64:192])
	assert.Equal(t, wantC[:], out[192:256])
}

func TestProofBytesRejectsMalformed(t *testing.T) {
	_, err := ProofBytes([]string{"1"}, [][]string{{"1", "2"}, {"3", "4"}}, []string{"1", "2"})
	assert.Error(t, err)

	_, err = ProofBytes([]string{"1", "2"}, [][]string{{"1"}, {"3", "4"}}, []string{"1", "2"})
	assert.Error(t, err)

	_, err = ProofBytes([]string{"1", "2"}, [][]string{{"1", "2"}, {"3", "4"}}, []string{"1"})
	assert.Error(t, err)
}

func TestParseVerificationKey(t *testing.T) {
	alpha := []string{"1", "2", "1"}
	beta := [][]string{{"3", "4"}, {"5", "6"}, {"1", "0"}}
	gamma := [][]string{{"7", "8"}, {"9", "10"}, {"1", "0"}}
	delta := [][]string{{"11", "12"}, {"13", "14"}, {"1", "0"}}
	ic := [][]string{{"15", "16", "1"}, {"17", "18", "1"}}

	vk, err := ParseVerificationKey(alpha, beta, gamma, delta, ic)
	require.NoError(t, err)

	wantAlpha, _ := G1Bytes("1", "2")
	assert.Equal(t, wantAlpha, vk.Alpha)
	require.Len(t, vk.IC, 2)
	wantIC0, _ := G1Bytes("15", "16")
	assert.Equal(t, wantIC0, vk.IC[0])
}

func TestParseVerificationKeyRejectsMalformed(t *testing.T) {
	_, err := ParseVerificationKey([]string{"1"}, nil, nil, nil, nil)
	assert.Error(t, err)
}
