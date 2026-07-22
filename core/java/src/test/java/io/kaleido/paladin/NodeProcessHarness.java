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

import com.fasterxml.jackson.databind.ObjectMapper;
import io.kaleido.paladin.toolkit.JsonRpcClient;

import java.io.File;
import java.net.ServerSocket;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.fail;

// Genuinely-separate-process node infrastructure, extracted from TestSenteThreeNodeHarness.java
// (that test's own doc comment covers the full rationale for launching real `Main.run(configFile,
// "engine")` OS processes over Testbed's single-JVM simulation) so a second harness needing the
// same mechanics (chapter 18's institutional repo demo, TestInstitutionalRepoDemo.java) doesn't
// duplicate it. Per-node YAML config building stays in each test file itself - that part varies too
// much (which domains, which identities/roles) to usefully share.
public final class NodeProcessHarness {

    private NodeProcessHarness() {
    }

    public static int freePort() throws Exception {
        try (ServerSocket s = new ServerSocket(0)) {
            return s.getLocalPort();
        }
    }

    public record NodeCert(String certPath, String keyPath, String certPem) {
    }

    // No self-signed-cert helper is importable from Java (transports/grpc's own TLS enforcement -
    // grpc_transport.go:115-118 unconditionally forces TLS on regardless of config - has no
    // plaintext escape hatch), so this shells out to openssl, mirroring this repo's own existing
    // convention of shelling out to an external CLI rather than reimplementing crypto in-repo
    // (soroban/scripts/deploy-stellar-fixtures.sh's use of the `stellar` CLI).
    public static NodeCert generateCert(File dir, String nodeName) throws Exception {
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

    // Joins rootDir-relative lib directories into one jna.library.path value - callers pass exactly
    // the set their own node config's domains/transports/registries need (e.g.
    // TestSenteThreeNodeHarness needs sente+grpc+static; TestInstitutionalRepoDemo also needs noto).
    public static String jnaLibraryPath(File rootDir, String... relativeDirs) {
        StringBuilder sb = new StringBuilder();
        for (String rel : relativeDirs) {
            if (sb.length() > 0) {
                sb.append(File.pathSeparator);
            }
            sb.append(new File(rootDir, rel).getAbsolutePath());
        }
        return sb.toString();
    }

    public static Process launchNode(File configFile, File logFile, String jnaLibraryPath) throws Exception {
        String javaBin = System.getProperty("java.home") + File.separator + "bin" + File.separator + "java";
        return new ProcessBuilder(
                javaBin,
                "-cp", System.getProperty("java.class.path"),
                "-Djna.library.path=" + jnaLibraryPath,
                "io.kaleido.paladin.Main",
                configFile.getAbsolutePath(),
                "engine"
        ).redirectOutput(logFile).redirectErrorStream(true).start();
    }

    // Waits for the real, non-testbed domain_listDomains RPC to respond with exactly
    // expectedDomainCount domains - the engine-mode equivalent of Testbed.java's own start()'s
    // testbed_listDomains poll, since a real engine node has no testbed_* methods at all.
    public static JsonRpcClient waitForReady(int rpcPort, Process process, long timeoutMs, int expectedDomainCount) throws Exception {
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
                if (domains.size() == expectedDomainCount) {
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
    // this bypasses JsonRpcClient for just this one call with a generous explicit timeout.
    public static <T> T rawRequestWithTimeout(int rpcPort, String method, Object params, long timeoutSeconds, Class<T> resultType) throws Exception {
        ObjectMapper mapper = new ObjectMapper();
        Map<String, Object> body = Map.of(
                "jsonrpc", "2.0",
                "id", RAW_REQUEST_ID.getAndIncrement(),
                "method", method,
                "params", List.of(params)
        );
        System.out.println("RAW REQUEST " + method + ": " + mapper.writeValueAsString(body));
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

    public static String resolveAndFundVerifier(JsonRpcClient client, String lookup) throws Exception {
        String verifier = client.request("ptx_resolveVerifier", lookup, "eddsa:ed25519", "stellar_address");
        assertNotNull(verifier);
        String friendbotUrl = System.getProperty("paladin.test.stellar.friendbotUrl", "http://localhost:8000/friendbot");
        var response = HttpClient.newHttpClient().send(
                HttpRequest.newBuilder(URI.create(friendbotUrl + "?addr=" + verifier)).GET().build(),
                HttpResponse.BodyHandlers.ofString());
        boolean alreadyFunded = response.statusCode() == 400 && response.body().contains("already funded");
        if (response.statusCode() != 200 && !alreadyFunded) {
            fail("failed to fund verifier %s via friendbot: HTTP %d: %s".formatted(verifier, response.statusCode(), response.body()));
        }
        return verifier;
    }

    // Destroys all non-null processes and waits (with a forced kill fallback) for clean exit -
    // callers use this in a `finally` block regardless of pass/fail, mirroring
    // TestSenteThreeNodeHarness's own teardown discipline.
    public static void teardown(Process[] processes) throws InterruptedException {
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
