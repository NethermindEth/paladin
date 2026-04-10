/*
 * Sepolia testnet configuration.
 * Centralizes network detection, RPC URLs, and poll timeouts.
 */

import { DEFAULT_POLL_TIMEOUT as BASE_POLL, LONG_POLL_TIMEOUT as BASE_LONG_POLL } from "paladin-example-common";
import * as dotenv from "dotenv";
import * as path from "path";

dotenv.config({ path: path.resolve(__dirname, "../.env") });

export const IS_SEPOLIA = !!process.env.ALCHEMY_API_KEY;

export const SEPOLIA_RPC_URL = process.env.ALCHEMY_API_KEY
  ? `https://eth-sepolia.g.alchemy.com/v2/${process.env.ALCHEMY_API_KEY}`
  : undefined;

// Sepolia: 12s blocks + indexer latency. Besu: instant blocks.
export const POLL_TIMEOUT = IS_SEPOLIA ? 120_000 : BASE_POLL;
export const LONG_POLL_TIMEOUT = IS_SEPOLIA ? 300_000 : BASE_LONG_POLL;

const ETHERSCAN = "https://sepolia.etherscan.io";
export const etherscanTx = (hash: string) => `${ETHERSCAN}/tx/${hash}`;
export const etherscanAddr = (addr: string) => `${ETHERSCAN}/address/${addr}`;
