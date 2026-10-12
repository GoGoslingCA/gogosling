"""引擎与宿主之间的契约。

- :class:`Host`：引擎能做的全部外部动作——发 ``run.event``、申请 ``host.tool.call``；
- :class:`Engine`：一次运行的执行者，``run()`` 必须在返回前发出恰好一个终态事件
  （``run.completed`` / ``run.failed`` / ``run.cancelled``）；即便它没做到，服务层也会兜底补发。
- :class:`RunCancelled`：守护进程以 ``-32003`` 结束了反向请求，说明运行已被取消；
  引擎看到它与看到 ``asyncio.CancelledError`` 的处理相同——停止一切并发 ``run.cancelled``。
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Protocol

from gogosling_worker.protocol import Message, RunRequest, ToolCall, ToolResult, Usage
from gogosling_worker.providers.base import Provider


class RunCancelled(Exception):
    """运行已被守护进程取消（反向请求以 -32003 结束）。"""


class Host(Protocol):
    """引擎可用的宿主能力。"""

    @property
    def locale(self) -> str:
        """文案语言，来自 ``worker.hello``。"""
        ...

    def emit(
        self,
        event_type: str,
        *,
        delta: str | None = None,
        tool_call: ToolCall | None = None,
        tool_result: ToolResult | None = None,
        usage: Usage | None = None,
        message: Message | None = None,
        error: str | None = None,
    ) -> None:
        """发出一条 ``run.event``；终态之后的事件会被忽略。"""
        ...

    async def call_tool(self, call: ToolCall) -> ToolResult:
        """申请守护进程执行工具调用；可能等待很久，被取消时抛 :class:`RunCancelled`。"""
        ...


class Engine(ABC):
    """一次运行的执行者。"""

    @abstractmethod
    async def run(self, request: RunRequest, host: Host, provider: Provider) -> None:
        """执行推理循环直到终态。被取消时应先发 ``run.cancelled`` 再把取消向上传播。"""
