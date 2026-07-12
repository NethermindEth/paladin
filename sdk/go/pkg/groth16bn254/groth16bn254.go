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

// Package groth16bn254 converts snarkjs-format (decimal-string) Groth16 proof/verification-key
// material for the BN254 curve into the byte layout Soroban's native crypto::bn254 host
// functions expect (chapter 13 Phase 5, SZeto's M0 feasibility spike).
//
// The math mirrors NethermindEth/stellar-private-payments's `circuit-keys` crate
// (`parse_fq_decimal`/`g1_to_soroban_bytes`/`g2_to_soroban_bytes`/`fq2_from_decimals`,
// Apache-2.0), ported to Go since this repo's translation happens at Go-side transaction-assembly
// time (converting Zeto's existing Go prover's per-transaction output), not at Rust build time.
package groth16bn254

import (
	"fmt"
	"math/big"
)

const fieldElementSize = 32

// FieldElementBytes parses a base-10 decimal string (snarkjs's field-element convention) into a
// 32-byte big-endian representation.
func FieldElementBytes(decimal string) ([fieldElementSize]byte, error) {
	var out [fieldElementSize]byte
	v, ok := new(big.Int).SetString(decimal, 10)
	if !ok {
		return out, fmt.Errorf("invalid decimal field element: %q", decimal)
	}
	if v.Sign() < 0 {
		return out, fmt.Errorf("field element must not be negative: %q", decimal)
	}
	b := v.Bytes()
	if len(b) > fieldElementSize {
		return out, fmt.Errorf("field element too large for %d bytes: %q", fieldElementSize, decimal)
	}
	copy(out[fieldElementSize-len(b):], b)
	return out, nil
}

// G1Bytes converts a snarkjs-format G1 point (x, y decimal strings - the trailing "1" projective
// coordinate is not part of the affine representation and is not passed here) into Soroban's
// 64-byte (x || y) big-endian layout.
func G1Bytes(x, y string) ([64]byte, error) {
	var out [64]byte
	xb, err := FieldElementBytes(x)
	if err != nil {
		return out, fmt.Errorf("G1.x: %w", err)
	}
	yb, err := FieldElementBytes(y)
	if err != nil {
		return out, fmt.Errorf("G1.y: %w", err)
	}
	copy(out[:32], xb[:])
	copy(out[32:], yb[:])
	return out, nil
}

// G2Bytes converts a snarkjs-format G2 point (x = [c0, c1], y = [c0, c1] decimal strings) into
// Soroban's 128-byte layout. Soroban orders Fp2 components as c1||c0 (imaginary||real) - the
// reverse of snarkjs's own [c0, c1] JSON convention - confirmed against soroban-sdk's own doc
// comment on Bn254G2Affine and independently corroborated by the reference implementation's
// g2_to_soroban_bytes helper.
func G2Bytes(xC0, xC1, yC0, yC1 string) ([128]byte, error) {
	var out [128]byte
	xc0b, err := FieldElementBytes(xC0)
	if err != nil {
		return out, fmt.Errorf("G2.x.c0: %w", err)
	}
	xc1b, err := FieldElementBytes(xC1)
	if err != nil {
		return out, fmt.Errorf("G2.x.c1: %w", err)
	}
	yc0b, err := FieldElementBytes(yC0)
	if err != nil {
		return out, fmt.Errorf("G2.y.c0: %w", err)
	}
	yc1b, err := FieldElementBytes(yC1)
	if err != nil {
		return out, fmt.Errorf("G2.y.c1: %w", err)
	}
	copy(out[0:32], xc1b[:])
	copy(out[32:64], xc0b[:])
	copy(out[64:96], yc1b[:])
	copy(out[96:128], yc0b[:])
	return out, nil
}

// ProofBytes converts a full snarkjs Groth16 proof - `a` as [x, y, "1"], `b` as
// [[x_c0, x_c1], [y_c0, y_c1], ["1", "0"]], `c` as [x, y, "1"] (exactly the shape
// `github.com/iden3/go-rapidsnark/types.ProofData` and Zeto's own `pb.SnarkProof` carry) - into
// Soroban's 256-byte Groth16Proof layout: a (64B) || b (128B) || c (64B).
func ProofBytes(a []string, b [][]string, c []string) ([256]byte, error) {
	var out [256]byte
	if len(a) < 2 {
		return out, fmt.Errorf("proof.a: expected at least 2 elements, got %d", len(a))
	}
	if len(b) < 2 || len(b[0]) < 2 || len(b[1]) < 2 {
		return out, fmt.Errorf("proof.b: expected [[x_c0,x_c1],[y_c0,y_c1],...] shape")
	}
	if len(c) < 2 {
		return out, fmt.Errorf("proof.c: expected at least 2 elements, got %d", len(c))
	}

	aBytes, err := G1Bytes(a[0], a[1])
	if err != nil {
		return out, fmt.Errorf("proof.a: %w", err)
	}
	bBytes, err := G2Bytes(b[0][0], b[0][1], b[1][0], b[1][1])
	if err != nil {
		return out, fmt.Errorf("proof.b: %w", err)
	}
	cBytes, err := G1Bytes(c[0], c[1])
	if err != nil {
		return out, fmt.Errorf("proof.c: %w", err)
	}

	copy(out[0:64], aBytes[:])
	copy(out[64:192], bBytes[:])
	copy(out[192:256], cBytes[:])
	return out, nil
}

// VerificationKeyBytes holds a Groth16 verification key in Soroban's byte layout, ready to be
// XDR-encoded as a Soroban `VerificationKeyBytes`-shaped contract type.
type VerificationKeyBytes struct {
	Alpha [64]byte
	Beta  [128]byte
	Gamma [128]byte
	Delta [128]byte
	IC    [][64]byte
}

// ParseVerificationKey converts a snarkjs `verification_key.json` (already unmarshaled into decimal
// strings) into Soroban's byte layout. `alpha`/`ic[i]` are G1 points ([x, y, "1"]); `beta`/
// `gamma`/`delta` are G2 points ([[x_c0,x_c1],[y_c0,y_c1],["1","0"]]).
func ParseVerificationKey(alpha []string, beta, gamma, delta [][]string, ic [][]string) (VerificationKeyBytes, error) {
	var vk VerificationKeyBytes

	if len(alpha) < 2 {
		return vk, fmt.Errorf("vk.alpha: expected at least 2 elements, got %d", len(alpha))
	}
	alphaBytes, err := G1Bytes(alpha[0], alpha[1])
	if err != nil {
		return vk, fmt.Errorf("vk.alpha: %w", err)
	}
	vk.Alpha = alphaBytes

	parseG2Field := func(name string, pt [][]string) ([128]byte, error) {
		var out [128]byte
		if len(pt) < 2 || len(pt[0]) < 2 || len(pt[1]) < 2 {
			return out, fmt.Errorf("vk.%s: expected [[x_c0,x_c1],[y_c0,y_c1],...] shape", name)
		}
		return G2Bytes(pt[0][0], pt[0][1], pt[1][0], pt[1][1])
	}

	if vk.Beta, err = parseG2Field("beta", beta); err != nil {
		return vk, err
	}
	if vk.Gamma, err = parseG2Field("gamma", gamma); err != nil {
		return vk, err
	}
	if vk.Delta, err = parseG2Field("delta", delta); err != nil {
		return vk, err
	}

	vk.IC = make([][64]byte, len(ic))
	for i, pt := range ic {
		if len(pt) < 2 {
			return vk, fmt.Errorf("vk.ic[%d]: expected at least 2 elements, got %d", i, len(pt))
		}
		icBytes, err := G1Bytes(pt[0], pt[1])
		if err != nil {
			return vk, fmt.Errorf("vk.ic[%d]: %w", i, err)
		}
		vk.IC[i] = icBytes
	}

	return vk, nil
}
