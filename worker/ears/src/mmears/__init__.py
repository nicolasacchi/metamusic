"""mmears: the Tier B worker stage (contract mm.ears-b/1)."""

CONTRACT = "mm.ears-b/1"
__version__ = "0.1.0"

import sys

# Isolation from yue2 (C333): the ears interpreter layers /opt/ears-site over /opt/venv, where yue2's transformers lives.
# ctranslate2/converters/transformers.py (line 14 in 4.6.0) does `try: import transformers ... except ImportError: pass` at
# module level, and `import ctranslate2` imports that module, so without this block the ears process would load transformers
# opportunistically. A None entry makes `import transformers` raise ImportError, which that try/except already handles.
sys.modules.setdefault("transformers", None)  # type: ignore[arg-type]
