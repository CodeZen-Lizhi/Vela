"""用同提交上游 Python 评分器重新计算当前 bank/bin 的固定期望；仅需 numpy，不加载网络数据模块。
用法：python3 testdata/generate_snapshot.py /path/to/upstream-checkout
"""
import importlib
import json
from pathlib import Path
import sys
import types

upstream = Path(sys.argv[1])
root = Path(__file__).resolve().parent.parent
package = types.ModuleType("lmfpd")
package.__path__ = [str(upstream / "sdk/python/src/lmfpd")]
sys.modules["lmfpd"] = package
Scorer = importlib.import_module("lmfpd._analysis").Scorer
decode_detector = importlib.import_module("lmfpd._codec").decode_detector
bank = json.loads((root / "data/unified_bank.json").read_text())
detector = decode_detector((root / "data/shared_detector.bin").read_bytes())
scorer = Scorer(bank, detector)
fixture = json.loads((root / "testdata/conformance/analyze.json").read_text())
fixture["data"]["built_at"] = bank["built_at"]
for case in fixture["cases"]:
    case["expected"] = scorer.analyze(case["outputs"], case["strict"])
(root / "testdata/analyze-snapshot.json").write_text(json.dumps(fixture, ensure_ascii=False, indent=2) + "\n")
