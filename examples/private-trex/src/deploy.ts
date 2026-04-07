/*
 * One-time contract deployment for private-trex on local Besu (or Sepolia).
 *
 * Deploys:
 *   - Poseidon libraries, SmtLib
 *   - 4 AENKNR-E Groth16 verifiers
 *   - AENKNRECodec, Zeto_AENKNRETransferFacet
 *   - Zeto_AnonEncNullifierKycNonRepudiationEnforced (implementation)
 *   - ZetoFactory (implementation + ERC1967Proxy)
 *   - Registers implementation with factory
 *
 * Uses pre-compiled artifacts from domains/integration-test/helpers/abis/
 * (solc 0.8.27, no viaIR) to avoid the FailedCall() bug.
 *
 * Usage: ts-node src/deploy.ts [--config path/to/config.json]
 */

import PaladinClient, {
  PaladinVerifier,
  TransactionType,
} from "@lfdecentralizedtrust/paladin-sdk";
import { checkReceipt, LONG_POLL_TIMEOUT, getNodeConnections } from "paladin-example-common";
import * as fs from "fs";
import * as path from "path";


const ABIS_DIR = path.resolve(__dirname, "../../../domains/integration-test/helpers/abis");

const ZERO = "0x0000000000000000000000000000000000000000";

const log = (msg: string) => console.log(`  ${msg}`);

// ---------------------------------------------------------------------------
// ABI loading + library linking
// ---------------------------------------------------------------------------

interface Artifact {
  abi: any[];
  bytecode: string;
  linkReferences?: Record<string, Record<string, Array<{ start: number; length: number }>>>;
}

function loadArtifact(name: string): Artifact {
  const raw = fs.readFileSync(path.join(ABIS_DIR, `${name}.json`), "utf-8");
  return JSON.parse(raw);
}

/**
 * Resolve Solidity library link placeholders in bytecode.
 * Format: __$<34-char-keccak-prefix>$__  at each reference offset.
 */
function linkBytecode(
  artifact: Artifact,
  libraries: Record<string, string>,
): string {
  let bytecode = artifact.bytecode;
  if (!artifact.linkReferences) return bytecode;

  for (const [fileName, fileRefs] of Object.entries(artifact.linkReferences)) {
    for (const [libName, refs] of Object.entries(fileRefs)) {
      const fullName = `${fileName}:${libName}`;
      const addr = libraries[fullName] || libraries[libName];
      if (!addr) throw new Error(`Missing library address for ${fullName}`);

      const cleanAddr = addr.toLowerCase().replace("0x", "");
      for (const ref of refs) {
        const start = 2 + ref.start * 2; // skip "0x"
        const end = start + 40; // 20 bytes = 40 hex chars
        bytecode = bytecode.substring(0, start) + cleanAddr + bytecode.substring(end);
      }
    }
  }
  return bytecode;
}

// ---------------------------------------------------------------------------
// Deploy helpers
// ---------------------------------------------------------------------------

async function deployContract(
  paladin: PaladinClient,
  deployer: PaladinVerifier,
  name: string,
  artifact: Artifact,
  libraries: Record<string, string> = {},
  args: Record<string, any> = {},
): Promise<string> {
  const linkedBytecode = linkBytecode(artifact, libraries);

  log(`Deploying ${name}...`);
  const txId = await paladin.ptx.sendTransaction({
    type: TransactionType.PUBLIC,
    from: deployer.lookup,
    data: args,
    function: "",
    abi: artifact.abi,
    bytecode: linkedBytecode,
  });
  const receipt = await paladin.pollForReceipt(txId, LONG_POLL_TIMEOUT);
  if (!checkReceipt(receipt)) {
    throw new Error(`Failed to deploy ${name}: ${(receipt as any)?.failureMessage}`);
  }
  const addr = receipt!.contractAddress!;
  log(`  ${name} => ${addr}`);
  return addr;
}

async function callContract(
  paladin: PaladinClient,
  from: PaladinVerifier,
  to: string,
  abi: any[],
  fn: string,
  data: Record<string, any>,
): Promise<void> {
  const txId = await paladin.ptx.sendTransaction({
    type: TransactionType.PUBLIC,
    from: from.lookup,
    to,
    data,
    function: fn,
    abi,
  });
  const receipt = await paladin.pollForReceipt(txId, LONG_POLL_TIMEOUT);
  if (!checkReceipt(receipt)) {
    throw new Error(`Failed to call ${fn}: ${(receipt as any)?.failureMessage}`);
  }
}

// ---------------------------------------------------------------------------
// Main deployment
// ---------------------------------------------------------------------------

async function main() {
  // Parse --config flag or use default
  const configIdx = process.argv.indexOf("--config");
  const configPath = configIdx >= 0 ? process.argv[configIdx + 1] : undefined;
  const nodeConnections = getNodeConnections(configPath);

  if (nodeConnections.length < 1) {
    console.error("Need at least 1 node in config.json");
    process.exit(1);
  }

  const paladin = new PaladinClient(nodeConnections[0].clientOptions);
  const deployer = paladin.getVerifiers(`deployer@${nodeConnections[0].id}`)[0];

  console.log("\n=== Phase 1: Deploy Zeto AENKNR-E Contracts ===\n");

  // 1. Libraries
  const poseidon2 = await deployContract(paladin, deployer, "PoseidonUnit2L", loadArtifact("Poseidon2"));
  const poseidon3 = await deployContract(paladin, deployer, "PoseidonUnit3L", loadArtifact("Poseidon3"));
  const smtLib = await deployContract(paladin, deployer, "SmtLib", loadArtifact("SmtLib"));

  const libs: Record<string, string> = {
    "PoseidonUnit2L": poseidon2,
    "@iden3/contracts/contracts/lib/Poseidon.sol:PoseidonUnit2L": poseidon2,
    "PoseidonUnit3L": poseidon3,
    "@iden3/contracts/contracts/lib/Poseidon.sol:PoseidonUnit3L": poseidon3,
    "SmtLib": smtLib,
    "@iden3/contracts/contracts/lib/SmtLib.sol:SmtLib": smtLib,
  };

  // 2. Verifiers (no links needed)
  const transferVerifier = await deployContract(
    paladin, deployer,
    "Groth16Verifier_AnonEncNullifierKycNonRepudiationEnforced",
    loadArtifact("Groth16Verifier_AnonEncNullifierKycNonRepudiationEnforced"),
  );
  const depositVerifier = await deployContract(
    paladin, deployer,
    "Groth16Verifier_DepositKycNonRepudiationEnforced",
    loadArtifact("Groth16Verifier_DepositKycNonRepudiationEnforced"),
  );
  const withdrawVerifier = await deployContract(
    paladin, deployer,
    "Groth16Verifier_WithdrawNullifierKycEnforced",
    loadArtifact("Groth16Verifier_WithdrawNullifierKycEnforced"),
  );
  const forcedTransferVerifier = await deployContract(
    paladin, deployer,
    "Groth16Verifier_ForcedTransferNullifierKycEnforced",
    loadArtifact("Groth16Verifier_ForcedTransferNullifierKycEnforced"),
  );

  // 3. Codec and transfer facet (codec has no links, facet needs libraries)
  const codec = await deployContract(
    paladin, deployer, "AENKNRECodec", loadArtifact("AENKNRECodec"),
  );
  const transferFacet = await deployContract(
    paladin, deployer, "Zeto_AENKNRETransferFacet",
    loadArtifact("Zeto_AENKNRETransferFacet"), libs,
  );

  // 4. Implementation (needs libraries)
  const implementation = await deployContract(
    paladin, deployer, "Zeto_AnonEncNullifierKycNonRepudiationEnforced",
    loadArtifact("Zeto_AnonEncNullifierKycNonRepudiationEnforced"), libs,
  );

  // 5. Factory (implementation + ERC1967Proxy)
  console.log("\n=== Phase 2: Deploy ZetoFactory ===\n");

  const factoryImpl = await deployContract(
    paladin, deployer, "ZetoFactory (impl)", loadArtifact("ZetoFactory"),
  );

  // Deploy ERC1967Proxy pointing to factory impl, calling initialize()
  const initSelector = "0x8129fc1c"; // initialize() with no params
  const proxyArtifact = loadArtifact("ERC1967Proxy");
  const factoryProxy = await deployContract(
    paladin, deployer, "ZetoFactory (proxy)",
    proxyArtifact, {}, { implementation: factoryImpl, _data: initSelector },
  );
  log(`Factory proxy: ${factoryProxy}`);

  // 6. Register implementation with factory
  console.log("\n=== Phase 3: Register Implementation ===\n");

  const factoryAbi = loadArtifact("ZetoFactory").abi;
  await callContract(paladin, deployer, factoryProxy, factoryAbi, "registerImplementation", {
    name: "Zeto_AnonEncNullifierKycNonRepudiationEnforced",
    implementation: {
      implementation,
      verifiers: {
        verifier: transferVerifier,
        depositVerifier,
        withdrawVerifier,
        lockVerifier: ZERO,
        burnVerifier: ZERO,
        batchVerifier: ZERO,
        batchWithdrawVerifier: ZERO,
        batchLockVerifier: ZERO,
        batchBurnVerifier: ZERO,
        forcedTransferVerifier,
      },
    },
  });
  log("Registered AENKNR-E implementation with factory");

  // 7. Save deployment data
  const deployData = {
    factoryAddress: factoryProxy,
    contracts: {
      poseidon2, poseidon3, smtLib,
      transferVerifier, depositVerifier, withdrawVerifier, forcedTransferVerifier,
      codec, transferFacet, implementation,
      factoryImpl, factoryProxy,
    },
    deployedAt: new Date().toISOString(),
    chainId: "local-besu",
  };

  const dataDir = path.resolve(__dirname, "../data");
  if (!fs.existsSync(dataDir)) fs.mkdirSync(dataDir, { recursive: true });
  const dataFile = path.join(dataDir, "deploy.json");
  fs.writeFileSync(dataFile, JSON.stringify(deployData, null, 2));

  console.log(`\n=== Deployment Complete ===`);
  console.log(`Factory address: ${factoryProxy}`);
  console.log(`Data saved to: ${dataFile}`);
  console.log(`\nNext: restart Paladin with zeto domain config pointing to ${factoryProxy}\n`);
}

main().catch((err) => {
  console.error("Deployment failed:", err);
  process.exit(1);
});
