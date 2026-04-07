# Example: Private T-REX (ERC-3643 + Zeto AENKNR-E)

Institutional privacy demo combining public compliance (T-REX) with shielded transfers (Zeto). Shows KYC enforcement, account freezing, regulatory decryption, and asset seizure — all in a 17-step scenario.

See [ARCHITECTURE.md](ARCHITECTURE.md) for system design and [DOCKER_RUNBOOK.md](DOCKER_RUNBOOK.md) for Docker deployment.

## Quick Start (Docker)

```shell
./start.sh          # setup: infra, build, deploy, start
./start.sh --demo   # run the 17-step demo
./start.sh --clean  # tear down
```

## Manual Setup

```shell
# Build SDK and deps
cd ../../sdk/typescript && npm install && npm run build
cd ../../examples/common && npm install && npm run build
cd ../../examples/private-trex && npm install

# Run (requires Paladin running with Zeto domain configured)
npm run start
```

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — Domain plugins, factory pattern, AENKNR-E compliance features
- [DOCKER_RUNBOOK.md](DOCKER_RUNBOOK.md) — Docker deployment quick start
- [BALANCE_BUG_ANALYSIS.md](BALANCE_BUG_ANALYSIS.md) — Post-seizure balance tracking (fixed)
- [DECRYPTION_ARCHITECTURE.md](DECRYPTION_ARCHITECTURE.md) — Arbiter/enforcer key management
- [DEMO.md](DEMO.md) — Product/UX specification (aspirational — current implementation is CLI only)
