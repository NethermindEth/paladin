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
            // Genesis: deploy a one-member group. init_deploy/prepare_deploy (S3) resolve
            // "member1"'s group-scoped verifier and build a real SenteFactory.deploy_group
            // SorobanInvoke.
            String groupAddress = testbed.getRpcClient().request("testbed_deployChainNeutral",
                    "sente", "member1",
                    new HashMap<>() {{
                        put("group", new HashMap<>() {{
                            put("salt", "0x0101010101010101010101010101010101010101010101010101010101010101");
                            put("members", List.of("member1"));
                        }});
                    }});
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
}
