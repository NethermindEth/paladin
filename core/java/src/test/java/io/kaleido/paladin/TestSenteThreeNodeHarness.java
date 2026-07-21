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
import io.kaleido.paladin.toolkit.JsonRpcClient;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.net.ServerSocket;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.time.Duration;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

// Chapter 14/15's "real 3-node Sente demo" gap: unlike every other Sente test in this package,
// which uses Testbed.java to simulate multiple "members" inside ONE JVM process (Testbed never
// emits a transports/registries YAML section at all, and hardcodes nodeName: node1 for every
// member - core/go/internal/componentmgr/manager.go:253-260,632-638 confirms transportManager/
// registryManager always start but do nothing without those sections), this test launches 3
// GENUINELY SEPARATE Java OS processes, each running Main.run(configFile, "engine") - the same
// config schema/parser Testbed uses (core/go/pkg/bootstrap/instance.go:96-115: "engine" is just
// "testbed" minus the extra in-process testbed.NewTestBed() manager and its testbed_* RPC stubs),
// wired together over the real `grpc` transport plugin and `static` registry plugin (mirroring
// core/go/noderuntests/pkg/testutils.go:246-290's own peer-wiring shape, the proven precedent for
// the real 3-node Go component test - just written as static YAML instead of built at Go-test
// runtime, since this harness has no equivalent Go test-time object graph to build it from).
//
// Scope: proves one Sente group member truly resolves on each of 3 separate nodes, and a
// root-only transition's unanimous-signature collection genuinely round-trips over real
// cross-process gRPC transport - not the Noto-domain external-call variant (already proven
// correct via TestSenteRealTransition.java's own external-call test and the Rust unit test),
// which is a natural follow-on once this harness itself is proven solid.
public class TestSenteThreeNodeHarness {

    private static final String[] NODE_NAMES = {"node1", "node2", "node3"};

    @JsonIgnoreProperties(ignoreUnknown = true)
    private record StellarFixtures(
            @JsonProperty String saladinFactoryAddress,
            @JsonProperty String senteFactoryAddress,
            @JsonProperty String senteWasmHash
    ) {
    }

    // Mirrors TestSenteRealTransition.java's own loadStellarFixtures - same file, same
    // Gradle-provisions-infrastructure convention, just this test's own minimal field subset.
    private static StellarFixtures loadStellarFixtures() throws Exception {
        File f = new File("../../soroban/artifacts/stellar-fixtures.json");
        if (!f.exists()) {
            fail("run `./gradlew :soroban:deployStellarFixtures` first (chapter 14 step 6) - " + f.getAbsolutePath() + " not found");
        }
        StellarFixtures fixtures = new ObjectMapper().readValue(f, StellarFixtures.class);
        assertNotNull(fixtures.saladinFactoryAddress());
        assertNotNull(fixtures.senteFactoryAddress());
        assertNotNull(fixtures.senteWasmHash());
        return fixtures;
    }

    private static int freePort() throws Exception {
        try (ServerSocket s = new ServerSocket(0)) {
            return s.getLocalPort();
        }
    }

    private record NodeCert(String certPath, String keyPath, String certPem) {
    }

    // No self-signed-cert helper is importable from Java (transports/grpc's own TLS enforcement -
    // grpc_transport.go:115-118 unconditionally forces TLS on regardless of config - has no
    // plaintext escape hatch), so this shells out to openssl, mirroring this repo's own existing
    // convention of shelling out to an external CLI rather than reimplementing crypto in-repo
    // (soroban/scripts/deploy-stellar-fixtures.sh's use of the `stellar` CLI).
    private static NodeCert generateCert(File dir, String nodeName) throws Exception {
        File certFile = new File(dir, nodeName + "-cert.pem");
        File keyFile = new File(dir, nodeName + "-key.pem");
        Process p = new ProcessBuilder(
                "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                "-keyout", keyFile.getAbsolutePath(),
                "-out", certFile.getAbsolutePath(),
                "-days", "3650",
                "-subj", "/CN=" + nodeName
        ).redirectErrorStream(true).start();
        String output = new String(p.getInputStream().readAllBytes());
        boolean finished = p.waitFor(30, TimeUnit.SECONDS);
        if (!finished || p.exitValue() != 0) {
            fail("openssl cert generation for %s failed: %s".formatted(nodeName, output));
        }
        return new NodeCert(certFile.getAbsolutePath(), keyFile.getAbsolutePath(), Files.readString(certFile.toPath()));
    }

    // Testbed.java hardcodes ONE wallet1 seed because every simulated "member" lives in the same
    // JVM/DB - a single underlying key is fine there. Reusing that same literal verbatim across 3
    // genuinely separate nodes is wrong: member1@node1/member2@node2/member3@node3 each resolve
    // via the FIRST identifier that node's own (per-node, independent) key_paths table allocates
    // under wallet1's keySelector (".*") - which lands on the SAME derivation index on every node
    // (each node performs the same sequence of prior operations), so an IDENTICAL seed makes all
    // 3 members derive to the exact same underlying key. Sente's own group genesis then registers
    // that one key 3 times as "3 distinct members", and its transition() entry point's own
    // duplicate-signer check (a real safety property, not a bug) correctly rejects the resulting
    // 3-identical-signatures submission with a WasmVm InvalidAction trap. Each node needs its own
    // distinct wallet1 seed so member1/2/3 are genuinely different keys, matching what "3 members"
    // is actually supposed to mean.
    private static String wallet1SeedForNode(int index) {
        String base = "cdd8dbc37a9fa235a3c56367bb029c27a1bdf49b8090070d1b22993f343e098d";
        return base.substring(0, base.length() - 2) + String.format("%02x", index);
    }

    // Builds one node's full engine-mode config: everything Testbed.java's own baseConfig()/
    // walletsYaml()/baseLedgerYaml() already generate (nodeName/db/rpcServer/wallets/baseLedger/
    // sequencerManager - reused as one literal text block, parsed into a Map so transports/
    // registries/domains can be merged in as native structures rather than hand-escaped YAML
    // text, exactly the technique Testbed.java itself already uses for its own "domains" section),
    // PLUS the two sections Testbed never emits: transports.grpc (this node's own listener) and
    // registries.registry1 (the OTHER two nodes' dial endpoints/certs, static.StaticEntry-shaped -
    // registries/static/internal/staticregistry/config.go:20-26).
    private static String buildNodeConfig(
            int index, int rpcPort, int[] grpcPorts, NodeCert[] certs, File workDir, StellarFixtures fixtures
    ) throws Exception {
        String base = """
                nodeName: %s
                db:
                  type: sqlite
                  sqlite:
                    dsn: "%s?_busy_timeout=5000&_journal_mode=WAL"
                    # maxOpenConns: SQLite's own driver default (core/go/pkg/persistence/sqlite.go's
                    # SQLiteDefaults.MaxOpenConns=1) starves Go's own database/sql connection pool
                    # into a self-deadlock the moment any code opens a second (NOTX()) DB handle
                    # while a Transaction() is still holding the pool's one-and-only connection -
                    # confirmed to be exactly what groupmgr.CreateGroup's own connectivity check
                    # (registrymgr.GetNodeTransports, called for any genuinely remote group member)
                    # does. Postgres never hits this (100-conn default pool + MVCC), which is why no
                    # existing Postgres-backed test in this repo has ever surfaced it. A config-only
                    # fix - no groupmgr/registrymgr code change needed. _busy_timeout/_journal_mode
                    # are a general SQLite-under-concurrency safety net now that maxOpenConns > 1 makes
                    # genuinely concurrent writers possible; the one specific race this harness hit
                    # (sequencer.go's deployment loop starting - and opening its own DB transaction -
                    # while the caller's own dbTX was still open) is fixed at the core level via
                    # dbTX.AddPostCommit, not by this DSN tuning.
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
                        # SLIP-10/ed25519 derivation (toolkit/go/pkg/signer/hd_key_derivation.go's
                        # loadSLIP10PrivateKey) is hardened-only by spec - every path segment must
                        # be hardened, enforced explicitly rather than silently accepted. This
                        # harness is all-Stellar/eddsa, so every identifier this wallet ever
                        # resolves must land entirely within the hardened range regardless of how
                        # many dot-separated levels it has (root/member1 need few; public-tx-
                        # manager's own internal orchestrator-loop retry bookkeeping needs more
                        # than 5) - set generously high rather than tuned to what's observed today.
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
                        # SLIP-10/ed25519 derivation (toolkit/go/pkg/signer/hd_key_derivation.go's
                        # loadSLIP10PrivateKey) is hardened-only by spec - every path segment must
                        # be hardened, enforced explicitly rather than silently accepted. This
                        # harness is all-Stellar/eddsa, so every identifier this wallet ever
                        # resolves must land entirely within the hardened range regardless of how
                        # many dot-separated levels it has (root/member1 need few; public-tx-
                        # manager's own internal orchestrator-loop retry bookkeeping needs more
                        # than 5) - set generously high rather than tuned to what's observed today.
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
                wallet1SeedForNode(index),
                System.getProperty("paladin.test.stellar.rpcUrl", "http://localhost:8000/soroban/rpc"),
                System.getProperty("paladin.test.stellar.networkPassphrase", "Standalone Network ; February 2017"),
                System.getProperty("paladin.test.stellar.pollInterval", "1s"),
                // Real testnet needs each channel account created+funded via a real on-chain
                // transaction (slow, ~5s ledger closes) rather than quickstart's fast local ledger -
                // matching stellar.testnet.node1.config.yaml's own quickstart(8)->testnet(2) reduction.
                System.getProperty("paladin.test.stellar.channelAccountPoolSize", "8"),
                System.getProperty("paladin.test.stellar.channelAccountStartingBalance", "5")
        );

        ObjectMapper yamlMapper = new ObjectMapper(YAMLFactory.builder().build());
        Map<String, Object> configMap = yamlMapper.readValue(base, new TypeReference<>() {
        });

        Map<String, Object> senteConfig = new HashMap<>() {{
            put("senteFactoryAddress", fixtures.senteFactoryAddress());
            put("saladinFactoryAddress", fixtures.saladinFactoryAddress());
            put("senteWasmHash", fixtures.senteWasmHash());
            put("networkPassphrase", System.getProperty("paladin.test.stellar.networkPassphrase", "Standalone Network ; February 2017"));
        }};
        Map<String, Object> sentePlugin = new HashMap<>() {{
            put("type", "c-shared");
            put("library", "sente");
        }};
        Map<String, Object> senteDomain = new HashMap<>() {{
            put("registryAddress", fixtures.saladinFactoryAddress());
            put("plugin", sentePlugin);
            put("config", senteConfig);
        }};
        configMap.put("domains", Map.of("sente", senteDomain));

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

        // Peer wiring: one static.StaticEntry per OTHER node, "transport.grpc" property set to a
        // JSON-ENCODED STRING of PublishedTransportDetails{Endpoint, Issuers} - confirmed via
        // transports/grpc/internal/grpctransport/grpc_transport.go:225-239's own
        // getTransportDetails, which json.Unmarshals gtdr.TransportDetails (a plain string) into
        // that struct; the static registry's own Properties map value type
        // (pldtypes.RawJSON) just carries those bytes through unmodified, so the YAML value here
        // must be the already-serialized inner JSON as a string, not a nested YAML mapping.
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

    private static String jnaLibraryPath() {
        String rootDir = new File("../..").getAbsolutePath();
        return String.join(File.pathSeparator,
                new File(rootDir, "core/go/build/libs").getAbsolutePath(),
                new File(rootDir, "toolkit/go/build/libs").getAbsolutePath(),
                new File(rootDir, "domains/sente/target/release").getAbsolutePath(),
                new File(rootDir, "transports/grpc/build/libs").getAbsolutePath(),
                new File(rootDir, "registries/static/build/libs").getAbsolutePath()
        );
    }

    private static Process launchNode(File configFile, File logFile) throws Exception {
        String javaBin = System.getProperty("java.home") + File.separator + "bin" + File.separator + "java";
        return new ProcessBuilder(
                javaBin,
                "-cp", System.getProperty("java.class.path"),
                "-Djna.library.path=" + jnaLibraryPath(),
                "io.kaleido.paladin.Main",
                configFile.getAbsolutePath(),
                "engine"
        ).redirectOutput(logFile).redirectErrorStream(true).start();
    }

    // Waits for the real, non-testbed domain_listDomains RPC to respond with exactly one domain
    // (sente) - the engine-mode equivalent of Testbed.java's own start()'s testbed_listDomains
    // poll, since a real engine node has no testbed_* methods at all.
    private static JsonRpcClient waitForReady(int rpcPort, Process process, long timeoutMs) throws Exception {
        long startTime = System.currentTimeMillis();
        JsonRpcClient client = new JsonRpcClient("http://127.0.0.1:%d".formatted(rpcPort));
        while (true) {
            if (!process.isAlive()) {
                fail("node process exited early with code %d before becoming ready".formatted(process.exitValue()));
            }
            long elapsed = System.currentTimeMillis() - startTime;
            if (elapsed > timeoutMs) {
                fail("timed out after %dms waiting for node RPC on port %d".formatted(elapsed, rpcPort));
            }
            try {
                List<?> domains = client.request("domain_listDomains");
                if (domains.size() == 1) {
                    return client;
                }
            } catch (Exception e) {
                // not ready yet
            }
            Thread.sleep(250);
        }
    }

    private static final AtomicLong RAW_REQUEST_ID = new AtomicLong(20000000);

    // JsonRpcClient's own request() hardcodes a fixed 30s HTTP timeout (toolkit/java/.../
    // JsonRpcClient.java:92,109) with no override point. pgroup_createGroup was observed to
    // reliably exceed that on this harness's very first genesis-deploy attempt against a brand
    // new group/contract - the same category of "first private transaction dispatch" latency
    // already flagged as unresolved for Testbed's own mint/deploy calls (chapter 14 §14.3) - so
    // this bypasses JsonRpcClient for just this one call with a generous explicit timeout, to
    // confirm whether it's the same "just needs more time" characteristic rather than a hang.
    private static <T> T rawRequestWithTimeout(int rpcPort, String method, Object params, long timeoutSeconds, Class<T> resultType) throws Exception {
        ObjectMapper mapper = new ObjectMapper();
        Map<String, Object> body = Map.of(
                "jsonrpc", "2.0",
                "id", RAW_REQUEST_ID.getAndIncrement(),
                "method", method,
                "params", List.of(params)
        );
        HttpRequest req = HttpRequest.newBuilder()
                .timeout(Duration.ofSeconds(timeoutSeconds))
                .uri(URI.create("http://127.0.0.1:%d".formatted(rpcPort)))
                .header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(mapper.writeValueAsString(body)))
                .build();
        HttpResponse<String> res = HttpClient.newHttpClient().send(req, HttpResponse.BodyHandlers.ofString());
        Map<?, ?> rpcRes = mapper.readValue(res.body(), Map.class);
        if (rpcRes.get("error") != null) {
            fail("%s failed: %s".formatted(method, rpcRes.get("error")));
        }
        return mapper.convertValue(rpcRes.get("result"), resultType);
    }

    private static String resolveAndFundVerifier(JsonRpcClient client, String lookup) throws Exception {
        String verifier = client.request("ptx_resolveVerifier", lookup, "eddsa:ed25519", "stellar_address");
        assertNotNull(verifier);
        String friendbotUrl = System.getProperty("paladin.test.stellar.friendbotUrl", "http://localhost:8000/friendbot");
        var response = java.net.http.HttpClient.newHttpClient().send(
                java.net.http.HttpRequest.newBuilder(java.net.URI.create(friendbotUrl + "?addr=" + verifier)).GET().build(),
                java.net.http.HttpResponse.BodyHandlers.ofString());
        boolean alreadyFunded = response.statusCode() == 400 && response.body().contains("already funded");
        if (response.statusCode() != 200 && !alreadyFunded) {
            fail("failed to fund verifier %s via friendbot: HTTP %d: %s".formatted(verifier, response.statusCode(), response.body()));
        }
        return verifier;
    }

    @Test
    void threeRealNodesFormSenteGroupAndTransition() throws Exception {
        StellarFixtures fixtures = loadStellarFixtures();
        File workDir = Files.createTempDirectory("sente-3node").toFile();

        int[] rpcPorts = new int[NODE_NAMES.length];
        int[] grpcPorts = new int[NODE_NAMES.length];
        NodeCert[] certs = new NodeCert[NODE_NAMES.length];
        for (int i = 0; i < NODE_NAMES.length; i++) {
            rpcPorts[i] = freePort();
            grpcPorts[i] = freePort();
            certs[i] = generateCert(workDir, NODE_NAMES[i]);
        }

        Process[] processes = new Process[NODE_NAMES.length];
        JsonRpcClient[] clients = new JsonRpcClient[NODE_NAMES.length];
        try {
            for (int i = 0; i < NODE_NAMES.length; i++) {
                String yaml = buildNodeConfig(i, rpcPorts[i], grpcPorts, certs, workDir, fixtures);
                File configFile = new File(workDir, NODE_NAMES[i] + ".yaml");
                Files.writeString(configFile.toPath(), yaml);
            }

            // Every node's "root" wallet uses the identical seed (see buildNodeConfig) and the
            // identical keySelector ("^root$"), but each node keeps its OWN independent key_paths
            // allocation table (one SQLite file per node) - so the BIP32 derivation INDEX "root"
            // lands on (the 3rd path segment, e.g. m/44'/60'/1' vs m/44'/60'/3') depends on
            // incidental per-node allocation order, not just the identifier string. That means
            // "root" resolves to a genuinely DIFFERENT Stellar address on each node, and each
            // node's own channelAccounts.funder ("root") must be funded on THAT node specifically -
            // funding only node1's "root" leaves node2/node3's own channel-account funder
            // (and, when either becomes the active coordinator and dispatches a transaction using a
            // dynamically-allocated signing identity - see coordinator/coordinating.go's
            // getCoordinatorSigningIdentity - its own dispatch funding) permanently unfunded.
            //
            // Launching all 3 processes at once and funding "root" only afterwards also loses a
            // race: each node's channel-account dispatch loop starts looking up its (still-unfunded)
            // funder account immediately on boot. That lookup itself retries with backoff (not a
            // permanent failure), so as long as funding happens soon after each node starts, it
            // recovers - but node1 is started alone first anyway, since its own group-genesis-deploy
            // dispatch needs its "root" funded before pgroup_createGroup is even called.
            processes[0] = launchNode(new File(workDir, NODE_NAMES[0] + ".yaml"), new File(workDir, NODE_NAMES[0] + ".engine.log"));
            clients[0] = waitForReady(rpcPorts[0], processes[0], 30_000);
            resolveAndFundVerifier(clients[0], "root");

            for (int i = 1; i < NODE_NAMES.length; i++) {
                processes[i] = launchNode(new File(workDir, NODE_NAMES[i] + ".yaml"), new File(workDir, NODE_NAMES[i] + ".engine.log"));
            }
            for (int i = 1; i < NODE_NAMES.length; i++) {
                clients[i] = waitForReady(rpcPorts[i], processes[i], 30_000);
                resolveAndFundVerifier(clients[i], "root");
            }

            Map<?, ?> createdGroup = rawRequestWithTimeout(rpcPorts[0], "pgroup_createGroup", Map.of(
                    "domain", "sente",
                    "name", "sente-three-node-harness-test",
                    // The key difference from every other Sente test in this package: each
                    // member locator resolves on a genuinely separate real node, not "@node1"
                    // three times over.
                    "members", List.of("member1@node1", "member2@node2", "member3@node3")
            ), 120, Map.class);
            assertNotNull(createdGroup);
            String groupID = (String) createdGroup.get("id");
            assertNotNull(groupID);

            // pollIterations x 500ms: 180 (90s) is generous for quickstart's fast, predictable
            // ~1s ledger closes, but real testnet's slower ~5s closes - plus the extra ledger cycle
            // needed just to index the deploy/transition once submitted - occasionally need more,
            // so this is overridable rather than hardcoded.
            int pollIterations = Integer.parseInt(System.getProperty("paladin.test.stellar.pollIterations", "180"));

            String groupAddress = null;
            for (int i = 0; i < pollIterations && groupAddress == null; i++) {
                Map<?, ?> group = clients[0].request("pgroup_getGroupById", "sente", groupID);
                Object contractAddress = group != null ? group.get("contractAddress") : null;
                if (contractAddress != null) {
                    groupAddress = (String) contractAddress;
                } else {
                    Thread.sleep(500);
                }
            }
            assertNotNull(groupAddress, "group deploy did not confirm within timeout");
            assertFalse(groupAddress.isBlank());

            Map<String, Object> transitionFunction = Map.of(
                    "type", "function",
                    "name", "transition",
                    "inputs", List.of(),
                    "outputs", List.of()
            );
            String txID = rawRequestWithTimeout(rpcPorts[0], "pgroup_sendTransaction", Map.of(
                    "domain", "sente",
                    "group", groupID,
                    "from", "member1",
                    "function", transitionFunction,
                    "input", Map.of()
            ), 120, String.class);
            assertNotNull(txID);

            Boolean succeeded = null;
            for (int i = 0; i < pollIterations && succeeded == null; i++) {
                Map<?, ?> receipt = clients[0].request("ptx_getTransactionReceipt", txID);
                if (receipt != null) {
                    succeeded = (Boolean) receipt.get("success");
                } else {
                    Thread.sleep(500);
                }
            }
            assertNotNull(succeeded, "transition did not receive a receipt within timeout");
            assertTrue(succeeded, "transition receipt was not successful");
        } finally {
            for (JsonRpcClient client : clients) {
                if (client != null) {
                    try {
                        client.close();
                    } catch (Exception ignored) {
                    }
                }
            }
            for (Process process : processes) {
                if (process != null) {
                    process.destroy();
                }
            }
            for (Process process : processes) {
                if (process != null) {
                    process.waitFor(10, TimeUnit.SECONDS);
                    if (process.isAlive()) {
                        process.destroyForcibly();
                    }
                }
            }
        }
    }
}
