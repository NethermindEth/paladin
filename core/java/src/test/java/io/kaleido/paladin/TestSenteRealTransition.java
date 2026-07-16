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
import com.fasterxml.jackson.databind.ObjectMapper;
import io.kaleido.paladin.testbed.Testbed;
import io.kaleido.paladin.toolkit.JsonABI;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.fail;

// Chapter 14 §14.3 S3's real Go-side integration test: loads the actual compiled Sente cdylib via
// the JVM's JNA path (the same mechanism TestStartTestbedWithSenteHelloWorld.java already proves
// for a hello-world stub - this test replaces the hello-world business logic with the real
// SenteDomain, exercising a genuine group deploy and one root-only transition against a live
// Stellar quickstart network). Unlike TestStartTestbedWithSenteHelloWorld, this needs
// `./gradlew :soroban:deployStellarFixtures` and `:testinfra:startTestInfra` to have already run -
// see loadStellarFixtures's own doc comment.
public class TestSenteRealTransition {

    @JsonIgnoreProperties(ignoreUnknown = true)
    private record StellarFixtures(
            @JsonProperty String saladinFactoryAddress,
            @JsonProperty String senteFactoryAddress,
            @JsonProperty String senteWasmHash
    ) {
    }

    // Mirrors core/go/noderuntests/componenttest/stellar_component_test.go's own
    // loadStellarFixtures - same file, same "Gradle/docker-compose provisions infrastructure"
    // convention, just read from core/java's own working directory instead.
    private StellarFixtures loadStellarFixtures() throws Exception {
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

    @Test
    void deployGroupAndSubmitTransition() throws Exception {
        StellarFixtures fixtures = loadStellarFixtures();

        Testbed testbed = new Testbed(
                new Testbed.Setup("../go/db/migrations/sqlite", "build/test.java-sente-real-transition.txt", 15000),
                Testbed.BaseLedger.STELLAR,
                new Testbed.ConfigDomain(
                        "sente",
                        fixtures.saladinFactoryAddress(),
                        new Testbed.ConfigPlugin("c-shared", "sente", ""),
                        new HashMap<>() {{
                            put("senteFactoryAddress", fixtures.senteFactoryAddress());
                            put("saladinFactoryAddress", fixtures.saladinFactoryAddress());
                            put("senteWasmHash", fixtures.senteWasmHash());
                            put("networkPassphrase", "Standalone Network ; February 2017");
                        }},
                        // "root" is the one account guaranteed to exist and be funded on a fresh
                        // quickstart network (it derives the network's own genesis account, seed =
                        // SHA-256(networkPassphrase) - see stellar.node1.config.yaml's own comment)
                        // - member1's own account is never funded in this test, so it can't be the
                        // one submitting chain-neutral transactions itself.
                        "root")
        );
        try {
            // Genesis: create the group via groupmgr's own pgroup_createGroup (not
            // testbed_deployChainNeutral directly) - this is what makes core persist "member1" as
            // this group's identity locator (InitContractRequest.privacy_group.members), which
            // SenteDomain.configure_privacy_group/init_privacy_group (domain.rs) turn into a real
            // SenteFactory.deploy_group deploy underneath, reusing S3's already-proven genesis-deploy
            // code unchanged. Without going through groupmgr, "member1" is only ever known
            // transiently to this deploy transaction, and assemble_transaction's "endorsement"
            // attestation has no member identity locator to route to - see 14-domain-ports.md §14.3
            // S3's "no endorsement signatures collected" section.
            Map<?, ?> createdGroup = testbed.getRpcClient().request("pgroup_createGroup",
                    new HashMap<>() {{
                        put("domain", "sente");
                        put("name", "sente-real-transition-test");
                        // groupmgr requires every member locator to be fully node-qualified
                        // (PD020017) - "node1" is Testbed's own fixed local node name
                        // (Testbed.java's nodeName: node1 in its generated config).
                        put("members", List.of("member1@node1"));
                    }});
            assertNotNull(createdGroup);
            String groupID = (String) createdGroup.get("id");
            assertNotNull(groupID);

            // pgroup_createGroup only submits the genesis deploy transaction - it doesn't wait for
            // on-chain confirmation the way testbed_deployChainNeutral's caller-visible return did, so
            // poll pgroup_getGroupById until the deploy confirms and the group's real on-chain
            // contract address is indexed (mirrors deployPrivateContract's own ExecDeployAndWait on
            // the Go side, just driven from the JVM side here since pgroup_createGroup has no
            // synchronous-wait equivalent).
            String groupAddress = null;
            for (int i = 0; i < 60 && groupAddress == null; i++) {
                Map<?, ?> group = testbed.getRpcClient().request("pgroup_getGroupById", "sente", groupID);
                Object contractAddress = group != null ? group.get("contractAddress") : null;
                if (contractAddress != null) {
                    groupAddress = (String) contractAddress;
                } else {
                    Thread.sleep(500);
                }
            }
            assertNotNull(groupAddress, "group deploy did not confirm within timeout");
            assertFalse(groupAddress.isBlank());

            // One root-only transition (chapter 14 §14.3's module doc comment): assemble_transaction
            // ignores function_params_json entirely for this phase, so the ABI/inputs here are a
            // placeholder satisfying testbed's own "an ABI is required" check, not a real business
            // payload.
            JsonABI transitionABI = new JsonABI();
            transitionABI.add(JsonABI.newFunction("transition", new JsonABI.Parameters(), new JsonABI.Parameters()));

            Map<?, ?> result = testbed.getRpcClient().request("testbed_invoke",
                    new Testbed.TransactionInput(
                            "private",
                            "sente",
                            "member1",
                            groupAddress,
                            new HashMap<>(),
                            transitionABI,
                            "transition"
                    ),
                    true);
            assertNotNull(result);
        } finally {
            testbed.close();
        }
    }

    // Same flow as deployGroupAndSubmitTransition, but with a genuinely multi-member group (two
    // distinct signers, not one member endorsing itself) - proves the group-scoped endorsement
    // parties fix (14-domain-ports.md §14.3 S3) generalizes past N=1, and that the on-chain
    // `transition`'s unanimous-signature check (`signatures.len() != members.len()`) is satisfied by
    // two independently-collected, independently-verified ed25519 signatures rather than one.
    @Test
    void deployMultiMemberGroupAndSubmitTransition() throws Exception {
        StellarFixtures fixtures = loadStellarFixtures();

        Testbed testbed = new Testbed(
                new Testbed.Setup("../go/db/migrations/sqlite", "build/test.java-sente-multi-member-transition.txt", 15000),
                Testbed.BaseLedger.STELLAR,
                new Testbed.ConfigDomain(
                        "sente",
                        fixtures.saladinFactoryAddress(),
                        new Testbed.ConfigPlugin("c-shared", "sente", ""),
                        new HashMap<>() {{
                            put("senteFactoryAddress", fixtures.senteFactoryAddress());
                            put("saladinFactoryAddress", fixtures.saladinFactoryAddress());
                            put("senteWasmHash", fixtures.senteWasmHash());
                            put("networkPassphrase", "Standalone Network ; February 2017");
                        }},
                        "root")
        );
        try {
            // "root"'s own resolved verifier is allocation-order-dependent: Paladin's key resolver
            // (core/go/internal/keymanager/key_resolver.go) allocates a sequential HD index per
            // *parent* scope, and every top-level identity - "root", "member1", "member2" - shares
            // the same (empty) parent, regardless of which named wallet's keySelector regex
            // eventually matches it. So "root"'s resolved key depends on how many *other* distinct
            // top-level identities were resolved before it, which depends on group size - it isn't
            // actually pinned to the network's genesis account by identity alone, only
            // coincidentally so in the single-member test (exactly one prior allocation,
            // "member1", happens to land "root" on the same index the quickstart network's own
            // genesis account uses). Rather than depend on that coincidence for every future group
            // size, resolve "root" up front and fund whatever it actually resolves to directly via
            // the quickstart network's friendbot - this makes the test correct independent of
            // however many members the group has.
            String rootVerifier = testbed.getRpcClient().request("testbed_resolveVerifier", "root", "eddsa:ed25519", "stellar_address");
            assertNotNull(rootVerifier);
            var friendbotResponse = java.net.http.HttpClient.newHttpClient().send(
                    java.net.http.HttpRequest.newBuilder(java.net.URI.create("http://localhost:8000/friendbot?addr=" + rootVerifier)).GET().build(),
                    java.net.http.HttpResponse.BodyHandlers.ofString());
            // The chain itself persists across test runs (only the sqlite DB behind key
            // resolution is fresh each run), so a prior run funding this same resolved index is a
            // real, expected outcome here, not a failure.
            boolean alreadyFunded = friendbotResponse.statusCode() == 400
                    && friendbotResponse.body().contains("already funded");
            if (friendbotResponse.statusCode() != 200 && !alreadyFunded) {
                fail("failed to fund root verifier %s via friendbot: HTTP %d: %s".formatted(rootVerifier, friendbotResponse.statusCode(), friendbotResponse.body()));
            }

            Map<?, ?> createdGroup = testbed.getRpcClient().request("pgroup_createGroup",
                    new HashMap<>() {{
                        put("domain", "sente");
                        put("name", "sente-multi-member-transition-test");
                        put("members", List.of("member1@node1", "member2@node1"));
                    }});
            assertNotNull(createdGroup);
            String groupID = (String) createdGroup.get("id");
            assertNotNull(groupID);

            // Longer timeout than the single-member test: root was only just funded above (rather
            // than long since established), so this run also pays the one-time cost of creating and
            // funding the channel-account pool (8 accounts) before the deploy itself can submit.
            String groupAddress = null;
            for (int i = 0; i < 180 && groupAddress == null; i++) {
                Map<?, ?> group = testbed.getRpcClient().request("pgroup_getGroupById", "sente", groupID);
                Object contractAddress = group != null ? group.get("contractAddress") : null;
                if (contractAddress != null) {
                    groupAddress = (String) contractAddress;
                } else {
                    Thread.sleep(500);
                }
            }
            assertNotNull(groupAddress, "group deploy did not confirm within timeout");
            assertFalse(groupAddress.isBlank());

            JsonABI transitionABI = new JsonABI();
            transitionABI.add(JsonABI.newFunction("transition", new JsonABI.Parameters(), new JsonABI.Parameters()));

            Map<?, ?> result = testbed.getRpcClient().request("testbed_invoke",
                    new Testbed.TransactionInput(
                            "private",
                            "sente",
                            "member1",
                            groupAddress,
                            new HashMap<>(),
                            transitionABI,
                            "transition"
                    ),
                    true);
            assertNotNull(result);
        } finally {
            testbed.close();
        }
    }
}
