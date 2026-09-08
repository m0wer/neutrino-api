#!/usr/bin/env python3
"""Exercise persisted single-UTXO verification across an offline regtest reorg."""

from __future__ import annotations

import json
import subprocess
import sys
import time
import uuid
from decimal import Decimal
from pathlib import Path
from typing import Any, Callable
from urllib.parse import urlencode


ROOT = Path(__file__).resolve().parents[1]
COMPOSE_FILE = ROOT / "docker-compose.reorg-test.yml"
COIN = 100_000_000
COMMAND_TIMEOUT_SECONDS = 90
READY_TIMEOUT_SECONDS = 120
RESCAN_TIMEOUT_SECONDS = 120
HTTP_TIMEOUT_SECONDS = 30
POLL_SECONDS = 1
PAYMENT_AMOUNT = Decimal("1.00000000")
SPEND_FEE = Decimal("0.00010000")


class RegressionFailure(RuntimeError):
    """Raised when the isolated reorg regression does not meet its contract."""


class ReorgRegression:
    """Owns one Docker Compose project and the regression scenario within it."""

    def __init__(self) -> None:
        self.project = f"neutrino-utxo-reorg-{uuid.uuid4().hex[:12]}"
        self.started = False

    def compose_command(self, *args: str) -> list[str]:
        return [
            "docker",
            "compose",
            "--env-file",
            "/dev/null",
            "-p",
            self.project,
            "-f",
            str(COMPOSE_FILE),
            *args,
        ]

    def run(self, command: list[str], timeout: int = COMMAND_TIMEOUT_SECONDS) -> str:
        try:
            completed = subprocess.run(
                command,
                cwd=ROOT,
                check=False,
                capture_output=True,
                text=True,
                timeout=timeout,
            )
        except FileNotFoundError as exc:
            raise RegressionFailure(f"required executable is unavailable: {command[0]}") from exc
        except subprocess.TimeoutExpired as exc:
            raise RegressionFailure(f"command timed out after {timeout}s: {' '.join(command)}") from exc

        if completed.returncode != 0:
            output = (completed.stdout + completed.stderr).strip()
            raise RegressionFailure(
                f"command failed ({completed.returncode}): {' '.join(command)}\n{output[-4000:]}"
            )
        return completed.stdout.strip()

    def compose(self, *args: str, timeout: int = COMMAND_TIMEOUT_SECONDS) -> str:
        return self.run(self.compose_command(*args), timeout)

    def core(self, *args: str, wallet: str | None = None) -> Any:
        command = [
            *self.compose_command(
                "exec",
                "-T",
                "bitcoin",
                "bitcoin-cli",
                "-chain=regtest",
                "-rpcport=18443",
                "-rpcuser=test",
                "-rpcpassword=test",
            ),
        ]
        if wallet is not None:
            command.append(f"-rpcwallet={wallet}")
        command.extend(args)
        output = self.run(command)
        try:
            return json.loads(output, parse_float=Decimal)
        except json.JSONDecodeError:
            # bitcoin-cli renders scalar RPC results (hashes, txids, and
            # addresses) as plain text, while no-result RPCs produce no output.
            # Object and array results remain JSON.
            return output or None

    def api(
        self,
        method: str,
        path: str,
        payload: dict[str, Any] | None = None,
        query: dict[str, str | int | bool] | None = None,
    ) -> Any:
        url = f"http://127.0.0.1:8334{path}"
        if query:
            url = f"{url}?{urlencode(query)}"
        command = [*self.compose_command("exec", "-T", "neutrino", "wget", "-qO-")]
        if payload is not None:
            command.extend(
                [
                    "--header=Content-Type: application/json",
                    f"--post-data={json.dumps(payload)}",
                ]
            )
        command.append(url)
        try:
            raw = self.run(command, timeout=HTTP_TIMEOUT_SECONDS)
        except RegressionFailure as exc:
            raise RegressionFailure(f"API {method} {path} failed: {exc}") from exc
        try:
            return json.loads(raw, parse_float=Decimal)
        except json.JSONDecodeError as exc:
            raise RegressionFailure(f"API {method} {path} did not return JSON: {raw}") from exc

    def wait_for(self, description: str, timeout: int, condition: Callable[[], Any]) -> Any:
        deadline = time.monotonic() + timeout
        last_error = "condition was false"
        while time.monotonic() < deadline:
            try:
                result = condition()
                if result:
                    return result
                last_error = "condition was false"
            except RegressionFailure as exc:
                last_error = str(exc)
            time.sleep(POLL_SECONDS)
        raise RegressionFailure(f"timed out waiting for {description}: {last_error}")

    def wait_for_core(self) -> dict[str, Any]:
        def core_ready() -> dict[str, Any] | None:
            info = self.core("getblockchaininfo")
            return info if info.get("chain") == "regtest" else None

        return self.wait_for("isolated Core regtest RPC", READY_TIMEOUT_SECONDS, core_ready)

    def wait_for_neutrino_tip(self, height: int, block_hash: str) -> dict[str, Any]:
        def tip_matches() -> dict[str, Any] | None:
            status = self.api("GET", "/v1/status")
            if status.get("synced") is not True or status.get("block_height") != height:
                return None
            header = self.api("GET", f"/v1/block/{height}/header")
            if header.get("hash") != block_hash:
                return None
            return status

        return self.wait_for(
            f"Neutrino height {height} and block hash {block_hash}", READY_TIMEOUT_SECONDS, tip_matches
        )

    def wait_for_rescan(self, expected_tip: int | None = None) -> dict[str, Any]:
        def completed() -> dict[str, Any] | None:
            status = self.api("GET", "/v1/rescan/status")
            if status.get("in_progress"):
                return None
            if status.get("last_finished", 0) == 0:
                return None
            if status.get("last_error", ""):
                raise RegressionFailure(f"forced rescan failed: {status['last_error']}")
            if expected_tip is not None and status.get("last_scanned_tip") != expected_tip:
                return None
            return status

        return self.wait_for("watched-address rescan", RESCAN_TIMEOUT_SECONDS, completed)

    def assert_neutrino_regtest(self, core_genesis_hash: str) -> None:
        self.compose(
            "exec",
            "-T",
            "neutrino",
            "sh",
            "-ec",
            'test "$NETWORK" = regtest && test "$NO_AUTH" = true && test "$ADD_PEERS" = bitcoin:18444',
        )

        def regtest_header() -> bool:
            status = self.api("GET", "/v1/status")
            if "block_height" not in status or "filter_height" not in status or "synced" not in status:
                raise RegressionFailure(f"Neutrino status is missing sync fields: {status}")
            header = self.api("GET", "/v1/block/0/header")
            return header.get("hash") == core_genesis_hash

        self.wait_for("Neutrino regtest genesis header", READY_TIMEOUT_SECONDS, regtest_header)

    def cleanup(self) -> None:
        if not self.started:
            return
        self.compose("down", "-v", "--remove-orphans", timeout=COMMAND_TIMEOUT_SECONDS)
        self.started = False
        print(f"CLEANUP project={self.project} volumes=removed")

    def failure_logs(self) -> None:
        try:
            output = self.run(self.compose_command("logs", "--no-color"), timeout=30)
        except RegressionFailure as exc:
            print(f"Unable to collect project logs: {exc}", file=sys.stderr)
            return
        if output:
            print(f"Owned project logs ({self.project}):\n{output}", file=sys.stderr)


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RegressionFailure(message)


def satoshis(amount: Any) -> int:
    decimal_amount = amount if isinstance(amount, Decimal) else Decimal(str(amount))
    result = decimal_amount * COIN
    require(result == result.to_integral_value(), f"amount is not a whole number of satoshis: {amount}")
    return int(result)


def json_amount(amount: Decimal) -> str:
    require(amount >= 0, f"negative Bitcoin amount: {amount}")
    rendered = format(amount, "f")
    require(Decimal(rendered) == amount, f"amount lost precision during JSON conversion: {amount}")
    return rendered


def locate_output(transaction: dict[str, Any], address: str) -> tuple[int, int, Decimal]:
    matches: list[tuple[int, int, Decimal]] = []
    for output in transaction.get("vout", []):
        script = output.get("scriptPubKey", {})
        addresses = [script.get("address"), *script.get("addresses", [])]
        if address in addresses:
            amount = output.get("value")
            matches.append((int(output["n"]), satoshis(amount), Decimal(str(amount))))
    require(len(matches) == 1, f"expected one payment output for watched address, got {matches}")
    return matches[0]


def run_scenario(regression: ReorgRegression) -> dict[str, Any]:
    regression.started = True
    regression.compose("up", "-d", "--build", timeout=360)
    core_info = regression.wait_for_core()
    require(core_info.get("chain") == "regtest", f"Core did not report regtest: {core_info}")
    core_genesis_hash = regression.core("getblockhash", "0")
    regression.assert_neutrino_regtest(core_genesis_hash)

    wallet = "utxo-reorg"
    regression.core("createwallet", wallet, "false", "false", "", "false", "true", "false")
    wallet_info = regression.core("getwalletinfo", wallet=wallet)
    require(wallet_info.get("descriptors") is True, f"throwaway wallet is not descriptor based: {wallet_info}")
    mining_address = regression.core("getnewaddress", "", "bech32", wallet=wallet)
    watched_address = regression.core("getnewaddress", "", "bech32", wallet=wallet)
    regression.core("generatetoaddress", "101", mining_address, wallet=wallet)

    payment_txid = regression.core("sendtoaddress", watched_address, json_amount(PAYMENT_AMOUNT), wallet=wallet)
    payment_block_hash = regression.core("generatetoaddress", "1", mining_address, wallet=wallet)[0]
    payment_height = int(regression.core("getblockcount"))
    require(
        regression.core("getblockhash", str(payment_height)) == payment_block_hash,
        "payment was not mined at the expected height",
    )
    old_tip_hash = regression.core("generatetoaddress", "1", mining_address, wallet=wallet)[0]
    old_tip_height = int(regression.core("getblockcount"))
    require(old_tip_height == payment_height + 1, "old branch was not extended by one block")
    require(
        regression.core("getblockhash", str(old_tip_height)) == old_tip_hash,
        "old branch tip hash does not match Core",
    )

    regression.wait_for_neutrino_tip(old_tip_height, old_tip_hash)
    forced_rescan = regression.api(
        "POST",
        "/v1/rescan",
        {"start_height": payment_height, "addresses": [watched_address], "force": True},
    )
    require(forced_rescan.get("status") == "started", f"forced rescan was not admitted: {forced_rescan}")
    regression.wait_for_rescan()
    initial_rescan = regression.api(
        "POST",
        "/v1/rescan",
        {"start_height": payment_height, "addresses": [], "force": False},
    )
    require(initial_rescan.get("status") == "started", f"initial global rescan was not admitted: {initial_rescan}")
    initial_coverage = regression.wait_for_rescan(expected_tip=old_tip_height)
    require(
        initial_coverage.get("last_start_height") == payment_height,
        f"initial global rescan did not preserve its start height: {initial_coverage}",
    )

    payment_tx = regression.core("getrawtransaction", payment_txid, "true")
    payment_vout, payment_sats, payment_value = locate_output(payment_tx, watched_address)
    require(payment_value == PAYMENT_AMOUNT, f"payment amount changed: {payment_value}")
    cached = regression.api(
        "POST", "/v1/utxos", {"addresses": [watched_address], "include_mempool": False}
    )
    cached_matches = [
        utxo
        for utxo in cached.get("utxos", [])
        if utxo.get("txid") == payment_txid and int(utxo.get("vout", -1)) == payment_vout
    ]
    require(len(cached_matches) == 1, f"cached target UTXO was not found exactly once: {cached}")
    cached_utxo = cached_matches[0]
    require(cached_utxo.get("value") == payment_sats, f"cached value changed: {cached_utxo}")
    require(cached_utxo.get("height") == payment_height, f"cached height changed: {cached_utxo}")
    require(
        cached_utxo.get("verified_tip_height") == old_tip_height,
        f"cached UTXO was not anchored at old tip height: {cached_utxo}",
    )
    require(
        cached_utxo.get("verified_tip_hash") == old_tip_hash,
        f"cached UTXO was not anchored at old tip hash: {cached_utxo}",
    )
    unspent = regression.api(
        "GET",
        f"/v1/utxo/{payment_txid}/{payment_vout}",
        query={"address": watched_address, "start_height": payment_height, "include_mempool": False},
    )
    require(unspent.get("unspent") is True, f"old branch single-UTXO lookup was not unspent: {unspent}")
    require(unspent.get("value") == payment_sats, f"old branch UTXO value changed: {unspent}")

    regression.compose("stop", "neutrino")
    regression.core("invalidateblock", old_tip_hash)
    require(int(regression.core("getblockcount")) == payment_height, "old tip invalidation did not return to H")
    spend_address = regression.core("getnewaddress", "", "bech32", wallet=wallet)
    spend_amount = payment_value - SPEND_FEE
    require(spend_amount > 0, "spend fee exceeds the watched payment")
    raw_spend = regression.core(
        "createrawtransaction",
        json.dumps([{"txid": payment_txid, "vout": payment_vout}]),
        json.dumps({spend_address: str(spend_amount)}),
    )
    signed_spend = regression.core("signrawtransactionwithwallet", raw_spend, wallet=wallet)
    require(signed_spend.get("complete") is True, f"could not sign exact payment spend: {signed_spend}")
    spending_txid = regression.core("sendrawtransaction", signed_spend["hex"])
    replacement_block_hash = regression.core("generatetoaddress", "1", mining_address, wallet=wallet)[0]
    replacement_height = int(regression.core("getblockcount"))
    require(replacement_height == old_tip_height, "replacement branch did not restore H+1")
    require(replacement_block_hash != old_tip_hash, "same-height replacement block did not differ")
    require(
        regression.core("getblockhash", str(replacement_height)) == replacement_block_hash,
        "replacement spending block is not Core's current best block",
    )
    spending_tx = regression.core("getrawtransaction", spending_txid, "true")
    require(
        spending_tx.get("blockhash") == replacement_block_hash,
        "exact payment spend was not mined in the replacement block",
    )
    adopted_tip_hash = regression.core("generatetoaddress", "1", mining_address, wallet=wallet)[0]
    adopted_tip_height = int(regression.core("getblockcount"))
    require(adopted_tip_height == replacement_height + 1, "replacement branch did not gain greater work")
    require(
        regression.core("getblockhash", str(adopted_tip_height)) == adopted_tip_hash,
        "greater-work branch is not Core's current best chain",
    )

    regression.compose("start", "neutrino")
    regression.wait_for_neutrino_tip(adopted_tip_height, adopted_tip_hash)
    incremental_rescan = regression.api(
        "POST",
        "/v1/rescan",
        {"start_height": payment_height, "addresses": [watched_address], "force": False},
    )
    require(
        incremental_rescan.get("status") == "started",
        f"incremental rescan was not admitted: {incremental_rescan}",
    )
    incremental_coverage = regression.wait_for_rescan(expected_tip=adopted_tip_height)
    require(
        incremental_coverage.get("last_start_height") == payment_height,
        f"incremental rescan changed the global scan start: {incremental_coverage}",
    )
    stale_cached = regression.api(
        "POST", "/v1/utxos", {"addresses": [watched_address], "include_mempool": False}
    )
    stale_matches = [
        utxo
        for utxo in stale_cached.get("utxos", [])
        if utxo.get("txid") == payment_txid and int(utxo.get("vout", -1)) == payment_vout
    ]
    require(len(stale_matches) == 1, f"suffix scan unexpectedly removed cached target UTXO: {stale_cached}")
    stale_utxo = stale_matches[0]
    require(
        stale_utxo.get("verified_tip_height") == old_tip_height,
        f"suffix scan re-certified stale UTXO height: {stale_utxo}",
    )
    require(
        stale_utxo.get("verified_tip_hash") == old_tip_hash,
        f"suffix scan re-certified stale UTXO hash: {stale_utxo}",
    )
    spent = regression.api(
        "GET",
        f"/v1/utxo/{payment_txid}/{payment_vout}",
        query={"address": watched_address, "start_height": payment_height, "include_mempool": False},
    )
    require(spent.get("unspent") is False, f"stale persisted UTXO cache was trusted: {spent}")
    require(
        spent.get("spending_txid") == spending_txid,
        f"single-UTXO lookup did not report the alternate-branch spend: {spent}",
    )
    require(
        spent.get("spending_height") == replacement_height,
        f"single-UTXO lookup reported the wrong spending height: {spent}",
    )
    return {
        "project": regression.project,
        "payment_height": payment_height,
        "old_tip_height": old_tip_height,
        "old_tip_hash": old_tip_hash,
        "replacement_height": replacement_height,
        "replacement_block_hash": replacement_block_hash,
        "adopted_tip_height": adopted_tip_height,
        "adopted_tip_hash": adopted_tip_hash,
        "payment_txid": payment_txid,
        "spending_txid": spending_txid,
    }


def main() -> int:
    regression = ReorgRegression()
    try:
        result = run_scenario(regression)
    except BaseException:
        regression.failure_logs()
        try:
            regression.cleanup()
        except RegressionFailure as cleanup_error:
            print(f"Owned project cleanup failed: {cleanup_error}", file=sys.stderr)
        raise
    else:
        regression.cleanup()
    print(
        "PASS "
        f"project={result['project']} payment_height={result['payment_height']} "
        f"old_tip={result['old_tip_height']}:{result['old_tip_hash']} "
        f"replacement={result['replacement_height']}:{result['replacement_block_hash']} "
        f"adopted_tip={result['adopted_tip_height']}:{result['adopted_tip_hash']} "
        f"payment_txid={result['payment_txid']} spending_txid={result['spending_txid']}"
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RegressionFailure as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
