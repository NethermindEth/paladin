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

package pldtypes

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
)

type ChainAddressKind string

const (
	ChainAddressKindEVM             ChainAddressKind = "evm"
	ChainAddressKindStellarAccount  ChainAddressKind = "stellar_account"
	ChainAddressKindStellarContract ChainAddressKind = "stellar_contract"
)

// ChainAddress is a text-first, chain-neutral base ledger address.
//
// The JSON/native text form is intentionally the native address string for the
// underlying chain. For EVM this preserves the existing 0x-prefixed public API
// representation. SQL storage keeps EVM values compatible with EthAddress by
// storing 40 hex characters without the 0x prefix.
type ChainAddress struct {
	kind ChainAddressKind
	text string
}

func NewEVMChainAddress(addr EthAddress) ChainAddress {
	return ChainAddress{
		kind: ChainAddressKindEVM,
		text: addr.String(),
	}
}

func NewStellarAccountAddress(strkey string) (ChainAddress, error) {
	if !strings.HasPrefix(strkey, "G") {
		return ChainAddress{}, fmt.Errorf("stellar account address must start with G")
	}
	return ChainAddress{kind: ChainAddressKindStellarAccount, text: strkey}, nil
}

func NewStellarContractAddress(strkey string) (ChainAddress, error) {
	if !strings.HasPrefix(strkey, "C") {
		return ChainAddress{}, fmt.Errorf("stellar contract address must start with C")
	}
	return ChainAddress{kind: ChainAddressKindStellarContract, text: strkey}, nil
}

func ParseChainAddress(s string) (*ChainAddress, error) {
	return ParseChainAddressCtx(context.Background(), s)
}

func ParseChainAddressCtx(_ context.Context, s string) (*ChainAddress, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("chain address is empty")
	}
	if isEVMAddressString(s) {
		ethAddr, err := ParseEthAddress(s)
		if err != nil {
			return nil, err
		}
		addr := NewEVMChainAddress(*ethAddr)
		return &addr, nil
	}
	switch {
	case strings.HasPrefix(s, "G"):
		addr, err := NewStellarAccountAddress(s)
		return &addr, err
	case strings.HasPrefix(s, "C"):
		addr, err := NewStellarContractAddress(s)
		return &addr, err
	default:
		return nil, fmt.Errorf("unsupported chain address format")
	}
}

func MustParseChainAddress(s string) *ChainAddress {
	addr, err := ParseChainAddress(s)
	if err != nil {
		panic(err)
	}
	return addr
}

func isEVMAddressString(s string) bool {
	trimmed := strings.TrimPrefix(s, "0x")
	trimmed = strings.TrimPrefix(trimmed, "0X")
	if len(trimmed) != 40 {
		return false
	}
	for _, r := range trimmed {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func (a ChainAddress) Kind() ChainAddressKind {
	return a.kind
}

func (a ChainAddress) String() string {
	return a.text
}

func (a ChainAddress) StorageString() string {
	if a.kind == ChainAddressKindEVM {
		return strings.TrimPrefix(strings.TrimPrefix(a.text, "0x"), "0X")
	}
	return a.text
}

func (a ChainAddress) IsZero() bool {
	return a.kind == "" && a.text == ""
}

func (a ChainAddress) Equals(b *ChainAddress) bool {
	if b == nil {
		return false
	}
	return a.kind == b.kind && a.text == b.text
}

func (a ChainAddress) EthAddress() (*EthAddress, error) {
	if a.kind != ChainAddressKindEVM {
		return nil, fmt.Errorf("chain address kind %q is not an EVM address", a.kind)
	}
	return ParseEthAddress(a.text)
}

func (a ChainAddress) MarshalJSON() ([]byte, error) {
	return json.Marshal(a.String())
}

func (a *ChainAddress) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := ParseChainAddress(s)
	if err != nil {
		return err
	}
	*a = *parsed
	return nil
}

func (a ChainAddress) Value() (driver.Value, error) {
	if a.IsZero() {
		return nil, nil
	}
	return a.StorageString(), nil
}

func (a *ChainAddress) Scan(src interface{}) error {
	switch v := src.(type) {
	case nil:
		return nil
	case string:
		parsed, err := ParseChainAddress(v)
		if err != nil {
			return err
		}
		*a = *parsed
		return nil
	case []byte:
		parsed, err := ParseChainAddress(string(v))
		if err != nil {
			return err
		}
		*a = *parsed
		return nil
	default:
		return fmt.Errorf("unable to scan type %T into ChainAddress", src)
	}
}
