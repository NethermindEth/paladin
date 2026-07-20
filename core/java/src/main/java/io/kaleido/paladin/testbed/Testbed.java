/*
 * Copyright © 2024 Kaleido, Inc.
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


 package io.kaleido.paladin.testbed;

 import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
 import com.fasterxml.jackson.databind.JsonNode;
 import io.kaleido.paladin.logging.PaladinLogging;
 import io.kaleido.paladin.toolkit.JsonABI;
 import io.kaleido.paladin.toolkit.JsonHex;
 import io.kaleido.paladin.toolkit.JsonRpcClient;
 
 import com.fasterxml.jackson.annotation.JsonInclude;
 import com.fasterxml.jackson.annotation.JsonProperty;
 import com.fasterxml.jackson.core.type.TypeReference;
 import com.fasterxml.jackson.databind.ObjectMapper;
 import com.fasterxml.jackson.dataformat.yaml.YAMLFactory;
 import io.kaleido.paladin.Main;
 import org.apache.logging.log4j.Logger;
 
 import java.io.Closeable;
 import java.io.File;
 import java.io.IOException;
 import java.net.ServerSocket;
 import java.nio.file.Files;
 import java.util.HashMap;
 import java.util.List;
 import java.util.Map;
 import java.util.UUID;
 import java.util.concurrent.CompletableFuture;
 import java.util.concurrent.ExecutionException;
 import java.util.concurrent.TimeoutException;
 
 public class Testbed implements Closeable {
 
     private final Logger LOGGER = PaladinLogging.getLogger(Testbed.class);
 
     private final String yamlConfigMerged;
 
     private final long availableRPCPort;
 
     private final ConfigDomain[] configuredDomains;
 
     private CompletableFuture<Integer> mainRun;
 
     private final Setup testbedSetup;
 
     private JsonRpcClient rpcClient;
 
     public record Setup(
             String dbMigrationsDir,
             String logFile,
             long startTimeoutMS
     ) {
     }
 
 
     @JsonIgnoreProperties(ignoreUnknown = true)
     public record StateEncoded(
             @JsonProperty
             JsonHex.Bytes id,
             @JsonProperty
             String domain,
             @JsonProperty
             JsonHex.Bytes32 schema,
             @JsonProperty
             JsonHex.Address contractAddress,
             @JsonProperty
             JsonHex.Bytes data
     ) {
     }
 
     @JsonIgnoreProperties(ignoreUnknown = true)
     public record TransactionInput(
             @JsonProperty
             String type,
             @JsonProperty
             String domain,
             @JsonProperty
             String from,
             // A plain Object, not JsonHex.Address: an EVM contract address (existing callers,
             // unchanged) or a Stellar strkey String (chapter 14 §14.3's Sente integration) - same
             // reasoning as ConfigDomain.registryAddress above.
             @JsonProperty
             Object to,
             @JsonProperty
             Map<String, Object> data,
             @JsonProperty
             JsonABI abi,
             @JsonProperty
             String function
     ) {
     }
 
     @JsonIgnoreProperties(ignoreUnknown = true)
     public record TransactionResult(
             @JsonProperty
             String id,
             @JsonProperty
             JsonHex.Bytes encodedCall,
             @JsonProperty
             TransactionInput preparedTransaction,
             @JsonProperty
             JsonNode preparedMetadata,
             @JsonProperty
             List<StateEncoded> inputStates,
             @JsonProperty
             List<StateEncoded> outputStates,
             @JsonProperty
             List<StateEncoded> readStates,
             @JsonProperty
             List<StateEncoded> infoStates,
             @JsonProperty
             JsonNode domainReceipt
     ) {
     }
 
     // BaseLedger selects which chain kind Testbed's own base-ledger config block targets - EVM
     // (the original, still-default behavior) or Stellar (chapter 14 §14.3's Sente integration,
     // never previously exercised through this class - see baseConfig()'s own doc comment).
     public enum BaseLedger {
         EVM,
         STELLAR,
     }

     private final BaseLedger baseLedger;

     public Testbed(Setup testbedSetup, ConfigDomain... domains) throws Exception {
         this(testbedSetup, BaseLedger.EVM, domains);
     }

     public Testbed(Setup testbedSetup, BaseLedger baseLedger, ConfigDomain... domains) throws Exception {
         this.testbedSetup = testbedSetup;
         this.baseLedger = baseLedger;
         this.configuredDomains = domains;
         // Assign ourselves a free port
         try (ServerSocket s = new ServerSocket(0);) {
             availableRPCPort = s.getLocalPort();
         }

         // Build the config
         ObjectMapper objectMapper = new ObjectMapper(YAMLFactory.builder().build());
         var baseConfig = baseConfig();
         Map<String, Object> configMap = objectMapper.readValue(baseConfig, new TypeReference<>() {
         });
         Map<String, Object> domainMap = new HashMap<>();
         for (ConfigDomain domain : domains) {
             domainMap.put(domain.name(), domain);
         }
         configMap.put("domains", domainMap);
         yamlConfigMerged = objectMapper.writerWithDefaultPrettyPrinter().writeValueAsString(configMap);
         try {
             start();
         } catch (Exception e) {
             close();
             throw e;
         }
     }

     public record ConfigDomain(
             String name,
             // A plain Object, not JsonHex.Address: an EVM registry address (existing callers,
             // unchanged - Jackson serializes a JsonHex.Address exactly as before via its own
             // serializer) or a Stellar strkey String (chapter 14 §14.3's Sente integration -
             // JsonHex.Address's 20-byte-hex constructor cannot hold one).
             @JsonProperty
             Object registryAddress,
             @JsonProperty
             ConfigPlugin plugin,
             @JsonProperty
             Map<String, Object> config,
             // Matches pldconf.DomainConfig.FixedSigningIdentity - which base-ledger identity to
             // submit every public/chain-neutral transaction for this domain as, rather than a
             // fresh one-time key per transaction. Needed on Stellar (chapter 14 §14.3's Sente
             // integration): a one-time key resolves to a brand new, unfunded account, which a
             // chain-neutral InvokeHostFunction's own SourceAccount needs to be pre-existing/funded
             // for - channel-account pooling only funds the outer transaction envelope, not the
             // operation's own source.
             @JsonProperty
             @JsonInclude(JsonInclude.Include.NON_DEFAULT)
             String fixedSigningIdentity
     ) {
         public ConfigDomain(String name, Object registryAddress, ConfigPlugin plugin, Map<String, Object> config) {
             this(name, registryAddress, plugin, config, "");
         }
     }
 
     public record ConfigPlugin(
             @JsonProperty
             String type,
             @JsonProperty
             String library,
             @JsonProperty("class")
             @JsonInclude(JsonInclude.Include.NON_DEFAULT)
             String clazz
     ) {
     }
 
     private String baseConfig() {
         return """
                 nodeName: node1
                 db:
                   type: sqlite
                   sqlite:
                     dsn:           ":memory:"
                     autoMigrate:   true
                     migrationsDir: %s
                     debugQueries:  false
                 %s
                 rpcServer:
                   http:
                     port: %s
                     shutdownTimeout: 0s
                   ws:
                     disabled: true
                     shutdownTimeout: 0s
                 grpc:
                     shutdownTimeout: 0s
                 blockIndexer:
                   fromBlock: latest
                 %s
                 loader:
                   debug: true
                 log:
                   level: debug
                   output: file
                   file:
                     filename: %s
                 """.formatted(
                 new File(testbedSetup.dbMigrationsDir).getAbsolutePath(),
                 walletsYaml(),
                 availableRPCPort,
                 baseLedgerYaml(),
                 new File(testbedSetup.logFile).getAbsolutePath()
         );
     }

     private String walletsYaml() {
         return switch (baseLedger) {
             case EVM -> """
                     wallets:
                     - name: wallet1
                       keySelector: .*
                       signer:
                         keyDerivation:
                           type: "bip32"
                         keyStore:
                           type: "static"
                           static:
                             keys:
                               seed:
                                 encoding: hex
                                 inline: '%s'
                     """.formatted(JsonHex.randomBytes32());
             // Fixed seeds, matching core/go/noderuntests/componenttest/config/stellar.node1.config.yaml
             // exactly (the proven precedent this mirrors) - bip44HardenedSegments: 5 on both wallets
             // is required for SLIP-10 ed25519 derivation (every Stellar/ed25519 identity resolved
             // here), unlike secp256k1/EVM's default of 1. "root" derives the standalone network's
             // genesis/root account (seed = SHA-256(networkPassphrase)), which funds channel accounts
             // since there's no friendbot in this dev/test setup (Horizon is never started).
             case STELLAR -> """
                     wallets:
                       - name: root
                         keySelector: "^root$"
                         signer:
                           keyDerivation:
                             type: "bip32"
                             bip44HardenedSegments: 5
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
                             bip44HardenedSegments: 5
                           keyStore:
                             type: "static"
                             static:
                               keys:
                                 seed:
                                   encoding: hex
                                   inline: cdd8dbc37a9fa235a3c56367bb029c27a1bdf49b8090070d1b22993f343e098d
                     """;
         };
     }

     // Stellar quickstart --local network defaults (testinfra/docker-compose-test.yml's
     // stellar_quickstart service) - mirrors core/go/noderuntests/componenttest/config/
     // stellar.node1.config.yaml's baseLedger block verbatim, the proven precedent this reuses
     // rather than inventing a second Stellar test config from scratch. Overridable via system
     // properties for a manual Stellar-testnet run (chapter 14/15's "testnet manual demo"
     // workstream) - defaults are unchanged, so no existing quickstart-based run is affected
     // unless these are explicitly set, e.g.:
     //   -Dpaladin.test.stellar.rpcUrl=https://soroban-testnet.stellar.org/
     //   -Dpaladin.test.stellar.networkPassphrase="Test SDF Network ; September 2015"
     //   -Dpaladin.test.stellar.pollInterval=5s
     private String baseLedgerYaml() {
         return switch (baseLedger) {
             case EVM -> """
                     blockchain:
                        http:
                          url: http://localhost:8545
                        ws:
                          url: ws://localhost:8546
                     """;
             case STELLAR -> """
                     baseLedger:
                       type: stellar
                       stellar:
                         url: %s
                         networkPassphrase: "%s"
                         ingestor:
                           pollInterval: "%s"
                           insertDBBatchSize: 100
                         channelAccounts:
                           poolSize: 8
                           funder: root
                           startingBalance: "5"
                     publicTxManager:
                       gasPrice:
                         fixedGasPrice:
                           maxFeePerGas: "0x0"
                           maxPriorityFeePerGas: "0x0"
                     """.formatted(
                     System.getProperty("paladin.test.stellar.rpcUrl", "http://localhost:8000/soroban/rpc"),
                     System.getProperty("paladin.test.stellar.networkPassphrase", "Standalone Network ; February 2017"),
                     System.getProperty("paladin.test.stellar.pollInterval", "1s")
             );
         };
     }
 
     private void start() throws Exception {
         final File configFile = File.createTempFile("paladin-ut-", ".yaml");
         Files.writeString(configFile.toPath(), yamlConfigMerged);
 
         // Kick off the load in the background
         setMainRun(CompletableFuture.supplyAsync(() -> Main.run(new String[]{
                 configFile.getAbsolutePath(),
                 "testbed",
         })));
 
         // Spin trying to connect to the RPC endpoint
         long startTime = System.currentTimeMillis();
         boolean connected = false;
         while (!connected) {
             long timeStarting = System.currentTimeMillis() - startTime;
             if (timeStarting > testbedSetup.startTimeoutMS) {
                 throw new TimeoutException("timed out start after %dms".formatted(timeStarting));
             }
             Thread.sleep(250);
 
             rpcClient = new JsonRpcClient("http://127.0.0.1:%d".formatted(availableRPCPort));
             try {
                 List<String> domains = rpcClient.request("testbed_listDomains");
                 if (domains.size() != configuredDomains.length) {
                     throw new IllegalStateException("expected %d domains, found %d".formatted(configuredDomains.length, domains.size()));
                 }
                 connected = true;
             } catch (IOException e) {
                 System.err.printf("Waiting to connect: %s\n", e);
                 rpcClient.close();
                 rpcClient = null;
             }
         }
     }
 
     private synchronized void setMainRun(CompletableFuture<Integer> mainRun) {
         this.mainRun = mainRun;
     }
 
     private synchronized CompletableFuture<Integer> getMainRun() {
         return this.mainRun;
     }
 
     public void stop() throws ExecutionException, InterruptedException {
         LOGGER.info("Stopping testbed");
         CompletableFuture<Integer> mainRun = getMainRun();
         if (mainRun != null) {
             Main.stop();
             int exitRC = mainRun.get();
             if (exitRC != 0) {
                 throw new IllegalStateException("failed with RC=%d".formatted(exitRC));
             }
         }
     }
 
     public void close() {
         try {
             stop();
             if (rpcClient != null) {
                 rpcClient.close();
             }
         } catch (Exception e) {
             throw new RuntimeException(e);
         }
     }
 
     public JsonRpcClient getRpcClient() {
         return rpcClient;
     }
 }
 
 