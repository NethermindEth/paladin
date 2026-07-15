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

import io.kaleido.paladin.testbed.Testbed;
import io.kaleido.paladin.toolkit.JsonHex;
import org.junit.jupiter.api.Test;

import java.util.HashMap;

// Chapter 14 §14.3's Phase 0 (M0 spike): proves a Rust cdylib loads via the exact same JNA/
// PluginJNA path Go's c-shared domains already use (mirrors TestStartTestbedWithNoopDomains.java's
// "starter" case, substituting the Rust "sente" hello-world plugin - see domains/sente/crates/
// sente). No soroban-env-host embedding here yet; ConfigureDomain/InitDomain only.
public class TestStartTestbedWithSenteHelloWorld {

    @Test
    void runTestbed() throws Exception {
        Testbed testBed = new Testbed(
                new Testbed.Setup("../go/db/migrations/sqlite", "build/test.java-sente-hello-world.txt", 5000),
                new Testbed.ConfigDomain(
                        "sente",
                        JsonHex.Address.addressFrom("0x1234567890abcdef1234567890abcdef12345678"),
                        new Testbed.ConfigPlugin("c-shared", "sente", ""),
                        new HashMap<>())
        );
        testBed.close();
    }
}
