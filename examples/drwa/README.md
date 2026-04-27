## DRWA Full-Stack Regression Harness

This example is the DRWA-oriented starting point for a single executable scenario
that drives the stack through:

1. simulator funding/bootstrap
2. DRWA contract transaction construction and submission
3. block generation until completion
4. proxy transaction inspection
5. API DRWA endpoint inspection

The script now supports two modes:

1. **Preferred:** build and submit the DRWA transactions itself using
   `mx-sdk-py` builders.
2. **Legacy fallback:** accept prebuilt transaction hashes if you already have
   an external transaction generator.

The script is intentionally environment-driven because contract addresses,
authorized senders, token balances, and denial-path parameters depend on the
deployment under test.

### Preferred mode: build and submit transactions

Required environment variables:

- `SIMULATOR_URL`
- `PROXY_URL`
- `API_URL`
- `DRWA_TOKEN_ID`
- `DRWA_HOLDER`
- `DRWA_POLICY_SENDER`
- `DRWA_POLICY_CONTRACT`
- `DRWA_SYNC_SENDER`
- `DRWA_SYNC_CONTRACT`
- `DRWA_DENIAL_RECEIVER`

Optional knobs:

- `DRWA_FAUCET_AMOUNT`
- `DRWA_POLICY_ENABLED`
- `DRWA_GLOBAL_PAUSE`
- `DRWA_STRICT_AUDITOR_MODE`
- `DRWA_METADATA_PROTECTION_ENABLED`
- `DRWA_ALLOWED_INVESTOR_CLASSES`
- `DRWA_ALLOWED_JURISDICTIONS`
- `DRWA_KYC_STATUS`
- `DRWA_AML_STATUS`
- `DRWA_INVESTOR_CLASS`
- `DRWA_JURISDICTION_CODE`
- `DRWA_EXPIRY_ROUND`
- `DRWA_TRANSFER_LOCKED`
- `DRWA_RECEIVE_LOCKED`
- `DRWA_AUDITOR_AUTHORIZED`
- `DRWA_DENIAL_AMOUNT`
- `DRWA_POLICY_GAS_LIMIT`
- `DRWA_SYNC_GAS_LIMIT`
- `DRWA_DENIAL_GAS_LIMIT`

### Legacy fallback mode

If all three hashes are provided, the script will skip transaction construction
and only perform the verification steps:

- `DRWA_POLICY_TX`
- `DRWA_SYNC_TX`
- `DRWA_DENIAL_TX`

### Example

```sh
python3 ./full_stack_regression.py
```

### Unit validation

```sh
python3 -m unittest full_stack_regression_test.py
```

### Optional attestation assertion

- set `DRWA_ATTESTATION_SUBJECT` to require that `/drwa/attestations/{tokenId}` contains the expected subject

### Expected assertions

- policy transaction completes successfully
- sync transaction completes successfully
- denial transaction is visible through proxy materialization
- DRWA API token policy endpoint responds
- DRWA API holder compliance endpoint responds
- DRWA API denial history endpoint contains the denial code
