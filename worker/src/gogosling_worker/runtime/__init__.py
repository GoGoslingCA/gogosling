"""智能体运行时：引擎接口（:mod:`engine`）、默认推理循环（:mod:`loop`）与宿主适配（:mod:`context`）。

引擎只通过 :class:`engine.Host` 与外界交互（发事件、申请工具调用），因此可以在测试里用
假宿主与假供应商驱动，也可以被别的实现替换而不改 RPC 层。

待实现：
- 上下文管理：历史压缩、超长工具结果落盘、子智能体的上下文隔离；
- 检查点：运行可中断、可恢复（当前被取消的运行只能由用户重新发起）；
- 单步超时（当前只有供应商层的连接 / 读超时，没有"整步"上限）。

边界：不直接读写用户文件、不直接执行命令；副作用操作一律通过 ``host.tool.call`` 交给守护进程，
由它完成权限判定与沙箱执行。
"""

from __future__ import annotations

from gogosling_worker.runtime.context import RunContext
from gogosling_worker.runtime.engine import Engine, Host, RunCancelled
from gogosling_worker.runtime.loop import DefaultEngine

__all__ = ["DefaultEngine", "Engine", "Host", "RunCancelled", "RunContext"]
