import PaladinClient, { PaladinVerifier } from "@lfdecentralizedtrust/paladin-sdk";
import { TREXSuite, registerIdentity, setAddressFrozen } from "./trex";
import { getBabyjubPublicKey, registerZetoKyc, setComplianceRoot } from "./helpers";
import { ComplianceSmtManager, STATUS_ACTIVE, STATUS_FROZEN } from "./complianceSmt";

export interface ActorIdentity {
  name: string;
  verifier: PaladinVerifier;
  evmAddress: string;
  babyjubPubKey: string[];
}

export async function resolveActorIdentity(
  name: string,
  verifier: PaladinVerifier,
): Promise<ActorIdentity> {
  return {
    name,
    verifier,
    evmAddress: await verifier.address(),
    babyjubPubKey: await getBabyjubPublicKey(verifier),
  };
}

/**
 * Registers an actor in both T-REX (identity registry) and Zeto (KYC + compliance ACTIVE).
 *
 * Does NOT post the compliance root on-chain — call postComplianceRoot() once
 * after batch onboarding to avoid redundant on-chain transactions.
 */
export async function onboardActor(
  paladin: PaladinClient,
  issuer: PaladinVerifier,
  actor: ActorIdentity,
  trex: TREXSuite,
  zetoAddr: string,
  complianceSmt: ComplianceSmtManager,
): Promise<void> {
  await registerIdentity(paladin, issuer, trex.identityRegistry, trex.dummyIdentity, actor.evmAddress, 756);
  await registerZetoKyc(paladin, issuer, zetoAddr, actor.babyjubPubKey);
  await complianceSmt.setStatus(actor.babyjubPubKey[0], actor.babyjubPubKey[1], STATUS_ACTIVE);
}

/**
 * Posts the current compliance SMT root on-chain.
 *
 * Call once after batch onboarding or after any freeze/unfreeze.
 */
export async function postComplianceRoot(
  paladin: PaladinClient,
  agent: PaladinVerifier,
  zetoAddr: string,
  complianceSmt: ComplianceSmtManager,
): Promise<void> {
  await setComplianceRoot(paladin, agent, zetoAddr, await complianceSmt.getRoot());
}

/**
 * Freezes an actor on both T-REX (address freeze) and Zeto (compliance FROZEN),
 * then posts the updated compliance root on-chain.
 */
export async function freezeActor(
  paladin: PaladinClient,
  agent: PaladinVerifier,
  actor: ActorIdentity,
  trex: TREXSuite,
  zetoAddr: string,
  complianceSmt: ComplianceSmtManager,
): Promise<void> {
  await setAddressFrozen(paladin, agent, trex.token, actor.evmAddress, true);
  await complianceSmt.setStatus(actor.babyjubPubKey[0], actor.babyjubPubKey[1], STATUS_FROZEN);
  await postComplianceRoot(paladin, agent, zetoAddr, complianceSmt);
}

/**
 * Unfreezes an actor on both T-REX and Zeto, then posts the updated compliance root.
 */
export async function unfreezeActor(
  paladin: PaladinClient,
  agent: PaladinVerifier,
  actor: ActorIdentity,
  trex: TREXSuite,
  zetoAddr: string,
  complianceSmt: ComplianceSmtManager,
): Promise<void> {
  await setAddressFrozen(paladin, agent, trex.token, actor.evmAddress, false);
  await complianceSmt.setStatus(actor.babyjubPubKey[0], actor.babyjubPubKey[1], STATUS_ACTIVE);
  await postComplianceRoot(paladin, agent, zetoAddr, complianceSmt);
}