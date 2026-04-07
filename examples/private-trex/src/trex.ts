/*
 * Copyright © 2025 Kaleido, Inc.
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

import PaladinClient, {
  PaladinVerifier,
  TransactionType,
} from "@lfdecentralizedtrust/paladin-sdk";
import { checkReceipt, DEFAULT_POLL_TIMEOUT } from "paladin-example-common";
import { contracts } from "@erc3643org/erc-3643";

const {
  ClaimTopicsRegistry: CTR,
  TrustedIssuersRegistry: TIR,
  IdentityRegistryStorage: IRS,
  IdentityRegistry: IR,
  ModularCompliance: MC,
  Token,
} = contracts;

const ZERO_ADDRESS = "0x0000000000000000000000000000000000000000";

export interface TREXSuite {
  token: string;
  identityRegistry: string;
  identityRegistryStorage: string;
  claimTopicsRegistry: string;
  trustedIssuersRegistry: string;
  modularCompliance: string;
  dummyIdentity: string;
}

// Internal helpers

async function deploy(
  paladin: PaladinClient,
  from: PaladinVerifier,
  artifact: { abi: any[]; bytecode: string },
  args?: Record<string, any>,
): Promise<string> {
  const txId = await paladin.ptx.sendTransaction({
    type: TransactionType.PUBLIC,
    from: from.lookup,
    data: args ?? {},
    function: "",
    abi: artifact.abi,
    bytecode: artifact.bytecode,
  });
  const receipt = await paladin.pollForReceipt(txId, DEFAULT_POLL_TIMEOUT);
  if (!checkReceipt(receipt)) throw new Error("Contract deployment failed");
  return receipt!.contractAddress!;
}

async function deployAndInit(
  paladin: PaladinClient,
  from: PaladinVerifier,
  artifact: { abi: any[]; bytecode: string },
  initArgs: Record<string, any> = {},
): Promise<string> {
  const addr = await deploy(paladin, from, artifact);
  await call(paladin, from, addr, artifact.abi, "init", initArgs);
  return addr;
}

async function call(
  paladin: PaladinClient,
  from: PaladinVerifier,
  to: string,
  abi: any[],
  fn: string,
  args: Record<string, any>,
): Promise<void> {
  const txId = await paladin.ptx.sendTransaction({
    type: TransactionType.PUBLIC,
    from: from.lookup,
    to,
    data: args,
    function: fn,
    abi,
  });
  const receipt = await paladin.pollForReceipt(txId, DEFAULT_POLL_TIMEOUT);
  if (!checkReceipt(receipt)) throw new Error(`Call to ${fn} failed`);
}

async function query(
  paladin: PaladinClient,
  from: PaladinVerifier,
  to: string,
  abi: any[],
  fn: string,
  args: Record<string, any>,
): Promise<any> {
  return paladin.ptx.call({
    type: TransactionType.PUBLIC,
    from: from.lookup,
    to,
    data: args,
    function: fn,
    abi,
  });
}

// Deploy

/**
 * Deploys the 6-contract T-REX suite.
 *
 * ClaimTopicsRegistry and TrustedIssuersRegistry are left empty, so
 * isVerified() degenerates to a registered/not-registered whitelist check.
 * ModularCompliance has no modules, so canTransfer() is always true.
 *
 * Token starts PAUSED — call unpause() after setup.
 */
export async function deployTREX(
  paladin: PaladinClient,
  deployer: PaladinVerifier,
  name: string,
  symbol: string,
  agents: { token: string[]; ir: string[] },
): Promise<TREXSuite> {
  const log = (msg: string) => console.log(`  [T-REX] ${msg}`);

  log("Deploying contracts...");
  const ctr = await deployAndInit(paladin, deployer, CTR);
  const tir = await deployAndInit(paladin, deployer, TIR);
  const irs = await deployAndInit(paladin, deployer, IRS);
  const ir = await deployAndInit(paladin, deployer, IR, {
    _trustedIssuersRegistry: tir,
    _claimTopicsRegistry: ctr,
    _identityStorage: irs,
  });
  const mc = await deployAndInit(paladin, deployer, MC);
  const token = await deployAndInit(paladin, deployer, Token, {
    _identityRegistry: ir,
    _compliance: mc,
    _name: name,
    _symbol: symbol,
    _decimals: 18,
    _onchainID: ZERO_ADDRESS,
  });

  // Wire contracts together
  log("Wiring contracts...");
  await call(paladin, deployer, irs, IRS.abi, "bindIdentityRegistry", { _identityRegistry: ir });
  await call(paladin, deployer, mc, MC.abi, "bindToken", { _token: token });
  await call(paladin, deployer, ir, IR.abi, "addAgent", { _agent: token });

  for (const agent of agents.ir) {
    await call(paladin, deployer, ir, IR.abi, "addAgent", { _agent: agent });
  }
  for (const agent of agents.token) {
    await call(paladin, deployer, token, Token.abi, "addAgent", { _agent: agent });
  }

  // With empty CTR, isVerified() only checks identity != address(0),
  // so any deployed contract address works as a dummy identity.
  log(`Deployed (token=${token}, ir=${ir}, token is PAUSED)`);

  return {
    token,
    identityRegistry: ir,
    identityRegistryStorage: irs,
    claimTopicsRegistry: ctr,
    trustedIssuersRegistry: tir,
    modularCompliance: mc,
    dummyIdentity: ctr,
  };
}

// Token operations

export const mint = (p: PaladinClient, agent: PaladinVerifier, token: string, to: string, amount: bigint | number) =>
  call(p, agent, token, Token.abi, "mint", { _to: to, _amount: amount.toString() });

export const transfer = (p: PaladinClient, from: PaladinVerifier, token: string, to: string, amount: bigint | number) =>
  call(p, from, token, Token.abi, "transfer", { _to: to, _amount: amount.toString() });

export const approve = (p: PaladinClient, from: PaladinVerifier, token: string, spender: string, amount: bigint | number) =>
  call(p, from, token, Token.abi, "approve", { _spender: spender, _amount: amount.toString() });

export const forcedTransfer = (p: PaladinClient, agent: PaladinVerifier, token: string, from: string, to: string, amount: bigint | number) =>
  call(p, agent, token, Token.abi, "forcedTransfer", { _from: from, _to: to, _amount: amount.toString() });

export const setAddressFrozen = (p: PaladinClient, agent: PaladinVerifier, token: string, user: string, freeze: boolean) =>
  call(p, agent, token, Token.abi, "setAddressFrozen", { _userAddress: user, _freeze: freeze });

export const pause = (p: PaladinClient, agent: PaladinVerifier, token: string) =>
  call(p, agent, token, Token.abi, "pause", {});

export const unpause = (p: PaladinClient, agent: PaladinVerifier, token: string) =>
  call(p, agent, token, Token.abi, "unpause", {});

// Identity operations

export const registerIdentity = (p: PaladinClient, agent: PaladinVerifier, ir: string, identity: string, user: string, country: number) =>
  call(p, agent, ir, IR.abi, "registerIdentity", { _userAddress: user, _identity: identity, _country: country });

// Queries

export async function balanceOf(p: PaladinClient, from: PaladinVerifier, token: string, account: string): Promise<bigint> {
  const result = await query(p, from, token, Token.abi, "balanceOf", { _userAddress: account });
  return BigInt(result[0]?.toString() ?? "0");
}

export async function isFrozen(p: PaladinClient, from: PaladinVerifier, token: string, user: string): Promise<boolean> {
  const result = await query(p, from, token, Token.abi, "isFrozen", { _userAddress: user });
  return Boolean(result[0]);
}
