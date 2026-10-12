"""pytest 配置：让 ``async def`` 测试直接在 asyncio 里运行（不引入 pytest-asyncio）。"""

from __future__ import annotations

import asyncio
import inspect
from typing import Any

import pytest


@pytest.hookimpl(tryfirst=True)
def pytest_pyfunc_call(pyfuncitem: pytest.Function) -> bool | None:
    func = pyfuncitem.obj
    if not inspect.iscoroutinefunction(func):
        return None
    names = pyfuncitem._fixtureinfo.argnames
    kwargs: dict[str, Any] = {name: pyfuncitem.funcargs[name] for name in names}
    asyncio.run(func(**kwargs))
    return True
