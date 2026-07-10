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

// Package stellarclient mirrors ethclient's role for the Stellar base ledger (chapter 12 §12.1):
// it is a thin constructor, not a parallel API surface. Stellar RPC is HTTP/JSON-RPC only (no
// WebSocket transport mode to juggle, unlike ethclient/EthClientFactory), so there is no need for
// a factory with HTTPClient()/SharedWS()/NewWS() variants - this package builds a correctly
// configured *http.Client from Paladin's HTTPClientConfig conventions (TLS/auth/retry/timeouts,
// via the same sdk/go/pkg/pldresty helper ethclient's own HTTP transport is built on) and hands
// it to the SDK's own rpcclient.Client, which callers use directly.
package stellarclient

import (
	"context"

	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldresty"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
)

// NewClient constructs the Stellar SDK's own *rpcclient.Client, configured from Paladin's
// HTTPClientConfig conventions. Callers use the returned client's methods
// (SimulateTransaction/SendTransaction/GetTransaction/LoadAccount/GetHealth/GetNetwork/...)
// directly - there is no Paladin-specific wrapper type here. The returned close function
// releases the underlying HTTP transport's idle connections and should be called when the
// client is no longer needed (mirroring pldresty.PLDClient.Close()).
func NewClient(ctx context.Context, conf *pldconf.StellarClientConfig) (client *rpcclient.Client, closeFn func(), err error) {
	pldClient, err := pldresty.New(ctx, &conf.HTTPClientConfig)
	if err != nil {
		return nil, nil, err
	}
	client = rpcclient.NewClient(conf.HTTPClientConfig.URL, pldClient.GetClient())
	return client, pldClient.Close, nil
}
