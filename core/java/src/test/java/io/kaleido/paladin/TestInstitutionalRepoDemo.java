/*
 * Copyright © 2026 Kaleido, Inc.
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

package io.kaleido.paladin;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.dataformat.yaml.YAMLFactory;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.kaleido.paladin.logging.PaladinLogging;
import io.kaleido.paladin.toolkit.JsonRpcClient;
import org.apache.logging.log4j.Level;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.nio.file.Files;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

// Chapter 18's institutional repo demo: an interbank repurchase agreement settling atomically
// across two real SNoto instances (a bond, and cash shielded from a real test SAC) and a real
// Sente group - three genuinely separate node processes (not Testbed's single-JVM simulation),
// extending TestSenteThreeNodeHarness.java's proven real-process/gRPC/registry machinery
// (factored out into NodeProcessHarness.java) with a second domain (noto, configured twice under
// distinct names/registries - see deploy-stellar-fixtures.sh's own header comment for why bond and
// cash can't share one noto config or one registry).
//
// Roles (ch.18 §18.4): node1 hosts the "registrar" (bond notary) and "cashNotary" identities -
// infrastructure roles, not a trading counterparty. node2 is Bank A, node3 is Bank B.
//
// Near/far-leg settlement reads prepareUnlock's own domain receipt (lockInfo.unlockParams.args) to
// get Sente's externalCalls args, rather than reconstructing state IDs by hand - the same
// "opaque, pre-built call" pattern EVM's own atom_helper.go/pvp_test.go already use via
// ReceiptLockInfo.UnlockCall, just JSON-shaped (scval_json.rs) instead of ABI-encoded, since
// Sente's own transition() takes JSON-typed args, not raw callData.
public class TestInstitutionalRepoDemo {

    private static final String[] NODE_NAMES = {"node1", "node2", "node3"};
    private static final int REGISTRAR_NODE = 0;
    private static final int BANK_A_NODE = 1;
    private static final int BANK_B_NODE = 2;

    @JsonIgnoreProperties(ignoreUnknown = true)
    private record StellarFixtures(
            @JsonProperty String saladinFactoryAddress,
            @JsonProperty String notoSaladinFactoryAddress,
            @JsonProperty String cashNotoSaladinFactoryAddress,
            @JsonProperty String snotoFactoryAddress,
            @JsonProperty String snotoWasmHash,
            @JsonProperty String senteFactoryAddress,
            @JsonProperty String senteWasmHash,
            @JsonProperty String testUsdcSacAddress,
            @JsonProperty String testUsdcIssuerAddress
    ) {
    }

    private static StellarFixtures loadStellarFixtures() throws Exception {
        File f = new File("../../soroban/artifacts/stellar-fixtures.json");
        if (!f.exists()) {
            fail("run ./soroban/scripts/deploy-stellar-fixtures.sh first - " + f.getAbsolutePath() + " not found");
        }
        StellarFixtures fixtures = new ObjectMapper().readValue(f, StellarFixtures.class);
        assertNotNull(fixtures.saladinFactoryAddress());
        assertNotNull(fixtures.notoSaladinFactoryAddress());
        assertNotNull(fixtures.cashNotoSaladinFactoryAddress());
        assertNotNull(fixtures.snotoFactoryAddress());
        assertNotNull(fixtures.senteFactoryAddress());
        assertNotNull(fixtures.testUsdcSacAddress());
        assertNotNull(fixtures.testUsdcIssuerAddress());
        return fixtures;
    }

    private static final String JNA_LIBRARY_PATH = NodeProcessHarness.jnaLibraryPath(
            new File("../.."), "core/go/build/libs", "toolkit/go/build/libs",
            "domains/sente/target/release", "domains/noto/build/libs",
            "transports/grpc/build/libs", "registries/static/build/libs");

    private static String networkPassphrase() {
        return System.getProperty("paladin.test.stellar.networkPassphrase", "Standalone Network ; February 2017");
    }

    private static String rpcUrl() {
        return System.getProperty("paladin.test.stellar.rpcUrl", "http://localhost:8000/soroban/rpc");
    }

    // The `stellar` CLI network alias to pass to invokeStellarCli - matches deploy-stellar-
    // fixtures.sh's own STELLAR_FIXTURE_NETWORK convention exactly (that script registers a local
    // "stellar-quickstart-local" alias via `stellar network add` when unset, or targets the CLI's
    // built-in "testnet" alias when STELLAR_FIXTURE_NETWORK=testnet - the same distinction
    // repo-demo.sh needs to pass through here too, not just to rpcUrl/friendbotUrl/networkPassphrase
    // above). Left hardcoded to "stellar-quickstart-local" for a long time since only quickstart
    // runs ever exercised this path - a real testnet run fails immediately with "Contract not
    // found" once this diverges from the fixtures' actual deploy network.
    private static String stellarCliNetwork() {
        return System.getProperty("paladin.test.stellar.network", "stellar-quickstart-local");
    }

    private static String friendbotUrl() {
        return System.getProperty("paladin.test.stellar.friendbotUrl", "http://localhost:8000/friendbot");
    }

    // Controls this test JVM's own PaladinLogging verbosity (the "TRACE JAVA: --> RPC[...]"/
    // "DEBUG JAVA: ..." lines JsonRpcClient emits for every request/response) - separate from each
    // spawned node's own engine log (buildNodeConfig's `log.level`, written to its own file, not
    // stdout). Defaults to "info" (only this test's own === narration and the real on-chain event
    // dumps printed below) rather than the noisier level a live audience doesn't need to see;
    // repo-demo.sh's --log-level flag raises it back to "debug"/"trace" for troubleshooting a run.
    private static String logLevel() {
        return System.getProperty("paladin.demo.logLevel", "info");
    }

    // Builds one node's full engine-mode config: the same base (db/rpcServer/wallets/baseLedger/
    // sequencerManager) plus transports.grpc/registries.registry1 TestSenteThreeNodeHarness.java's
    // own buildNodeConfig already establishes, PLUS three domains - noto-bond, noto-cash, sente -
    // configured identically on all 3 nodes (per-node identities are what actually differ at
    // runtime, not domain wiring).
    private static String buildNodeConfig(
            int index, int rpcPort, int[] grpcPorts, NodeProcessHarness.NodeCert[] certs, File workDir, StellarFixtures fixtures
    ) throws Exception {
        String base = """
                nodeName: %s
                db:
                  type: sqlite
                  sqlite:
                    dsn: "%s?_busy_timeout=5000&_journal_mode=WAL"
                    maxOpenConns: 4
                    autoMigrate: true
                    migrationsDir: %s
                    debugQueries: false
                rpcServer:
                  http:
                    port: %d
                    shutdownTimeout: 0s
                  ws:
                    disabled: true
                    shutdownTimeout: 0s
                grpc:
                  shutdownTimeout: 0s
                blockIndexer:
                  fromBlock: latest
                loader:
                  debug: true
                log:
                  level: debug
                  output: file
                  file:
                    filename: %s
                wallets:
                  - name: root
                    keySelector: "^root$"
                    signer:
                      keyDerivation:
                        type: "bip32"
                        bip44HardenedSegments: 20
                      keyStore:
                        type: "static"
                        static:
                          keys:
                            seed:
                              encoding: hex
                              inline: baefd734b8d3e48472cff83912375fedbc7573701912fe308af730180f97d74a
                  - name: wallet1
                    keySelector: .*
                    signer:
                      keyDerivation:
                        type: "bip32"
                        bip44HardenedSegments: 20
                      keyStore:
                        type: "static"
                        static:
                          keys:
                            seed:
                              encoding: hex
                              inline: %s
                baseLedger:
                  type: stellar
                  stellar:
                    url: %s
                    networkPassphrase: "%s"
                    ingestor:
                      pollInterval: "%s"
                      insertDBBatchSize: 100
                    channelAccounts:
                      poolSize: %s
                      funder: root
                      startingBalance: "%s"
                publicTxManager:
                  gasPrice:
                    fixedGasPrice:
                      maxFeePerGas: "0x0"
                      maxPriorityFeePerGas: "0x0"
                sequencerManager:
                  heartbeatInterval: "1s"
                  inactiveGracePeriod: 2
                  requestTimeout: "1s"
                  stateTimeout: "1s"
                """.formatted(
                NODE_NAMES[index],
                new File(workDir, NODE_NAMES[index] + ".sqlite").getAbsolutePath(),
                new File("../go/db/migrations/sqlite").getAbsolutePath(),
                rpcPort,
                new File(workDir, NODE_NAMES[index] + ".log").getAbsolutePath(),
                // Each node needs its own distinct wallet1 seed, same reasoning as
                // TestSenteThreeNodeHarness.java's own wallet1SeedForNode doc comment: an identical
                // seed across independent per-node key_paths tables would derive member1/2/3 (here,
                // registrar/bankA/bankB) to the SAME underlying key.
                "cdd8dbc37a9fa235a3c56367bb029c27a1bdf49b8090070d1b22993f343e09" + String.format("%02x", index),
                rpcUrl(),
                networkPassphrase(),
                System.getProperty("paladin.test.stellar.pollInterval", "1s"),
                System.getProperty("paladin.test.stellar.channelAccountPoolSize", "8"),
                System.getProperty("paladin.test.stellar.channelAccountStartingBalance", "5")
        );

        ObjectMapper yamlMapper = new ObjectMapper(YAMLFactory.builder().build());
        Map<String, Object> configMap = yamlMapper.readValue(base, new TypeReference<>() {
        });

        Map<String, Object> notoPlugin = new HashMap<>() {{
            put("type", "c-shared");
            put("library", "noto");
        }};
        Map<String, Object> notoBondConfig = new HashMap<>() {{
            put("stellarSnotoFactoryAddress", fixtures.snotoFactoryAddress());
            put("stellarSnotoWasmHash", fixtures.snotoWasmHash());
        }};
        Map<String, Object> notoBondDomain = new HashMap<>() {{
            put("registryAddress", fixtures.notoSaladinFactoryAddress());
            put("plugin", notoPlugin);
            put("config", notoBondConfig);
        }};
        Map<String, Object> notoCashConfig = new HashMap<>() {{
            put("stellarSnotoFactoryAddress", fixtures.snotoFactoryAddress());
            put("stellarSnotoWasmHash", fixtures.snotoWasmHash());
            put("stellarSacAddress", fixtures.testUsdcSacAddress());
        }};
        Map<String, Object> notoCashDomain = new HashMap<>() {{
            put("registryAddress", fixtures.cashNotoSaladinFactoryAddress());
            put("plugin", notoPlugin);
            put("config", notoCashConfig);
        }};
        Map<String, Object> sentePlugin = new HashMap<>() {{
            put("type", "c-shared");
            put("library", "sente");
        }};
        Map<String, Object> senteConfig = new HashMap<>() {{
            put("senteFactoryAddress", fixtures.senteFactoryAddress());
            put("saladinFactoryAddress", fixtures.saladinFactoryAddress());
            put("senteWasmHash", fixtures.senteWasmHash());
            put("networkPassphrase", networkPassphrase());
        }};
        Map<String, Object> senteDomain = new HashMap<>() {{
            put("registryAddress", fixtures.saladinFactoryAddress());
            put("plugin", sentePlugin);
            put("config", senteConfig);
        }};
        configMap.put("domains", Map.of("noto-bond", notoBondDomain, "noto-cash", notoCashDomain, "sente", senteDomain));

        Map<String, Object> transportPlugin = new HashMap<>() {{
            put("type", "c-shared");
            put("library", "grpc");
        }};
        Map<String, Object> tlsConfig = new HashMap<>() {{
            put("enabled", true);
            put("certFile", certs[index].certPath());
            put("keyFile", certs[index].keyPath());
        }};
        Map<String, Object> transportConfig = new HashMap<>() {{
            put("address", "0.0.0.0");
            put("port", grpcPorts[index]);
            put("directCertVerification", true);
            put("tls", tlsConfig);
        }};
        Map<String, Object> grpcTransport = new HashMap<>() {{
            put("plugin", transportPlugin);
            put("config", transportConfig);
        }};
        configMap.put("transports", Map.of("grpc", grpcTransport));

        ObjectMapper jsonMapper = new ObjectMapper();
        Map<String, Object> registryEntries = new HashMap<>();
        for (int peer = 0; peer < NODE_NAMES.length; peer++) {
            if (peer == index) continue;
            String transportDetailsJson = jsonMapper.writeValueAsString(Map.of(
                    "endpoint", "dns:///127.0.0.1:" + grpcPorts[peer],
                    "issuers", certs[peer].certPem()
            ));
            Map<String, Object> properties = Map.of("transport.grpc", transportDetailsJson);
            registryEntries.put(NODE_NAMES[peer], Map.of("properties", properties));
        }
        Map<String, Object> staticPlugin = new HashMap<>() {{
            put("type", "c-shared");
            put("library", "static");
        }};
        Map<String, Object> registryConfig = Map.of("entries", registryEntries);
        Map<String, Object> registry1 = new HashMap<>() {{
            put("plugin", staticPlugin);
            put("config", registryConfig);
        }};
        configMap.put("registries", Map.of("registry1", registry1));

        return yamlMapper.writerWithDefaultPrettyPrinter().writeValueAsString(configMap);
    }

    // --- ABIs (mirroring domains/noto/pkg/types/abis/INotoPrivate.json's canonical shapes exactly,
    // since this harness submits raw ptx_sendTransaction requests, not the Go SDK's fluent builder) ---

    private static List<Map<String, Object>> constructorABI() {
        return List.of(Map.of(
                "type", "constructor",
                "inputs", List.of(
                        Map.of("name", "notary", "type", "string"),
                        Map.of("name", "notaryMode", "type", "string"))
        ));
    }

    private static List<Map<String, Object>> mintABI() {
        return List.of(Map.of(
                "type", "function", "name", "mint",
                "inputs", List.of(
                        Map.of("name", "to", "type", "string"),
                        Map.of("name", "amount", "type", "uint256"),
                        Map.of("name", "data", "type", "bytes")),
                "outputs", List.of()
        ));
    }

    private static List<Map<String, Object>> lockABI() {
        return List.of(Map.of(
                "type", "function", "name", "lock",
                "inputs", List.of(
                        Map.of("name", "amount", "type", "uint256"),
                        Map.of("name", "data", "type", "bytes")),
                "outputs", List.of()
        ));
    }

    private static List<Map<String, Object>> prepareUnlockABI() {
        return List.of(Map.of(
                "type", "function", "name", "prepareUnlock",
                "inputs", List.of(
                        Map.of("name", "lockId", "type", "bytes32"),
                        Map.of("name", "from", "type", "string"),
                        Map.of("name", "recipients", "type", "tuple[]", "internalType", "struct INotoPrivate.UnlockRecipient[]", "components", List.of(
                                Map.of("name", "to", "type", "string"),
                                Map.of("name", "amount", "type", "uint256"))),
                        Map.of("name", "unlockData", "type", "bytes"),
                        Map.of("name", "data", "type", "bytes")),
                "outputs", List.of()
        ));
    }

    private static List<Map<String, Object>> delegateLockABI() {
        return List.of(Map.of(
                "type", "function", "name", "delegateLock",
                "inputs", List.of(
                        Map.of("name", "lockId", "type", "bytes32"),
                        Map.of("name", "delegate", "type", "string"),
                        Map.of("name", "data", "type", "bytes")),
                "outputs", List.of()
        ));
    }

    private static List<Map<String, Object>> depositABI() {
        return List.of(Map.of(
                "type", "function", "name", "deposit",
                "inputs", List.of(
                        Map.of("name", "from", "type", "string"),
                        Map.of("name", "amount", "type", "uint256"),
                        Map.of("name", "data", "type", "bytes")),
                "outputs", List.of()
        ));
    }

    private static List<Map<String, Object>> withdrawABI() {
        return List.of(Map.of(
                "type", "function", "name", "withdraw",
                "inputs", List.of(
                        Map.of("name", "recipient", "type", "string"),
                        Map.of("name", "amount", "type", "uint256"),
                        Map.of("name", "data", "type", "bytes")),
                "outputs", List.of()
        ));
    }

    private static final int POLL_ITERATIONS = Integer.parseInt(System.getProperty("paladin.test.stellar.pollIterations", "180"));

    // Submits a private ptx_sendTransaction and waits for a successful receipt - returns the
    // transaction ID. `to` is null for a constructor deploy (domain required in that case only -
    // TransactionBase's own doc comment: domain is "inferred from 'to' for invoke").
    private String submitAndWait(JsonRpcClient client, int rpcPort, String domain, String to, String from,
                                  List<Map<String, Object>> abi, String function, Map<String, Object> data) throws Exception {
        Map<String, Object> txInput = new HashMap<>();
        txInput.put("type", "private");
        // domain is only needed for a constructor deploy (no `to`) - inferred from `to` for an
        // invoke (TransactionBase's own doc comment); omit it there to match the exact shape
        // core/go/noderuntests/componenttest/stellar_component_test.go's own working ptx_sendTransaction
        // invoke calls use.
        if (to == null && domain != null) txInput.put("domain", domain);
        if (to != null) txInput.put("to", to);
        txInput.put("from", from);
        txInput.put("abi", abi);
        if (function != null) txInput.put("function", function);
        txInput.put("data", data);

        String txID = NodeProcessHarness.rawRequestWithTimeout(rpcPort, "ptx_sendTransaction", txInput, 120, String.class);
        assertNotNull(txID);
        waitForSuccess(client, txID);
        return txID;
    }

    private void waitForSuccess(JsonRpcClient client, String txID) throws Exception {
        Boolean succeeded = null;
        Map<?, ?> receipt = null;
        for (int i = 0; i < POLL_ITERATIONS && succeeded == null; i++) {
            receipt = client.request("ptx_getTransactionReceipt", txID);
            if (receipt != null) {
                succeeded = (Boolean) receipt.get("success");
            } else {
                Thread.sleep(500);
            }
        }
        assertNotNull(succeeded, "transaction " + txID + " did not receive a receipt within timeout");
        assertTrue(succeeded, "transaction " + txID + " failed: " + receipt);
    }

    // domainReceipt is only ever populated once the domain plugin's own BuildReceipt gRPC call
    // completes - observed live to lag slightly behind ptx_getTransactionReceipt's own
    // success:true (a separate, later stage of receipt finalization), so this polls rather than
    // fetching once immediately after waitForSuccess.
    @SuppressWarnings("unchecked")
    private Map<String, Object> getDomainReceipt(JsonRpcClient client, String txID) throws Exception {
        for (int i = 0; i < POLL_ITERATIONS; i++) {
            Map<?, ?> receiptFull = client.request("ptx_getTransactionReceiptFull", txID);
            Object domainReceiptRaw = receiptFull != null ? receiptFull.get("domainReceipt") : null;
            if (domainReceiptRaw instanceof String s) {
                return new ObjectMapper().readValue(s, new TypeReference<>() {
                });
            }
            if (domainReceiptRaw instanceof Map) {
                return (Map<String, Object>) domainReceiptRaw;
            }
            Thread.sleep(500);
        }
        fail("no domainReceipt for " + txID + " within timeout");
        return null;
    }

    private String transactionHash(JsonRpcClient client, String txID) throws Exception {
        Map<?, ?> receipt = client.request("ptx_getTransactionReceipt", txID);
        Object hash = receipt != null ? receipt.get("transactionHash") : null;
        return hash != null ? hash.toString() : null;
    }

    private void narrate(JsonRpcClient client, String step, String txID) throws Exception {
        String hash = transactionHash(client, txID);
        System.out.println("=== " + step + " ===");
        System.out.println("  tx: " + txID);
        if (hash != null) {
            // receipt.transactionHash is pldtypes.Bytes32, shared code used identically for both
            // EVM and Stellar chain submitters (core/go/internal/publictxmgr) - its JSON
            // marshaling always adds a "0x" prefix, correct for EVM's own convention but not
            // Stellar's: stellar.expert/Horizon/Laboratory all key transactions by the bare
            // 64-hex-char hash with no prefix, so a "0x"-prefixed URL 404s ("nothing found") even
            // though the trailing 64 characters are the genuine, correct hash. Stripping it here
            // (not in the shared Bytes32 type, which EVM callers still need "0x" from) is a
            // display-only concern specific to this Stellar-only demo's own explorer links.
            String bareHash = hash.startsWith("0x") ? hash.substring(2) : hash;
            // stellar.expert has no public explorer for a private local quickstart chain - only
            // print a link that can actually resolve.
            if ("testnet".equals(stellarCliNetwork())) {
                System.out.println("  https://stellar.expert/explorer/testnet/tx/" + bareHash);
            }
            printOnChainEvents(bareHash);
        }
    }

    // Ch.18 §18.3's whole disclosure narrative ("here is precisely what's public vs private") is
    // only as convincing as the evidence backing it - printing our own narration string is not
    // proof of what's actually on the public ledger. This fetches and prints the REAL, independently
    // re-derivable event log for this transaction, decoded by the `stellar` CLI itself (not by this
    // demo's own code) so a skeptical viewer isn't just trusting the demo's word for it. Genuinely
    // no on-chain interpretation happens here beyond what `stellar tx fetch events` already decodes
    // (contract address, event name, and each topic/data field's own type/value) - exactly what
    // ch.18 §18.3 means by "opaque state IDs, never what that state represents".
    private void printOnChainEvents(String txHash) {
        try {
            Process p = new ProcessBuilder("stellar", "-q", "tx", "fetch", "events",
                    "--hash", txHash, "--network", stellarCliNetwork(), "--output", "json")
                    .redirectErrorStream(true)
                    .start();
            String output = new String(p.getInputStream().readAllBytes());
            boolean finished = p.waitFor(30, java.util.concurrent.TimeUnit.SECONDS);
            if (!finished || p.exitValue() != 0) {
                System.out.println("  (on-chain events unavailable: " + output.trim() + ")");
                return;
            }
            Map<?, ?> parsed = new ObjectMapper().readValue(output, Map.class);
            List<?> perOperationEvents = (List<?>) parsed.get("contract_events");
            if (perOperationEvents == null || perOperationEvents.isEmpty()) {
                return;
            }
            System.out.println("  Published on-chain (decoded by `stellar tx fetch events`, not by this demo):");
            for (Object operationEvents : perOperationEvents) {
                for (Object eventObj : (List<?>) operationEvents) {
                    Map<?, ?> event = (Map<?, ?>) eventObj;
                    Map<?, ?> body = (Map<?, ?>) ((Map<?, ?>) event.get("body")).get("v0");
                    List<?> topics = (List<?>) body.get("topics");
                    String eventName = topics.isEmpty() ? "?" : describeScVal(topics.get(0));
                    List<String> topicFields = new java.util.ArrayList<>();
                    for (int i = 1; i < topics.size(); i++) {
                        topicFields.add(describeScVal(topics.get(i)));
                    }
                    String dataStr = describeScVal(body.get("data"));
                    String contractId = String.valueOf(event.get("contract_id"));
                    System.out.println("    " + contractId + " " + eventName
                            + (topicFields.isEmpty() ? "" : "(" + String.join(", ", topicFields) + ")")
                            + " -> " + dataStr);
                }
            }
        } catch (Exception e) {
            System.out.println("  (on-chain events unavailable: " + e.getMessage() + ")");
        }
    }

    // Positional, generic decode of one `stellar tx fetch events --output json` ScVal node - no
    // per-event-type field labels (this demo doesn't hardcode Lock/Transition/etc.'s own schema),
    // deliberately: printing exactly what a generic viewer of the chain would see, not a narrated
    // reinterpretation of it. Field order still matches each contract's own `#[contractevent]`
    // declaration (topics first, then `data_format = "vec"` fields in declaration order) - see
    // soroban/contracts/{snoto,sente,factory}/src/lib.rs for what each position actually means.
    @SuppressWarnings("unchecked")
    private String describeScVal(Object v) {
        if (!(v instanceof Map)) {
            return String.valueOf(v);
        }
        Map<String, Object> m = (Map<String, Object>) v;
        if (m.containsKey("symbol")) return String.valueOf(m.get("symbol"));
        if (m.containsKey("bytes")) return "0x" + m.get("bytes");
        if (m.containsKey("address")) return String.valueOf(m.get("address"));
        if (m.containsKey("string")) return "\"" + m.get("string") + "\"";
        if (m.containsKey("vec")) {
            List<?> vec = (List<?>) m.get("vec");
            List<String> parts = new java.util.ArrayList<>();
            for (Object el : vec) {
                parts.add(describeScVal(el));
            }
            return "[" + String.join(", ", parts) + "]";
        }
        // u32/i128/bool/void/etc. - print whatever scalar value the CLI already decoded, unlabeled.
        return m.values().stream().findFirst().map(String::valueOf).orElse("?");
    }

    // Off by default (paladin.demo.interactive=false) so automated/CI runs of this test never
    // block - repo-demo.sh turns it on for a live human-paced walkthrough between legs.
    //
    // This test's stdin is NOT the presenter's terminal: Gradle's `Test` task (unlike `Exec`/
    // `JavaExec`) has no standardInput property at all - there is no supported way to forward real
    // terminal input into this forked JVM. So the pause is a file-signal handoff instead:
    // repo-demo.sh (running in the foreground with real terminal access) passes a fresh empty
    // directory via paladin.demo.pauseDir; this method drops a "waiting" marker there and polls for
    // a "continue" marker, while the script's own background watcher (reading from /dev/tty) prints
    // the actual prompt and creates it once the presenter hits Enter.
    private static final boolean INTERACTIVE = Boolean.parseBoolean(System.getProperty("paladin.demo.interactive", "false"));
    private static final long PAUSE_TIMEOUT_MS = 600_000; // safety net so an unattended run can't hang forever

    private void pauseForDemo(String message) throws Exception {
        if (!INTERACTIVE) {
            return;
        }
        System.out.println();
        System.out.println(">>> " + message + " <<<");
        String pauseDirProp = System.getProperty("paladin.demo.pauseDir");
        if (pauseDirProp == null) {
            System.out.println("(paladin.demo.pauseDir not set - continuing without a real pause)");
            return;
        }
        File pauseDir = new File(pauseDirProp);
        File waiting = new File(pauseDir, "waiting");
        File cont = new File(pauseDir, "continue");
        cont.delete();
        Files.writeString(waiting.toPath(), message);
        long deadline = System.currentTimeMillis() + PAUSE_TIMEOUT_MS;
        while (!cont.exists() && System.currentTimeMillis() < deadline) {
            Thread.sleep(300);
        }
        cont.delete();
        waiting.delete();
    }

    private String deployNoto(JsonRpcClient client, int rpcPort, String domain, String from, String notaryLookup) throws Exception {
        Map<String, Object> params = Map.of("notary", notaryLookup, "notaryMode", "basic");
        String txID = submitAndWait(client, rpcPort, domain, null, from, constructorABI(), null, params);
        Map<?, ?> receipt = client.request("ptx_getTransactionReceipt", txID);
        String address = (String) receipt.get("contractAddress");
        assertNotNull(address, "no contractAddress on deploy receipt for domain " + domain);
        narrate(client, "Deployed " + domain + " (notary=" + notaryLookup + ")", txID);
        return address;
    }

    // A "lock" call's own Assemble (handler_lock.go) creates a lockInfoSchemaV1 output state
    // alongside the lockedCoin, but that lockInfo state can take longer to be confirmed/synced
    // than the lockedCoin state - both appear in the transaction's manifest, but BuildReceipt only
    // gets called once the underlying on-chain confirmation completes, and has been observed live
    // to run before the lockInfo state's own confirmation lands, leaving domainReceipt.lockInfo
    // absent even though domainReceipt itself is already populated. prepareUnlock's own Assemble
    // (loadLockInfoV1) depends on that same lockInfo state existing (PD200028 "Lock ID not found"
    // otherwise), so this polls specifically for lockInfo's own appearance - not just for
    // domainReceipt's - before treating the lock as ready to build on.
    @SuppressWarnings("unchecked")
    private String lockLockId(JsonRpcClient client, String txID) throws Exception {
        Map<String, Object> domainReceipt = null;
        for (int i = 0; i < POLL_ITERATIONS; i++) {
            domainReceipt = getDomainReceipt(client, txID);
            if (domainReceipt.get("lockInfo") != null) {
                break;
            }
            Thread.sleep(500);
        }
        assertNotNull(domainReceipt.get("lockInfo"), "no lockInfo on lock receipt " + txID + " within timeout");
        Map<String, Object> states = (Map<String, Object>) domainReceipt.get("states");
        assertNotNull(states, "no states on lock receipt " + txID);
        List<Object> lockedOutputs = (List<Object>) states.get("lockedOutputs");
        assertNotNull(lockedOutputs, "no lockedOutputs on lock receipt " + txID);
        assertEquals(1, lockedOutputs.size(), "expected exactly one lockedOutput on lock receipt " + txID);
        Map<String, Object> lockedOutput = (Map<String, Object>) lockedOutputs.get(0);
        Map<String, Object> data = (Map<String, Object>) lockedOutput.get("data");
        String lockId = (String) data.get("lockId");
        assertNotNull(lockId);
        return lockId;
    }

    @SuppressWarnings("unchecked")
    private List<Object> prepareUnlockArgs(JsonRpcClient client, String txID, boolean cancel) throws Exception {
        Map<String, Object> domainReceipt = getDomainReceipt(client, txID);
        Map<String, Object> lockInfo = (Map<String, Object>) domainReceipt.get("lockInfo");
        assertNotNull(lockInfo, "no lockInfo on prepareUnlock receipt " + txID);
        Map<String, Object> params = (Map<String, Object>) lockInfo.get(cancel ? "cancelParams" : "unlockParams");
        assertNotNull(params, "no " + (cancel ? "cancelParams" : "unlockParams") + " on prepareUnlock receipt " + txID);
        List<Object> args = (List<Object>) params.get("args");
        assertNotNull(args);
        return args;
    }

    // One repo leg's full lock->prepareUnlock->delegateLock sequence on a single SNoto instance -
    // called twice per transition (bond and cash), near leg and far leg, symmetric each time.
    // Returns the "unlock" externalCalls args (Sente-JSON-shaped) ready to drop into a transition.
    private List<Object> lockPrepareAndDelegate(
            JsonRpcClient[] clients, int[] rpcPorts, String domain, String snotoAddress,
            int ownerNode, String ownerIdentity,
            String counterpartyLocator, String amount, String senteGroupAddress
    ) throws Exception {
        String lockTx = submitAndWait(clients[ownerNode], rpcPorts[ownerNode], domain, snotoAddress, ownerIdentity,
                lockABI(), "lock", Map.of("amount", amount, "data", "0x"));
        narrate(clients[ownerNode], domain + ": " + ownerIdentity + " locks " + amount, lockTx);
        String lockId = lockLockId(clients[ownerNode], lockTx);

        Map<String, Object> prepareParams = new HashMap<>();
        prepareParams.put("lockId", lockId);
        prepareParams.put("from", ownerIdentity);
        prepareParams.put("recipients", List.of(Map.of("to", counterpartyLocator, "amount", amount)));
        prepareParams.put("unlockData", "0x");
        prepareParams.put("data", "0x");
        // checkAllowed (handler_unlock_common.go) requires prepareUnlock's own transaction
        // submitter to match its "from" param (the lock owner) in basic notary mode - the notary
        // still endorses it via Paladin's own endorsement flow regardless of who submits.
        String prepareTx = submitAndWait(clients[ownerNode], rpcPorts[ownerNode], domain, snotoAddress, ownerIdentity,
                prepareUnlockABI(), "prepareUnlock", prepareParams);
        narrate(clients[ownerNode], domain + ": " + ownerIdentity + " prepares unlock for " + counterpartyLocator, prepareTx);
        List<Object> unlockArgs = prepareUnlockArgs(clients[ownerNode], prepareTx, false);

        // delegateLock's own Endorse (handler_delegate_lock.go) resolves tx.Transaction.From as the
        // "sender" and requires it to match the lock's own owner (validateV1LockTransition) - so,
        // like prepareUnlock, it must be submitted by the owner, not the notary.
        String delegateTx = submitAndWait(clients[ownerNode], rpcPorts[ownerNode], domain, snotoAddress, ownerIdentity,
                delegateLockABI(), "delegateLock", Map.of("lockId", lockId, "delegate", senteGroupAddress, "data", "0x"));
        narrate(clients[ownerNode], domain + ": " + ownerIdentity + " delegates lock to Sente group", delegateTx);

        return unlockArgs;
    }

    private String submitTransition(JsonRpcClient client, int rpcPort, String groupID, String fromMember, List<Map<String, Object>> externalCalls) throws Exception {
        String externalCallsJson = new ObjectMapper().writeValueAsString(externalCalls);
        Map<String, Object> transitionFunction = Map.of(
                "type", "function", "name", "transition",
                "inputs", List.of(Map.of("name", "externalCalls", "type", "string")),
                "outputs", List.of()
        );
        String txID = NodeProcessHarness.rawRequestWithTimeout(rpcPort, "pgroup_sendTransaction", Map.of(
                "domain", "sente",
                "group", groupID,
                "from", fromMember,
                "function", transitionFunction,
                "input", Map.of("externalCalls", externalCallsJson)
        ), 120, String.class);
        assertNotNull(txID);
        waitForSuccess(client, txID);
        return txID;
    }

    @Test
    void interbankRepoSettlesAtomically() throws Exception {
        PaladinLogging.setLevel(Level.valueOf(logLevel().toUpperCase()));
        StellarFixtures fixtures = loadStellarFixtures();
        File workDir = Files.createTempDirectory("repo-demo").toFile();

        int[] rpcPorts = new int[NODE_NAMES.length];
        int[] grpcPorts = new int[NODE_NAMES.length];
        NodeProcessHarness.NodeCert[] certs = new NodeProcessHarness.NodeCert[NODE_NAMES.length];
        for (int i = 0; i < NODE_NAMES.length; i++) {
            rpcPorts[i] = NodeProcessHarness.freePort();
            grpcPorts[i] = NodeProcessHarness.freePort();
            certs[i] = NodeProcessHarness.generateCert(workDir, NODE_NAMES[i]);
        }

        Process[] processes = new Process[NODE_NAMES.length];
        JsonRpcClient[] clients = new JsonRpcClient[NODE_NAMES.length];
        try {
            for (int i = 0; i < NODE_NAMES.length; i++) {
                String yaml = buildNodeConfig(i, rpcPorts[i], grpcPorts, certs, workDir, fixtures);
                Files.writeString(new File(workDir, NODE_NAMES[i] + ".yaml").toPath(), yaml);
            }

            processes[REGISTRAR_NODE] = NodeProcessHarness.launchNode(new File(workDir, NODE_NAMES[REGISTRAR_NODE] + ".yaml"), new File(workDir, NODE_NAMES[REGISTRAR_NODE] + ".engine.log"), JNA_LIBRARY_PATH);
            clients[REGISTRAR_NODE] = NodeProcessHarness.waitForReady(rpcPorts[REGISTRAR_NODE], processes[REGISTRAR_NODE], 30_000, 3);
            NodeProcessHarness.resolveAndFundVerifier(clients[REGISTRAR_NODE], "root");
            NodeProcessHarness.resolveAndFundVerifier(clients[REGISTRAR_NODE], "registrar");
            NodeProcessHarness.resolveAndFundVerifier(clients[REGISTRAR_NODE], "cashNotary");

            for (int i = 1; i < NODE_NAMES.length; i++) {
                processes[i] = NodeProcessHarness.launchNode(new File(workDir, NODE_NAMES[i] + ".yaml"), new File(workDir, NODE_NAMES[i] + ".engine.log"), JNA_LIBRARY_PATH);
            }
            clients[BANK_A_NODE] = NodeProcessHarness.waitForReady(rpcPorts[BANK_A_NODE], processes[BANK_A_NODE], 30_000, 3);
            NodeProcessHarness.resolveAndFundVerifier(clients[BANK_A_NODE], "root");
            String bankAAddress = NodeProcessHarness.resolveAndFundVerifier(clients[BANK_A_NODE], "bankA");

            clients[BANK_B_NODE] = NodeProcessHarness.waitForReady(rpcPorts[BANK_B_NODE], processes[BANK_B_NODE], 30_000, 3);
            NodeProcessHarness.resolveAndFundVerifier(clients[BANK_B_NODE], "root");
            String bankBAddress = NodeProcessHarness.resolveAndFundVerifier(clients[BANK_B_NODE], "bankB");

            // --- Setup (ch.18 §18.5) ---
            String bondAddress = deployNoto(clients[REGISTRAR_NODE], rpcPorts[REGISTRAR_NODE], "noto-bond", "registrar", "registrar@node1");
            String cashAddress = deployNoto(clients[REGISTRAR_NODE], rpcPorts[REGISTRAR_NODE], "noto-cash", "cashNotary", "cashNotary@node1");

            String bondAmount = System.getProperty("paladin.demo.bondAmount", "1000000");
            String cashAmount = System.getProperty("paladin.demo.cashAmount", "500000");

            String mintTx = submitAndWait(clients[REGISTRAR_NODE], rpcPorts[REGISTRAR_NODE], "noto-bond", bondAddress, "registrar",
                    mintABI(), "mint", Map.of("to", "bankA@node2", "amount", bondAmount, "data", "0x"));
            narrate(clients[REGISTRAR_NODE], "Bank A holds the bond (minted by registrar)", mintTx);

            // Bank B funds its shielded cash pool, then approves the cash SNoto instance to pull it
            // (approve+transfer_from, this session's own SNoto deposit fix), then the cash notary
            // shields it into a real coin. How the funding itself happens depends on which kind of
            // test-USDC these fixtures point at:
            // - Testnet's real, shared "Test USDC" contract (deploy-stellar-fixtures.sh's own
            //   comment on it) is a native Soroban token, not a classic-asset-backed SAC - no
            //   issuer/trustline concept at all, and its own mint() was confirmed genuinely
            //   permissionless (no admin/require_auth gate) - so funding is one direct mint call.
            // - Local quickstart has no such shared official token, so it keeps deploying and
            //   funding its own throwaway classic asset instead, which DOES need a real trustline
            //   (submitted via Paladin's XDR_CLASSIC_OPS raw-passthrough path, signed by Bank B's
            //   own key since only the trustline's own holder can create it - unreachable via the
            //   in-process-only technique stellar_asset_test.go's establishTrustlineAndFund uses)
            //   followed by a real issuer-signed payment (mirrors stellar_asset_test.go's own
            //   invokeSACTransfer - a raw `stellar` CLI call, ordinary treasury funding, not a
            //   Paladin-orchestrated step).
            // deploy-stellar-fixtures.sh signals which situation the current fixtures represent by
            // leaving testUsdcIssuerAddress empty for the native-token case (a classic asset always
            // has a real issuer address; a native token has none).
            if (fixtures.testUsdcIssuerAddress().isEmpty()) {
                invokeStellarCli("contract", "invoke", "--id", fixtures.testUsdcSacAddress(),
                        "--source", "stellar-fixtures-deployer", "--network", stellarCliNetwork(),
                        "--", "mint", "--amount", cashAmount, "--to", bankBAddress);
                System.out.println("=== Bank B funded with " + cashAmount + " test-USDC (minted directly - testnet's real, shared Test USDC contract) ===");
            } else {
                String changeTrustHex = encodeChangeTrust(bankBAddress, "TUSD", fixtures.testUsdcIssuerAddress());
                // PublicTxOptions is embedded (anonymous) on TransactionBase - its fields (including
                // payloadKind) are flattened directly into the top-level transaction JSON, not
                // nested under a "publicTxOptions" key.
                String changeTrustTxID = NodeProcessHarness.rawRequestWithTimeout(rpcPorts[BANK_B_NODE], "ptx_sendTransaction", Map.of(
                        "type", "public",
                        "from", "bankB",
                        "payloadKind", "XDR_CLASSIC_OPS",
                        "data", changeTrustHex
                ), 120, String.class);
                waitForSuccess(clients[BANK_B_NODE], changeTrustTxID);
                narrate(clients[BANK_B_NODE], "Bank B establishes a trustline for test-USDC", changeTrustTxID);

                invokeStellarCli("contract", "invoke", "--id", fixtures.testUsdcSacAddress(),
                        "--source", "stellar-fixtures-test-usdc-issuer", "--network", stellarCliNetwork(),
                        "--", "transfer", "--from", fixtures.testUsdcIssuerAddress(), "--to", bankBAddress, "--amount", cashAmount);
                System.out.println("=== Bank B funded with " + cashAmount + " test-USDC (real classic asset transfer) ===");
            }

            int currentLedger = getLatestLedger();
            String approveHex = encodeSacApprove(fixtures.testUsdcSacAddress(), bankBAddress, cashAddress, cashAmount, currentLedger + 100000);
            // payloadKind must be explicit here - unlike buildStellarTx's own default-to-Soroban-
            // invoke behaviour, publicTxToUnsignedChainTx's resource-estimation stage defaults an
            // unset PayloadKind to FUNCTION_CALL_DATA (EVM's own shape), by design: "every existing
            // EVM caller leaves PayloadKind unset... Stellar callers must set PayloadKind explicitly."
            String approveTxID = NodeProcessHarness.rawRequestWithTimeout(rpcPorts[BANK_B_NODE], "ptx_sendTransaction", Map.of(
                    "type", "public",
                    "to", fixtures.testUsdcSacAddress(),
                    "from", "bankB",
                    "function", "approve",
                    "payloadKind", "XDR_INVOKE_CONTRACT_ARGS",
                    "data", approveHex
            ), 120, String.class);
            waitForSuccess(clients[BANK_B_NODE], approveTxID);
            narrate(clients[BANK_B_NODE], "Bank B approves the cash pool to pull test-USDC", approveTxID);

            String depositTx = submitAndWait(clients[REGISTRAR_NODE], rpcPorts[REGISTRAR_NODE], "noto-cash", cashAddress, "cashNotary",
                    depositABI(), "deposit", Map.of("from", "bankB@node3", "amount", cashAmount, "data", "0x"));
            narrate(clients[REGISTRAR_NODE], "Bank B holds shielded cash (real SAC pull via approve+transfer_from)", depositTx);

            // Sente's own deploy_group salt is sha256(members), so re-running this demo against the
            // same persistent chain with the same bankA/bankB pairing always targets the same
            // deployed group address - sente-factory's deploy_group is itself idempotent for
            // exactly this reason (checks Address::exists() before deploy_v2/initialize, still
            // registers unconditionally so this transaction's own on-chain event resolves
            // contractAddress either way - see that contract's own doc comment). The local-registry
            // lookup below is a pure optimization within one long-lived node process (skips an
            // unnecessary genesis call this node would otherwise redundantly resubmit), not what
            // makes repeat runs against a fresh node process safe - that's the factory's own job.
            String groupID = null;
            String senteGroupAddress = null;
            List<?> existingGroups = clients[BANK_A_NODE].request("pgroup_queryGroupsWithMember", "bankA@node2", Map.of("limit", 10));
            for (Object g : existingGroups) {
                Map<?, ?> group = (Map<?, ?>) g;
                if ("institutional-repo-demo".equals(group.get("name")) && "sente".equals(group.get("domain"))) {
                    groupID = (String) group.get("id");
                    senteGroupAddress = (String) group.get("contractAddress");
                    break;
                }
            }
            if (groupID == null) {
                Map<?, ?> createdGroup = NodeProcessHarness.rawRequestWithTimeout(rpcPorts[BANK_A_NODE], "pgroup_createGroup", Map.of(
                        "domain", "sente",
                        "name", "institutional-repo-demo",
                        "members", List.of("bankA@node2", "bankB@node3")
                ), 120, Map.class);
                groupID = (String) createdGroup.get("id");
                assertNotNull(groupID);
            }

            for (int i = 0; i < POLL_ITERATIONS && senteGroupAddress == null; i++) {
                Map<?, ?> group = clients[BANK_A_NODE].request("pgroup_getGroupById", "sente", groupID);
                Object contractAddress = group != null ? group.get("contractAddress") : null;
                if (contractAddress != null) {
                    senteGroupAddress = (String) contractAddress;
                } else {
                    Thread.sleep(500);
                }
            }
            assertNotNull(senteGroupAddress, "Sente group deploy did not confirm within timeout");
            System.out.println("=== Sente group (Bank A + Bank B) formed at " + senteGroupAddress + " ===");

            // Unlike the bond/cash SNoto instances above (deployed fresh under a per-transaction
            // tx_id salt every run, so an old one's TTL lapsing is harmless - it's simply never
            // touched again), this group's address is the same on every run against this same
            // bankA/bankB pairing (see the idempotent-deploy comment above) - so it's the one
            // contract this demo actually needs to keep alive indefinitely across runs spaced
            // arbitrarily far apart. Not needed against quickstart's own throwaway local chain.
            if ("testnet".equals(stellarCliNetwork())) {
                invokeStellarCli("contract", "extend", "--id", senteGroupAddress,
                        "--ledgers-to-extend", "2500000", "--source", "stellar-fixtures-deployer",
                        "--network", stellarCliNetwork());
            }

            // --- Near leg (ch.18 §18.5): bond A->B, cash B->A, atomically in one transition ---
            List<Object> bondUnlockArgs = lockPrepareAndDelegate(clients, rpcPorts, "noto-bond", bondAddress,
                    BANK_A_NODE, "bankA", "bankB@node3", bondAmount, senteGroupAddress);
            List<Object> cashUnlockArgs = lockPrepareAndDelegate(clients, rpcPorts, "noto-cash", cashAddress,
                    BANK_B_NODE, "bankB", "bankA@node2", cashAmount, senteGroupAddress);

            List<Map<String, Object>> nearLegCalls = List.of(
                    Map.of("contract", bondAddress, "function", "unlock", "args", bondUnlockArgs),
                    Map.of("contract", cashAddress, "function", "unlock", "args", cashUnlockArgs)
            );
            String nearLegTx = submitTransition(clients[BANK_A_NODE], rpcPorts[BANK_A_NODE], groupID, "bankA", nearLegCalls);
            narrate(clients[BANK_A_NODE], "NEAR LEG settled atomically: Bank A -> bond to Bank B, Bank B -> cash to Bank A", nearLegTx);

            pauseForDemo("Repo is open: Bank A holds cash, Bank B holds the bond. Next: the far leg matures the repo.");

            // --- Far leg (ch.18 §18.5): roles reverse. Bank A's repayment coin is a fresh mint
            // from the cash notary standing in for "already-held cash" (ch.18's own scoping keeps
            // repo-terms/rate modeling out of this demo - see chapter 18 §18.6) rather than an
            // ordinary private transfer, which would need Bank A to already hold a spendable coin.
            String repaymentTx = submitAndWait(clients[REGISTRAR_NODE], rpcPorts[REGISTRAR_NODE], "noto-cash", cashAddress, "cashNotary",
                    mintABI(), "mint", Map.of("to", "bankA@node2", "amount", cashAmount, "data", "0x"));
            narrate(clients[REGISTRAR_NODE], "Bank A funded with repayment cash (principal + interest, simplified as principal only)", repaymentTx);

            List<Object> bondReturnArgs = lockPrepareAndDelegate(clients, rpcPorts, "noto-bond", bondAddress,
                    BANK_B_NODE, "bankB", "bankA@node2", bondAmount, senteGroupAddress);
            List<Object> cashReturnArgs = lockPrepareAndDelegate(clients, rpcPorts, "noto-cash", cashAddress,
                    BANK_A_NODE, "bankA", "bankB@node3", cashAmount, senteGroupAddress);

            List<Map<String, Object>> farLegCalls = List.of(
                    Map.of("contract", bondAddress, "function", "unlock", "args", bondReturnArgs),
                    Map.of("contract", cashAddress, "function", "unlock", "args", cashReturnArgs)
            );
            String farLegTx = submitTransition(clients[BANK_B_NODE], rpcPorts[BANK_B_NODE], groupID, "bankB", farLegCalls);
            narrate(clients[BANK_B_NODE], "FAR LEG settled atomically: Bank B -> bond back to Bank A, Bank A -> cash back to Bank B", farLegTx);

            // --- Withdraw attempt (ch.18 §18.5's "converting back to real USDC") ---
            // withdrawHandler.Assemble (domains/noto/internal/noto/handler_withdraw.go) spends
            // coins owned by tx.Transaction.From itself, not params.Recipient - same "no separate
            // from param" convention as burn. Bank B holds the cash coin after the far leg, so
            // withdraw must be submitted as bankB, not cashNotary.
            String withdrawTx = submitAndWait(clients[BANK_B_NODE], rpcPorts[BANK_B_NODE], "noto-cash", cashAddress, "bankB",
                    withdrawABI(), "withdraw", Map.of("recipient", "bankB@node3", "amount", cashAmount, "data", "0x"));
            narrate(clients[BANK_B_NODE], "Bank B withdrew shielded cash back to real test-USDC", withdrawTx);

            System.out.println("=== Demo complete: repo opened and matured atomically across two SNoto instances via one Sente group ===");
        } finally {
            for (JsonRpcClient client : clients) {
                if (client != null) {
                    try {
                        client.close();
                    } catch (Exception ignored) {
                    }
                }
            }
            NodeProcessHarness.teardown(processes);
        }
    }

    // --- Stellar RPC/CLI helpers, self-contained (no Paladin identity involved) ---

    private static void invokeStellarCli(String... args) throws Exception {
        java.util.List<String> cmd = new java.util.ArrayList<>();
        cmd.add("stellar");
        cmd.addAll(List.of(args));
        Process p = new ProcessBuilder(cmd).redirectErrorStream(true).start();
        String output = new String(p.getInputStream().readAllBytes());
        boolean finished = p.waitFor(60, java.util.concurrent.TimeUnit.SECONDS);
        if (!finished || p.exitValue() != 0) {
            fail("stellar CLI command failed (%s): %s".formatted(String.join(" ", args), output));
        }
    }

    private static int getLatestLedger() throws Exception {
        ObjectMapper mapper = new ObjectMapper();
        Map<String, Object> body = Map.of("jsonrpc", "2.0", "id", 1, "method", "getLatestLedger");
        var req = java.net.http.HttpRequest.newBuilder()
                .uri(java.net.URI.create(rpcUrl()))
                .header("Content-Type", "application/json")
                .POST(java.net.http.HttpRequest.BodyPublishers.ofString(mapper.writeValueAsString(body)))
                .build();
        var res = java.net.http.HttpClient.newHttpClient().send(req, java.net.http.HttpResponse.BodyHandlers.ofString());
        Map<?, ?> rpcRes = mapper.readValue(res.body(), Map.class);
        Map<?, ?> result = (Map<?, ?>) rpcRes.get("result");
        return ((Number) result.get("sequence")).intValue();
    }

    // Shells out to the encode-sac-approve Go CLI (core/go/cmd/encode-sac-approve) built earlier
    // this session - Java has no Soroban XDR encoder of its own, and this call must be signed by
    // Bank B's own Paladin-managed key (so it can't be submitted directly via the `stellar` CLI,
    // which needs a raw secret key it doesn't have) - only the payload-building step is delegated.
    private static String encodeSacApprove(String sacAddress, String from, String spender, String amount, int expirationLedger) throws Exception {
        File goCliDir = new File("../../core/go");
        Process p = new ProcessBuilder(
                "go", "run", "./cmd/encode-sac-approve",
                sacAddress, from, spender, amount, String.valueOf(expirationLedger)
        ).directory(goCliDir).redirectErrorStream(true).start();
        String output = new String(p.getInputStream().readAllBytes()).trim();
        boolean finished = p.waitFor(60, java.util.concurrent.TimeUnit.SECONDS);
        if (!finished || p.exitValue() != 0) {
            fail("encode-sac-approve failed: " + output);
        }
        return output;
    }

    // Shells out to the encode-change-trust Go CLI (core/go/cmd/encode-change-trust), the
    // XDR_CLASSIC_OPS counterpart to encodeSacApprove above - a trustline can only be created by
    // its own holder, so this must be signed by Bank B's own Paladin-managed key.
    private static String encodeChangeTrust(String holderAccount, String assetCode, String assetIssuer) throws Exception {
        File goCliDir = new File("../../core/go");
        Process p = new ProcessBuilder(
                "go", "run", "./cmd/encode-change-trust",
                holderAccount, assetCode, assetIssuer
        ).directory(goCliDir).redirectErrorStream(true).start();
        String output = new String(p.getInputStream().readAllBytes()).trim();
        boolean finished = p.waitFor(60, java.util.concurrent.TimeUnit.SECONDS);
        if (!finished || p.exitValue() != 0) {
            fail("encode-change-trust failed: " + output);
        }
        return output;
    }
}
