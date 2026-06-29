# Example: Private T-REX (ERC-3643 + Zeto AENKNR-E)

Institutional privacy demo combining public compliance (T-REX) with shielded transfers (Zeto). Shows KYC enforcement, account freezing, regulatory decryption, and asset seizure — all in a 17-step scenario.

Read [ARCHITECTURE.md](ARCHITECTURE.md) to understand how Paladin loads domain plugins, why a two-phase deployment is needed for custom token types like AENKNR-E, and what the contract stack looks like.

## Prerequisites

- Docker running
- `circom` installed (`cargo install circom`) — only needed on first run for circuit generation
- Node.js 18+

## Quick Start

From this directory (`examples/private-trex`):

```bash
./start.sh          # full setup from scratch → ready for demo
./start.sh --demo   # run the 17-step demo
./start.sh --clean  # tear down everything
```

`start.sh` handles everything: cloning the zeto repo alongside paladin, generating ZKP circuit artifacts, building the Docker image, starting Besu + Postgres, deploying contracts (Phase 1), and starting Paladin with the Zeto domain (Phase 2).

First run takes 20-30 minutes (circuit generation + Docker build). Subsequent runs reuse cached artifacts.

## Viewing Logs

| What | Command |
|------|---------|
| Demo output | Prints to terminal when you run `--demo` |
| Paladin node | `docker logs -f paladin-node1` |
| Besu | `docker compose -f ../../testinfra/docker-compose-test.yml logs -f besu_free` |
| Postgres | `docker logs -f paladin-postgres` |

## Individual Steps

For debugging or re-running specific phases:

```bash
./start.sh --clone      # clone zeto + paladin repos (if not present)
./start.sh --circuits   # generate ZKP circuit artifacts (if not present)
./start.sh --infra      # start Besu + Postgres
./start.sh --build      # build Docker image + TypeScript deps
./start.sh --deploy     # Phase 1: deploy contracts (no domain)
./start.sh --start      # Phase 2: start Paladin with Zeto domain
```

To force a Docker image rebuild: `docker rmi paladin:test` then `./start.sh --build`.

## Manual Setup (without Docker)

```bash
# Build SDK and deps
cd ../../sdk/typescript && npm install && npm run build
cd ../../examples/common && npm install && npm run build
cd ../../examples/private-trex && npm install

# Run (requires Paladin running with Zeto domain configured)
npm run start
```
