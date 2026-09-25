// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Throwaway spike (chapter 13 Part B, phase B.2.1): confirms that Go's
// github.com/LFDT-Paladin/smt (the SMT library the real Paladin domains use) and Rust's
// soroban/contracts/szeto/src/tree.rs (a from-scratch port of the same SmtLib.sol algorithm)
// agree on the resulting root after inserting the same leaves in the same order. tree.rs's own
// doc comments only claim fidelity against vendored Solidity; this is the first direct check
// against the Go implementation this repo's real domains actually use.
package main

import (
	"context"
	"fmt"
	"math/big"

	core "github.com/LFDT-Paladin/smt/pkg/sparse-merkle-tree/core"
	node "github.com/LFDT-Paladin/smt/pkg/sparse-merkle-tree/node"
	smt "github.com/LFDT-Paladin/smt/pkg/sparse-merkle-tree/smt"
	utxo "github.com/LFDT-Paladin/smt/pkg/utxo"
	utxocore "github.com/LFDT-Paladin/smt/pkg/utxo/core"
)

// memStorage is a minimal in-memory core.Storage - no SQL/gorm needed for this parity check.
type memStorage struct {
	hasher utxocore.Hasher
	root   core.NodeRef
	nodes  map[string]core.Node
}

func newMemStorage(hasher utxocore.Hasher) *memStorage {
	return &memStorage{hasher: hasher, nodes: map[string]core.Node{}}
}

func (s *memStorage) GetRootNodeRef(context.Context) (core.NodeRef, error) {
	if s.root == nil {
		return nil, core.ErrNotFound
	}
	return s.root, nil
}

func (s *memStorage) UpsertRootNodeRef(_ context.Context, r core.NodeRef) error {
	s.root = r
	return nil
}

func (s *memStorage) GetNode(_ context.Context, key core.NodeRef) (core.Node, error) {
	n, ok := s.nodes[key.Hex()]
	if !ok {
		return nil, core.ErrNotFound
	}
	return n, nil
}

func (s *memStorage) InsertNode(_ context.Context, n core.Node) error {
	s.nodes[n.Ref().Hex()] = n
	return nil
}

func (s *memStorage) BeginTx(context.Context) (core.Transaction, error) {
	return &memTx{s}, nil
}

func (s *memStorage) Close() {}

func (s *memStorage) GetHasher() utxocore.Hasher { return s.hasher }

// memTx writes straight through to memStorage - fine for a single-goroutine spike, no real
// transactional isolation needed.
type memTx struct{ s *memStorage }

func (t *memTx) UpsertRootNodeRef(ctx context.Context, r core.NodeRef) error {
	return t.s.UpsertRootNodeRef(ctx, r)
}
func (t *memTx) GetNode(ctx context.Context, key core.NodeRef) (core.Node, error) {
	return t.s.GetNode(ctx, key)
}
func (t *memTx) InsertNode(ctx context.Context, n core.Node) error {
	return t.s.InsertNode(ctx, n)
}
func (t *memTx) Commit(context.Context) error   { return nil }
func (t *memTx) Rollback(context.Context) error { return nil }

func main() {
	ctx := context.Background()
	hasher := utxo.NewPoseidonHasher()
	storage := newMemStorage(hasher)

	// MAX_SMT_DEPTH in tree.rs.
	tree, err := smt.NewMerkleTree(ctx, storage, 64)
	if err != nil {
		panic(err)
	}

	// NodeRef.Hex() returns the little-endian byte layout used for internal storage keys - use
	// BigInt() (which un-swaps back to a normal big-endian integer) to get a hex string
	// comparable against Rust's U256::to_be_bytes().
	rootHex := func(r core.NodeRef) string { return fmt.Sprintf("%064x", r.BigInt()) }

	fmt.Printf("empty root: %s\n", rootHex(tree.Root()))

	// index == value, matching lib.rs's actual call site: tree::insert_leaf(&env, value.clone(),
	// value) where value is the output commitment.
	leaves := []string{"1", "2", "42"}
	for _, v := range leaves {
		val, ok := new(big.Int).SetString(v, 10)
		if !ok {
			panic("bad decimal literal: " + v)
		}
		idx, err := node.NewNodeIndexFromBigInt(val, hasher)
		if err != nil {
			panic(err)
		}
		leafNode, err := node.NewLeafNode(node.NewIndexOnly(idx), val)
		if err != nil {
			panic(err)
		}
		if err := tree.AddLeaf(ctx, leafNode); err != nil {
			panic(err)
		}
		fmt.Printf("after inserting %s: root=%s\n", v, rootHex(tree.Root()))
	}
}
