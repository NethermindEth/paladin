// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package stellarclient

import (
	"context"
	"testing"

	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/stretchr/testify/require"
)

func TestNewClientBuildsConfiguredRPCClient(t *testing.T) {
	conf := &pldconf.StellarClientConfig{
		HTTPClientConfig: pldconf.HTTPClientConfig{
			URL: "https://soroban-testnet.stellar.org",
		},
		NetworkPassphrase: "Test SDF Network ; September 2015",
	}

	client, closeFn, err := NewClient(context.Background(), conf)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, closeFn)
	closeFn() // must not panic
}

func TestNewClientPropagatesTLSConfigErrors(t *testing.T) {
	conf := &pldconf.StellarClientConfig{
		HTTPClientConfig: pldconf.HTTPClientConfig{
			URL: "https://soroban-testnet.stellar.org",
			TLS: pldconf.TLSConfig{
				Enabled: true,
				CA:      "not a valid PEM certificate",
			},
		},
	}

	_, _, err := NewClient(context.Background(), conf)
	require.Error(t, err)
}
