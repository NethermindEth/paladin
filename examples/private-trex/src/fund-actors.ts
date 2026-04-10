/*
 * Fund actor addresses for a demo run on Sepolia.
 *
 * Funder wallet: m/44'/60'/0' (Paladin uses 1-based indices, so 0' never collides).
 * Domain submit keys: m/44'/60'/N'/0/0/0 where N follows actor indices (typically 7+).
 *
 * Usage: ts-node src/fund-actors.ts --config config.json --run-id <runId>
 */

import { utils, Wallet, providers, BigNumber } from "ethers";
import { Pool } from "pg";
import PaladinClient from "@lfdecentralizedtrust/paladin-sdk";
import { getNodeConnections } from "paladin-example-common";
import { SEPOLIA_RPC_URL } from "./sepolia";

const log = (msg: string) => console.log(`  ${msg}`);

export const ACTOR_FUNDING: Record<string, string> = {
  issuer: "0.3", bank: "0.5", regulator: "0.01",
  alice: "0.15", bob: "0.1", charlie: "0.1",
};

/** One actor to fund. `identifier` is the full Paladin lookup (e.g. "bank@node1" or "alice-abc@node1"). */
export interface ActorFundingRequest {
  label: string;
  identifier: string;
  amountEth: string;
}

// Domain submit key funding amount (ETH per key).
// Paladin assigns these keys dynamically as new Zeto contracts are deployed,
// so we query Paladin's Postgres directly to find the current active keys
// rather than brute-forcing a BIP32 index range.
const DOMAIN_KEY_AMOUNT = "0.15";

function getSeedHex(): string {
  const raw = process.env.WALLET_SEED;
  if (!raw) throw new Error("WALLET_SEED not set in .env");
  const hex = raw.replace(/^0x/i, "");
  if (!/^[0-9a-fA-F]{64}$/.test(hex)) {
    throw new Error(`WALLET_SEED must be 64 hex chars, got ${hex.length}`);
  }
  return hex;
}

function deriveWallet(rpcUrl: string, bip32Path: string): Wallet {
  const hd = utils.HDNode.fromSeed("0x" + getSeedHex());
  return new Wallet(hd.derivePath(bip32Path).privateKey, new providers.JsonRpcProvider(rpcUrl));
}

function getFunderWallet(rpcUrl: string): Wallet {
  return deriveWallet(rpcUrl, "m/44'/60'/0'");
}

/** Send ETH to `to` if its balance is below `needed`. Returns true if a tx was sent. */
async function topUpIfNeeded(
  funder: Wallet, to: string, needed: BigNumber, label: string,
): Promise<boolean> {
  const bal = await funder.provider!.getBalance(to);
  if (bal.gte(needed)) {
    log(`${label}: ${utils.formatEther(bal)} ETH — ok`);
    return false;
  }
  const topUp = needed.sub(bal);
  log(`${label}: sending ${utils.formatEther(topUp)} ETH...`);
  const tx = await funder.sendTransaction({ to, value: topUp });
  await tx.wait();
  return true;
}

/**
 * Fund a list of actors. Each entry carries its own Paladin identifier so the
 * caller is free to mix persistent identities (e.g. `bank@node1`) with
 * per-run-scoped ones (e.g. `alice-${runId}@node1`).
 */
export async function fundActors(
  paladin: PaladinClient, rpcUrl: string, actors: ActorFundingRequest[],
): Promise<void> {
  const funder = getFunderWallet(rpcUrl);
  let balance = await funder.getBalance();
  log(`Funder: ${funder.address} (${utils.formatEther(balance)} ETH)`);

  // Bootstrap: move funds from deployer (m/44'/60'/1') to funder if needed.
  if (balance.lt(utils.parseEther("0.5"))) {
    const deployer = deriveWallet(rpcUrl, "m/44'/60'/1'");
    const dBal = await deployer.getBalance();
    if (dBal.gt(utils.parseEther("0.1"))) {
      const gas = (await deployer.getGasPrice()).mul(21000);
      const amt = dBal.sub(gas).sub(utils.parseEther("0.05"));
      if (amt.gt(0)) {
        const tx = await deployer.sendTransaction({ to: funder.address, value: amt });
        await tx.wait();
        log(`Transferred ${utils.formatEther(amt)} ETH from deployer`);
      }
    }
    balance = await funder.getBalance();
  }

  console.log(`\n=== Funding ${actors.length} actors ===\n`);
  for (const a of actors) {
    const addr = await paladin.ptx.resolveVerifier(
      a.identifier, "ecdsa:secp256k1", "eth_address",
    );
    await topUpIfNeeded(funder, addr, utils.parseEther(a.amountEth), `${a.label} (${addr})`);
  }
  console.log(`\n  Funder remaining: ${utils.formatEther(await funder.getBalance())} ETH\n`);
}

/**
 * Convenience builder for the default actor set. Uses persistent identifiers
 * for `issuer/bank/regulator` and run-scoped identifiers for `alice/bob/charlie`.
 */
export function defaultFundingList(nodeId: string, runId: string): ActorFundingRequest[] {
  return [
    { label: "issuer",    identifier: `issuer@${nodeId}`,           amountEth: ACTOR_FUNDING.issuer },
    { label: "bank",      identifier: `bank@${nodeId}`,             amountEth: ACTOR_FUNDING.bank },
    { label: "regulator", identifier: `regulator@${nodeId}`,        amountEth: ACTOR_FUNDING.regulator },
    { label: "alice",     identifier: `alice-${runId}@${nodeId}`,   amountEth: ACTOR_FUNDING.alice },
    { label: "bob",       identifier: `bob-${runId}@${nodeId}`,     amountEth: ACTOR_FUNDING.bob },
    { label: "charlie",   identifier: `charlie-${runId}@${nodeId}`, amountEth: ACTOR_FUNDING.charlie },
  ];
}

/**
 * Best-effort fund Paladin's domain submit key for a specific Zeto contract.
 *
 * Paladin allocates `domains.<zeto-address>.submit.<uuid>` the first time
 * its private transaction manager needs a signer for a non-deposit Zeto
 * tx. The row only lands in `key_verifiers` at that moment, so if setup()
 * calls this function right after the deposit (as it does), the row is
 * usually absent. In that case we log a warning and return — Paladin will
 * allocate and resolve the key lazily on the first real private transfer,
 * and on machines that have leftover ETH at the resulting BIP32 slot from
 * prior sessions, things "just work". On a truly clean machine the first
 * private transfer will hang and require manual intervention; see
 * SUBMIT_KEY_POSTMORTEM.md for the full story and deferred fixes.
 */
export async function fundDomainSubmitKey(rpcUrl: string, zetoAddress: string): Promise<void> {
  const funder = getFunderWallet(rpcUrl);
  const needed = utils.parseEther(DOMAIN_KEY_AMOUNT);
  const pool = new Pool({
    host: process.env.PALADIN_DB_HOST ?? "127.0.0.1",
    port: parseInt(process.env.PALADIN_DB_PORT ?? "5433"),
    user: process.env.PALADIN_DB_USER ?? "postgres",
    password: process.env.PALADIN_DB_PASSWORD ?? "my-secret",
    database: process.env.PALADIN_DB_NAME ?? "paladin_demo",
  });

  try {
    const result = await pool.query(
      `SELECT verifier FROM key_verifiers
       WHERE identifier LIKE $1
       AND algorithm = 'ecdsa:secp256k1'
       AND type = 'eth_address'
       LIMIT 1`,
      [`domains.${zetoAddress.toLowerCase()}%submit%`],
    );

    if (result.rows.length === 0) {
      log(`No domain submit key found for ${zetoAddress} yet`);
      return;
    }

    const row = result.rows[0];
    const addr = row.verifier.startsWith("0x") ? row.verifier : "0x" + row.verifier;
    await topUpIfNeeded(funder, addr, needed, `Domain submit key ${addr}`);
  } finally {
    await pool.end();
  }
}

// CLI entrypoint
if (require.main === module) {
  (async () => {
    const configIdx = process.argv.indexOf("--config");
    const configPath = configIdx >= 0 ? process.argv[configIdx + 1] : undefined;
    const runIdIdx = process.argv.indexOf("--run-id");
    const runId = runIdIdx >= 0 ? process.argv[runIdIdx + 1] : undefined;
    if (!runId) { console.error("Usage: ts-node src/fund-actors.ts --config config.json --run-id <runId>"); process.exit(1); }
    if (!SEPOLIA_RPC_URL) throw new Error("ALCHEMY_API_KEY not set in .env");
    const nc = getNodeConnections(configPath);
    await fundActors(new PaladinClient(nc[0].clientOptions), SEPOLIA_RPC_URL, defaultFundingList(nc[0].id, runId));
  })().catch((err) => { console.error("Funding failed:", err); process.exit(1); });
}
