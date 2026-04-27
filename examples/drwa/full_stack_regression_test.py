import pathlib
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))

import full_stack_regression as scenario


class FullStackRegressionTests(unittest.TestCase):
    @patch.object(scenario, "run_scenario")
    @patch.object(scenario, "build_and_submit_transactions")
    @patch.dict("os.environ", {"DRWA_TOKEN_ID": "HOTEL-1234", "DRWA_HOLDER": "erd1holder"}, clear=False)
    def test_main_builds_transactions_when_hashes_not_provided(
        self,
        build_and_submit_transactions,
        run_scenario,
    ):
        build_and_submit_transactions.return_value = ("policy", "sync", "denial")

        scenario.main()

        build_and_submit_transactions.assert_called_once()
        run_scenario.assert_called_once()

    @patch.object(scenario, "run_scenario")
    @patch.dict(
        "os.environ",
        {
            "DRWA_POLICY_TX": "policy",
            "DRWA_SYNC_TX": "sync",
            "DRWA_DENIAL_TX": "denial",
            "DRWA_TOKEN_ID": "HOTEL-1234",
            "DRWA_HOLDER": "erd1holder",
        },
        clear=False,
    )
    def test_main_accepts_legacy_prebuilt_hash_mode(self, run_scenario):
        scenario.main()
        run_scenario.assert_called_once()

    @patch.object(scenario, "assert_attestation_list_contains")
    @patch.object(scenario, "assert_denial_list_contains")
    @patch.object(scenario, "assert_policy_and_holder")
    @patch.object(scenario, "assert_denial_materialized")
    @patch.object(scenario, "assert_transaction_processed")
    @patch.object(scenario, "wait_for_transaction")
    def test_run_scenario_orders_checks(
        self,
        wait_for_transaction,
        assert_transaction_processed,
        assert_denial_materialized,
        assert_policy_and_holder,
        assert_denial_list_contains,
        assert_attestation_list_contains,
    ):
        denial_transaction = {"drwa": {"denialCode": "DRWA_KYC_REQUIRED"}}
        assert_transaction_processed.side_effect = [{}, {}, denial_transaction]

        scenario.run_scenario(
            policy_tx="policy",
            sync_tx="sync",
            denial_tx="denial",
            token_id="HOTEL-1234",
            holder="erd1holder",
            denial_code="DRWA_KYC_REQUIRED",
            attestation_subject="erd1subject",
        )

        wait_for_transaction.assert_any_call("policy")
        wait_for_transaction.assert_any_call("sync")
        wait_for_transaction.assert_any_call("denial")
        self.assertEqual(wait_for_transaction.call_count, 3)
        self.assertEqual(assert_transaction_processed.call_count, 3)
        assert_denial_materialized.assert_called_once_with(denial_transaction)
        assert_policy_and_holder.assert_called_once_with("HOTEL-1234", "erd1holder")
        assert_denial_list_contains.assert_called_once_with(
            "HOTEL-1234", "DRWA_KYC_REQUIRED"
        )
        assert_attestation_list_contains.assert_called_once_with(
            "HOTEL-1234", "erd1subject"
        )

    @patch.object(scenario, "get_json")
    def test_denial_list_accepts_wrapped_api_payload(self, get_json):
        get_json.return_value = {"data": [{"denialCode": "DRWA_AML_BLOCKED"}]}
        scenario.assert_denial_list_contains("HOTEL-1234", "DRWA_AML_BLOCKED")

    @patch.object(scenario, "get_json")
    def test_attestation_list_accepts_raw_list_payload(self, get_json):
        get_json.return_value = [{"subject": "erd1subject"}]
        scenario.assert_attestation_list_contains("HOTEL-1234", "erd1subject")

    @patch.object(scenario, "get_json")
    def test_policy_accepts_wrapped_payload(self, get_json):
        get_json.side_effect = [
            {"data": {"regulated": True}},
            {"data": {"holder": "erd1holder"}},
        ]
        scenario.assert_policy_and_holder("HOTEL-1234", "erd1holder")

    @patch.object(scenario, "get_json")
    def test_holder_accepts_wrapped_payload(self, get_json):
        get_json.side_effect = [
            {"regulated": True},
            {"data": {"holder": "erd1holder"}},
        ]
        scenario.assert_policy_and_holder("HOTEL-1234", "erd1holder")

    @patch.object(scenario, "submit_transaction")
    @patch.object(scenario, "build_denial_transaction")
    @patch.object(scenario, "build_sync_transaction")
    @patch.object(scenario, "build_policy_transaction")
    @patch.object(scenario, "fund_address_if_requested")
    @patch.object(scenario, "ProxyNetworkProvider")
    @patch.object(scenario, "require_address")
    def test_build_and_submit_transactions_orders_builds(
        self,
        require_address,
        provider_cls,
        fund_address_if_requested,
        build_policy_transaction,
        build_sync_transaction,
        build_denial_transaction,
        submit_transaction,
    ):
        require_address.side_effect = ["policy-sender", "sync-sender", "holder"]
        provider = provider_cls.return_value
        build_policy_transaction.return_value = object()
        build_sync_transaction.return_value = object()
        build_denial_transaction.return_value = object()
        submit_transaction.side_effect = ["policy", "sync", "denial"]

        result = scenario.build_and_submit_transactions()

        self.assertEqual(result, ("policy", "sync", "denial"))
        self.assertEqual(fund_address_if_requested.call_count, 3)
        build_policy_transaction.assert_called_once_with(provider)
        build_sync_transaction.assert_called_once_with(provider)
        build_denial_transaction.assert_called_once_with(provider)


if __name__ == "__main__":
    unittest.main()
