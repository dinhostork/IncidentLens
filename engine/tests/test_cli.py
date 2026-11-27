import pytest

from incidentlens import __version__
from incidentlens.cli import main


def test_no_args_prints_version(capsys: pytest.CaptureFixture[str]) -> None:
    assert main([]) == 0
    assert capsys.readouterr().out.strip() == f"incidentlens {__version__}"


def test_version_flag(capsys: pytest.CaptureFixture[str]) -> None:
    with pytest.raises(SystemExit) as exc:
        main(["--version"])
    assert exc.value.code == 0
    assert capsys.readouterr().out.strip() == f"incidentlens {__version__}"


def test_scenario_run_unknown_scenario(capsys: pytest.CaptureFixture[str]) -> None:
    assert main(["scenario", "run", "no-such-scenario"]) == 2
    assert "no scenario 'no-such-scenario'" in capsys.readouterr().err


def test_scenario_run_refuses_a_deploy_to_the_running_version(
    capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    from incidentlens.scenario import demo
    from incidentlens.scenario.runner import StackInfo

    def check(self: object) -> StackInfo:
        return StackInfo("demo", {}, {"payments": "1.0.1"})

    def touched(*args: object) -> None:
        raise AssertionError("the run went ahead")

    monkeypatch.setattr(demo.ComposeStack, "check", check)
    monkeypatch.setattr(demo.DemoFlags, "on", touched)
    monkeypatch.setattr(demo.ComposeStack, "stop", touched)
    assert main(["scenario", "run", "payments-bad-deploy"]) == 2
    assert "payments already runs 1.0.1" in capsys.readouterr().err


def test_scenario_run_bad_override(capsys: pytest.CaptureFixture[str]) -> None:
    with pytest.raises(SystemExit) as exc:
        main(["scenario", "run", "payments-db-latency", "--warmup", "1h"])
    assert exc.value.code == 2
    assert "--warmup: want whole seconds or minutes" in capsys.readouterr().err
