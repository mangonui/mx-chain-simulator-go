import os
import sys
from typing import Any, Dict, List
from pathlib import Path

import requests

workspace_root = Path(__file__).resolve().parents[3]
sdk_py_root = workspace_root / "mx-sdk-py"
if sdk_py_root.exists():
    sys.path.insert(0, str(sdk_py_root))

from multiversx_sdk import (  # type: ignore
    Address,
    ProxyNetworkProvider,
    Token,
    TokenTransfer,
    TransactionsFactoryConfig,
    TransferTransactionsFactory,
)
from multiversx_sdk.drwa import build_set_token_policy_data, build_sync_holder_compliance_data  # type: ignore

SIMULATOR_URL = os.getenv("SIMULATOR_URL", "http://localhost:8085")
PROXY_URL = os.getenv("PROXY_URL", SIMULATOR_URL)
API_URL = os.getenv("API_URL", "http://localhost:3000")

GENERATE_BLOCKS_UNTIL_TX_PROCESSED = "simulator/generate-blocks-until-transaction-processed"


def require_env(name: str) -> str:
    value = os.getenv(name)
    if not value:
        sys.exit(f"missing required environment variable: {name}")
    return value


def env_bool(name: str, default: bool = False) -> bool:
    value = os.getenv(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "y", "on"}


def env_int(name: str, default: int) -> int:
    value = os.getenv(name)
    if value is None or value == "":
        return default
    return int(value)


def env_list(name: str) -> list[str]:
    value = os.getenv(name, "")
    return [item.strip() for item in value.split(",") if item.strip()]


def require_address(name: str) -> Address:
    return Address.new_from_bech32(require_env(name))


def post_json(base_url: str, path: str, payload: Dict[str, Any]) -> Dict[str, Any]:
    response = requests.post(f"{base_url.rstrip('/')}/{path.lstrip('/')}", json=payload, timeout=30)
    response.raise_for_status()
    return response.json()


def get_json(base_url: str, path: str) -> Dict[str, Any]:
    response = requests.get(f"{base_url.rstrip('/')}/{path.lstrip('/')}", timeout=30)
    response.raise_for_status()
    return response.json()


def wait_for_transaction(tx_hash: str) -> None:
    post_json(SIMULATOR_URL, f"{GENERATE_BLOCKS_UNTIL_TX_PROCESSED}/{tx_hash}", {})


def prepare_transaction(provider: ProxyNetworkProvider, transaction: Any, sender: Address) -> None:
    transaction.nonce = provider.get_account(sender).nonce
    transaction.signature = b"dummy"


def fund_address_if_requested(provider: ProxyNetworkProvider, address: Address) -> None:
    amount = env_int("DRWA_FAUCET_AMOUNT", 0)
    if amount <= 0:
        return
    provider.do_post_generic(
        "transaction/send-user-funds",
        {"receiver": address.to_bech32(), "value": amount},
    )
    provider.do_post_generic("simulator/generate-blocks/1", {})


def build_policy_transaction(provider: ProxyNetworkProvider) -> Any:
    sender = require_address("DRWA_POLICY_SENDER")
    contract = require_address("DRWA_POLICY_CONTRACT")
    token_id = require_env("DRWA_TOKEN_ID")

    config = TransactionsFactoryConfig(provider.get_network_config().chain_id)
    factory = TransferTransactionsFactory(config)
    tx = factory.create_transaction_for_native_token_transfer(
        sender=sender,
        receiver=contract,
        native_amount=0,
        data=build_set_token_policy_data(
            token_id=token_id,
            drwa_enabled=env_bool("DRWA_POLICY_ENABLED", True),
            global_pause=env_bool("DRWA_GLOBAL_PAUSE", False),
            strict_auditor_mode=env_bool("DRWA_STRICT_AUDITOR_MODE", False),
            metadata_protection_enabled=env_bool("DRWA_METADATA_PROTECTION_ENABLED", True),
            allowed_investor_classes=env_list("DRWA_ALLOWED_INVESTOR_CLASSES"),
            allowed_jurisdictions=env_list("DRWA_ALLOWED_JURISDICTIONS"),
        ),
    )
    tx.gas_limit = env_int("DRWA_POLICY_GAS_LIMIT", 60_000_000)
    prepare_transaction(provider, tx, sender)
    return tx


def build_sync_transaction(provider: ProxyNetworkProvider) -> Any:
    sender = require_address("DRWA_SYNC_SENDER")
    contract = require_address("DRWA_SYNC_CONTRACT")
    holder = require_address("DRWA_HOLDER")
    token_id = require_env("DRWA_TOKEN_ID")

    config = TransactionsFactoryConfig(provider.get_network_config().chain_id)
    factory = TransferTransactionsFactory(config)
    tx = factory.create_transaction_for_native_token_transfer(
        sender=sender,
        receiver=contract,
        native_amount=0,
        data=build_sync_holder_compliance_data(
            token_id=token_id,
            holder=holder,
            kyc_status=os.getenv("DRWA_KYC_STATUS", "approved"),
            aml_status=os.getenv("DRWA_AML_STATUS", "approved"),
            investor_class=os.getenv("DRWA_INVESTOR_CLASS", "QIB"),
            jurisdiction_code=os.getenv("DRWA_JURISDICTION_CODE", "SG"),
            expiry_round=env_int("DRWA_EXPIRY_ROUND", 0),
            transfer_locked=env_bool("DRWA_TRANSFER_LOCKED", False),
            receive_locked=env_bool("DRWA_RECEIVE_LOCKED", False),
            auditor_authorized=env_bool("DRWA_AUDITOR_AUTHORIZED", False),
        ),
    )
    tx.gas_limit = env_int("DRWA_SYNC_GAS_LIMIT", 80_000_000)
    prepare_transaction(provider, tx, sender)
    return tx


def build_denial_transaction(provider: ProxyNetworkProvider) -> Any:
    sender = require_address("DRWA_HOLDER")
    receiver = require_address("DRWA_DENIAL_RECEIVER")
    token_id = require_env("DRWA_TOKEN_ID")
    amount = env_int("DRWA_DENIAL_AMOUNT", 1)

    config = TransactionsFactoryConfig(provider.get_network_config().chain_id)
    factory = TransferTransactionsFactory(config)
    tx = factory.create_transaction_for_transfer(
        sender=sender,
        receiver=receiver,
        token_transfers=[TokenTransfer(Token(token_id), amount)],
    )
    tx.gas_limit = env_int("DRWA_DENIAL_GAS_LIMIT", 30_000_000)
    prepare_transaction(provider, tx, sender)
    return tx


def submit_transaction(provider: ProxyNetworkProvider, transaction: Any) -> str:
    tx_hash = provider.send_transaction(transaction)
    tx_hash_hex = tx_hash.hex()
    wait_for_transaction(tx_hash_hex)
    return tx_hash_hex


def build_and_submit_transactions() -> tuple[str, str, str]:
    provider = ProxyNetworkProvider(SIMULATOR_URL)

    fund_address_if_requested(provider, require_address("DRWA_POLICY_SENDER"))
    fund_address_if_requested(provider, require_address("DRWA_SYNC_SENDER"))
    fund_address_if_requested(provider, require_address("DRWA_HOLDER"))

    policy_tx = submit_transaction(provider, build_policy_transaction(provider))
    sync_tx = submit_transaction(provider, build_sync_transaction(provider))
    denial_tx = submit_transaction(provider, build_denial_transaction(provider))

    return policy_tx, sync_tx, denial_tx


def assert_transaction_processed(tx_hash: str) -> Dict[str, Any]:
    data = get_json(PROXY_URL, f"transaction/{tx_hash}?withResults=true")
    transaction = data.get("data", {}).get("transaction") or data.get("transaction") or data
    if not transaction:
        sys.exit(f"transaction {tx_hash} not found in proxy response")
    return transaction


def assert_denial_materialized(transaction: Dict[str, Any]) -> None:
    drwa = transaction.get("drwa")
    if not drwa or not drwa.get("denialCode"):
        sys.exit("expected DRWA denial materialization on transaction response")


def assert_denial_list_contains(token_id: str, denial_code: str) -> None:
    data = get_json(API_URL, f"drwa/denials?tokenId={token_id}")
    items: List[Dict[str, Any]] = data if isinstance(data, list) else data.get("data", data)
    if not any(item.get("denialCode") == denial_code for item in items):
        sys.exit(f"denial history missing expected denial code {denial_code}")


def assert_attestation_list_contains(token_id: str, subject: str) -> None:
    data = get_json(API_URL, f"drwa/attestations/{token_id}")
    items: List[Dict[str, Any]] = data if isinstance(data, list) else data.get("data", data)
    if not any(item.get("subject") == subject for item in items):
        sys.exit(f"attestation history missing expected subject {subject}")


def assert_policy_and_holder(token_id: str, holder: str) -> None:
    policy = get_json(API_URL, f"drwa/tokens/{token_id}")
    policy_data = policy.get("data", policy) if isinstance(policy, dict) else policy
    if not policy_data.get("regulated"):
        sys.exit("expected regulated token policy response")

    holder_state = get_json(API_URL, f"drwa/accounts/{holder}/tokens/{token_id}")
    holder_data = holder_state.get("data", holder_state) if isinstance(holder_state, dict) else holder_state
    if holder_data.get("holder") != holder:
        sys.exit("holder compliance endpoint did not return the requested holder")


def run_scenario(
    policy_tx: str,
    sync_tx: str,
    denial_tx: str,
    token_id: str,
    holder: str,
    denial_code: str,
    attestation_subject: str | None = None,
) -> None:
    for tx_hash in [policy_tx, sync_tx, denial_tx]:
        wait_for_transaction(tx_hash)

    assert_transaction_processed(policy_tx)
    assert_transaction_processed(sync_tx)
    denial_transaction = assert_transaction_processed(denial_tx)
    assert_denial_materialized(denial_transaction)
    assert_policy_and_holder(token_id, holder)
    assert_denial_list_contains(token_id, denial_code)

    if attestation_subject:
        assert_attestation_list_contains(token_id, attestation_subject)


def main() -> None:
    token_id = require_env("DRWA_TOKEN_ID")
    holder = require_env("DRWA_HOLDER")
    denial_code = os.getenv("DRWA_DENIAL_CODE", "DRWA_KYC_REQUIRED")
    attestation_subject = os.getenv("DRWA_ATTESTATION_SUBJECT")

    if os.getenv("DRWA_POLICY_TX") and os.getenv("DRWA_SYNC_TX") and os.getenv("DRWA_DENIAL_TX"):
        policy_tx = require_env("DRWA_POLICY_TX")
        sync_tx = require_env("DRWA_SYNC_TX")
        denial_tx = require_env("DRWA_DENIAL_TX")
    else:
        policy_tx, sync_tx, denial_tx = build_and_submit_transactions()

    run_scenario(
        policy_tx=policy_tx,
        sync_tx=sync_tx,
        denial_tx=denial_tx,
        token_id=token_id,
        holder=holder,
        denial_code=denial_code,
        attestation_subject=attestation_subject,
    )

    print("DRWA full-stack regression scenario completed")


if __name__ == "__main__":
    main()
