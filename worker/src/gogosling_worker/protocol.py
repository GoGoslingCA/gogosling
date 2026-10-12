"""worker-rpc 协议里的数据形状（``api/worker-rpc/protocol.md``）。

这些类型与 Go 的 ``internal/model`` 一一对应（JSON 为 ``snake_case``）。
解析时宽松：忽略未知字段与未知片段类型，历史消息里不完整的 ``tool_call`` / ``tool_result`` 片段
（例如旧版本写进库里的无名工具调用）记日志后丢弃；输出时严格：只写协议规定的字段，
本进程发出的 ``run.completed.message`` 必须能被本模块的 :func:`parse_run_request` 接受（协议 §3）。
``run.start`` 的参数校验集中在 :func:`parse_run_request`，结构上不合法的输入以 ``-32602`` 拒绝，
而不是在运行中途失败。

密钥只存在于 :class:`ProviderCredentials.api_key`，该字段不进 ``repr``，整个请求对象不得写日志。
"""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Any

from gogosling_worker.rpc import INVALID_PARAMS, JsonRpcError

PROTOCOL_VERSION = "1"

ROLE_SYSTEM = "system"
ROLE_USER = "user"
ROLE_ASSISTANT = "assistant"
ROLE_TOOL = "tool"
ROLES = frozenset({ROLE_SYSTEM, ROLE_USER, ROLE_ASSISTANT, ROLE_TOOL})

PART_TEXT = "text"
PART_TOOL_CALL = "tool_call"
PART_TOOL_RESULT = "tool_result"
PART_ATTACHMENT = "attachment"

EVENT_RUN_STARTED = "run.started"
EVENT_TEXT_DELTA = "text.delta"
# 模型思考内容的增量（供应商支持时才有），只用于界面展示，不进入最终助手消息。
EVENT_REASONING_DELTA = "reasoning.delta"
EVENT_TOOL_CALL_STARTED = "tool.call.started"
EVENT_TOOL_CALL_COMPLETED = "tool.call.completed"
EVENT_USAGE = "usage"
EVENT_RUN_COMPLETED = "run.completed"
EVENT_RUN_FAILED = "run.failed"
EVENT_RUN_CANCELLED = "run.cancelled"
TERMINAL_EVENTS = frozenset({EVENT_RUN_COMPLETED, EVENT_RUN_FAILED, EVENT_RUN_CANCELLED})

PROVIDER_OPENAI_COMPATIBLE = "openai_compatible"
PROVIDER_ANTHROPIC = "anthropic"
PROVIDER_OLLAMA = "ollama"
PROVIDER_KINDS = frozenset({PROVIDER_OPENAI_COMPATIBLE, PROVIDER_ANTHROPIC, PROVIDER_OLLAMA})

DEFAULT_MAX_STEPS = 32

Clock = Callable[[], datetime]

_log = logging.getLogger(__name__)


def now_utc() -> datetime:
    """当前 UTC 时间（带时区）。"""
    return datetime.now(UTC)


def format_time(value: datetime) -> str:
    """按 RFC 3339 格式化（Go 的 time.Time 可直接解析）。"""
    if value.tzinfo is None:
        value = value.replace(tzinfo=UTC)
    return value.astimezone(UTC).isoformat(timespec="microseconds").replace("+00:00", "Z")


@dataclass(frozen=True, slots=True)
class ToolCall:
    """模型发起的一次工具调用；``arguments`` 是任意 JSON 值（通常是对象）。"""

    id: str
    name: str
    arguments: Any = None

    def to_json(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "name": self.name,
            "arguments": self.arguments if self.arguments is not None else {},
        }


@dataclass(frozen=True, slots=True)
class ToolResult:
    """工具调用的结果，会作为 tool 角色消息回填给模型。"""

    call_id: str
    content: str
    is_error: bool = False
    truncated: bool = False

    def to_json(self) -> dict[str, Any]:
        out: dict[str, Any] = {"call_id": self.call_id, "content": self.content}
        if self.is_error:
            out["is_error"] = True
        if self.truncated:
            out["truncated"] = True
        return out


@dataclass(frozen=True, slots=True)
class Attachment:
    """工作区 inbound/ 目录下的一个文件。"""

    name: str
    path: str
    mime: str = ""
    size: int = 0

    def to_json(self) -> dict[str, Any]:
        out: dict[str, Any] = {"name": self.name, "path": self.path, "size": self.size}
        if self.mime:
            out["mime"] = self.mime
        return out


@dataclass(frozen=True, slots=True)
class Part:
    """消息片段；只有与 ``type`` 对应的字段有值。"""

    type: str
    text: str = ""
    tool_call: ToolCall | None = None
    tool_result: ToolResult | None = None
    attachment: Attachment | None = None

    @classmethod
    def of_text(cls, text: str) -> Part:
        return cls(PART_TEXT, text=text)

    @classmethod
    def of_tool_call(cls, call: ToolCall) -> Part:
        return cls(PART_TOOL_CALL, tool_call=call)

    @classmethod
    def of_tool_result(cls, result: ToolResult) -> Part:
        return cls(PART_TOOL_RESULT, tool_result=result)

    def to_json(self) -> dict[str, Any]:
        out: dict[str, Any] = {"type": self.type}
        if self.type == PART_TEXT:
            out["text"] = self.text
        elif self.type == PART_TOOL_CALL and self.tool_call is not None:
            out["tool_call"] = self.tool_call.to_json()
        elif self.type == PART_TOOL_RESULT and self.tool_result is not None:
            out["tool_result"] = self.tool_result.to_json()
        elif self.type == PART_ATTACHMENT and self.attachment is not None:
            out["attachment"] = self.attachment.to_json()
        return out


@dataclass(frozen=True, slots=True)
class Message:
    """会话消息。持久化相关字段（id、seq、时间）由守护进程填充，这里不关心。"""

    role: str
    parts: tuple[Part, ...] = ()

    def text(self) -> str:
        """拼接全部文本片段。"""
        return "".join(p.text for p in self.parts if p.type == PART_TEXT)

    def to_json(self, *, session_id: str = "", run_id: str = "") -> dict[str, Any]:
        out: dict[str, Any] = {"role": self.role, "parts": [p.to_json() for p in self.parts]}
        if session_id:
            out["session_id"] = session_id
        if run_id:
            out["run_id"] = run_id
        return out


@dataclass(frozen=True, slots=True)
class ToolSpec:
    """暴露给模型的工具声明。"""

    name: str
    description: str = ""
    input_schema: dict[str, Any] = field(default_factory=dict)
    source: str = ""
    risk: str = ""


@dataclass(frozen=True, slots=True)
class Usage:
    """token 用量。``input_tokens`` 含缓存读写的部分；``cache_read_tokens`` 是其中命中缓存的子集，
    ``cache_write_tokens`` 是其中写入提示缓存的子集
    （如 Anthropic 的 ``cache_creation_input_tokens``）。

    与 Go 的 ``model.Usage`` 口径一致。协议口径（§3）：``usage`` 事件是**一次模型调用的增量**，
    守护进程累加；``run.completed.usage`` 是整次运行的**累计值**。
    """

    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    cache_write_tokens: int = 0

    def add(self, other: Usage) -> Usage:
        """逐字段累加（跨步累计）。"""
        return Usage(
            self.input_tokens + other.input_tokens,
            self.output_tokens + other.output_tokens,
            self.cache_read_tokens + other.cache_read_tokens,
            self.cache_write_tokens + other.cache_write_tokens,
        )

    def merge_max(self, other: Usage) -> Usage:
        """逐字段取最大值：同一次模型调用内多次上报（累计值或分段值）时用它合并。"""
        return Usage(
            max(self.input_tokens, other.input_tokens),
            max(self.output_tokens, other.output_tokens),
            max(self.cache_read_tokens, other.cache_read_tokens),
            max(self.cache_write_tokens, other.cache_write_tokens),
        )

    def to_json(self) -> dict[str, int]:
        out = {"input_tokens": self.input_tokens, "output_tokens": self.output_tokens}
        if self.cache_read_tokens:
            out["cache_read_tokens"] = self.cache_read_tokens
        if self.cache_write_tokens:
            out["cache_write_tokens"] = self.cache_write_tokens
        return out


@dataclass(frozen=True, slots=True)
class AgentSpec:
    """交给工作进程的智能体描述。"""

    id: str
    name: str = ""
    system_prompt: str = ""
    autonomy: str = ""


@dataclass(frozen=True, slots=True)
class ProviderCredentials:
    """一次性凭据：只在内存中存在，``api_key`` 不进 repr、不进日志。"""

    kind: str
    model: str
    base_url: str = ""
    api_key: str = field(default="", repr=False)


@dataclass(frozen=True, slots=True)
class RunOptions:
    """单次运行的推理参数；``None`` 表示由供应商适配决定默认值。"""

    max_steps: int = DEFAULT_MAX_STEPS
    temperature: float | None = None
    max_output_tokens: int | None = None


@dataclass(frozen=True, slots=True)
class RunRequest:
    """``run.start`` 的参数。含凭据，整个对象不得写日志。"""

    run_id: str
    session_id: str
    agent: AgentSpec
    provider: ProviderCredentials
    messages: tuple[Message, ...] = ()
    tools: tuple[ToolSpec, ...] = ()
    options: RunOptions = RunOptions()


def run_event(
    event_type: str,
    *,
    run_id: str,
    session_id: str,
    agent_id: str,
    at: datetime,
    delta: str | None = None,
    tool_call: ToolCall | None = None,
    tool_result: ToolResult | None = None,
    usage: Usage | None = None,
    message: Message | None = None,
    error: str | None = None,
) -> dict[str, Any]:
    """构造一条 ``RunEvent``（形状与 Go 的 ``model.RunEvent`` 相同）。"""
    event: dict[str, Any] = {
        "type": event_type,
        "run_id": run_id,
        "session_id": session_id,
        "agent_id": agent_id,
        "at": format_time(at),
    }
    if delta is not None:
        event["delta"] = delta
    if tool_call is not None:
        event["tool_call"] = tool_call.to_json()
    if tool_result is not None:
        event["tool_result"] = tool_result.to_json()
    if usage is not None:
        event["usage"] = usage.to_json()
    if message is not None:
        event["message"] = message.to_json(session_id=session_id, run_id=run_id)
    if error is not None:
        event["error"] = error
    return event


# ---- run.start 参数解析 ----


def _invalid(path: str, reason: str) -> JsonRpcError:
    return JsonRpcError(INVALID_PARAMS, f"Invalid params: {path} {reason}")


def _as_dict(value: Any, path: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise _invalid(path, "must be an object")
    return value


def _as_list(value: Any, path: str) -> list[Any]:
    if value is None:
        return []
    if not isinstance(value, list):
        raise _invalid(path, "must be an array")
    return value


def _require_str(obj: dict[str, Any], key: str, path: str) -> str:
    value = obj.get(key)
    if not isinstance(value, str) or not value:
        raise _invalid(f"{path}.{key}", "must be a non-empty string")
    return value


def _optional_str(obj: dict[str, Any], key: str) -> str:
    value = obj.get(key)
    return value if isinstance(value, str) else ""


def _optional_int(value: Any) -> int | None:
    if isinstance(value, bool) or not isinstance(value, int):
        return None
    return value if value > 0 else None


def _optional_float(value: Any) -> float | None:
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    return float(value)


def parse_tool_call(obj: Any, path: str) -> ToolCall:
    """解析一个 ``ToolCall`` 对象。"""
    data = _as_dict(obj, path)
    return ToolCall(
        id=_require_str(data, "id", path),
        name=_require_str(data, "name", path),
        arguments=data.get("arguments"),
    )


def parse_tool_result(obj: Any, path: str) -> ToolResult:
    """解析一个 ``ToolResult`` 对象。"""
    data = _as_dict(obj, path)
    content = data.get("content")
    return ToolResult(
        call_id=_require_str(data, "call_id", path),
        content=content if isinstance(content, str) else "",
        is_error=bool(data.get("is_error", False)),
        truncated=data.get("truncated") is True,
    )


def parse_message(obj: Any, path: str = "message") -> Message:
    """解析一条 ``Message``；未知片段类型被忽略，缺少 ``parts`` 视为空消息。

    不完整的 ``tool_call``（缺 ``id`` 或 ``name``）与 ``tool_result``（缺 ``call_id``）片段记日志后
    忽略，而不是让整个 ``run.start`` 以 ``-32602`` 失败：这类片段只可能来自已落库的历史（旧版本会把
    模型给出的无名工具调用写进 ``run.completed.message``），拒绝它会让该会话之后的每次运行都失败。
    缺少配对的调用或结果由消息转换（``providers.messages.flatten``）修复。
    """
    data = _as_dict(obj, path)
    role = _require_str(data, "role", path)
    if role not in ROLES:
        raise _invalid(f"{path}.role", f"unknown role {role!r}")
    parts: list[Part] = []
    for i, raw in enumerate(_as_list(data.get("parts"), f"{path}.parts")):
        part_path = f"{path}.parts[{i}]"
        part = _as_dict(raw, part_path)
        kind = part.get("type")
        if kind == PART_TEXT:
            parts.append(Part.of_text(_optional_str(part, "text")))
        elif kind == PART_TOOL_CALL:
            try:
                call = parse_tool_call(part.get("tool_call"), part_path)
            except JsonRpcError as exc:
                _log.warning("忽略历史消息中不完整的 tool_call 片段：%s", exc.message)
                continue
            parts.append(Part.of_tool_call(call))
        elif kind == PART_TOOL_RESULT:
            try:
                result = parse_tool_result(part.get("tool_result"), part_path)
            except JsonRpcError as exc:
                _log.warning("忽略历史消息中不完整的 tool_result 片段：%s", exc.message)
                continue
            parts.append(Part.of_tool_result(result))
        elif kind == PART_ATTACHMENT:
            att = _as_dict(part.get("attachment"), f"{part_path}.attachment")
            size = att.get("size")
            parts.append(
                Part(
                    PART_ATTACHMENT,
                    attachment=Attachment(
                        name=_optional_str(att, "name"),
                        path=_optional_str(att, "path"),
                        mime=_optional_str(att, "mime"),
                        size=size if isinstance(size, int) and not isinstance(size, bool) else 0,
                    ),
                )
            )
        # 未知片段类型：忽略，保持向前兼容。
    return Message(role=role, parts=tuple(parts))


def parse_tool_spec(obj: Any, path: str) -> ToolSpec:
    """解析一个 ``ToolSpec``；``input_schema`` 缺省为空对象 schema。"""
    data = _as_dict(obj, path)
    schema = data.get("input_schema")
    if schema is None:
        schema = {"type": "object", "properties": {}}
    elif not isinstance(schema, dict):
        raise _invalid(f"{path}.input_schema", "must be an object")
    return ToolSpec(
        name=_require_str(data, "name", path),
        description=_optional_str(data, "description"),
        input_schema=schema,
        source=_optional_str(data, "source"),
        risk=_optional_str(data, "risk"),
    )


def parse_run_request(params: Any) -> RunRequest:
    """校验并解析 ``run.start`` 的参数；任何不合法之处抛 ``-32602``。"""
    data = _as_dict(params, "params")
    run_id = _require_str(data, "run_id", "params")
    session_id = _require_str(data, "session_id", "params")

    agent = _as_dict(data.get("agent"), "params.agent")
    agent_spec = AgentSpec(
        id=_require_str(agent, "id", "params.agent"),
        name=_optional_str(agent, "name"),
        system_prompt=_optional_str(agent, "system_prompt"),
        autonomy=_optional_str(agent, "autonomy"),
    )

    provider = _as_dict(data.get("provider"), "params.provider")
    kind = _require_str(provider, "kind", "params.provider")
    if kind not in PROVIDER_KINDS:
        raise _invalid("params.provider.kind", f"unsupported kind {kind!r}")
    credentials = ProviderCredentials(
        kind=kind,
        model=_require_str(provider, "model", "params.provider"),
        base_url=_optional_str(provider, "base_url").strip(),
        api_key=_optional_str(provider, "api_key"),
    )

    messages = tuple(
        parse_message(raw, f"params.messages[{i}]")
        for i, raw in enumerate(_as_list(data.get("messages"), "params.messages"))
    )
    tools = tuple(
        parse_tool_spec(raw, f"params.tools[{i}]")
        for i, raw in enumerate(_as_list(data.get("tools"), "params.tools"))
    )

    raw_options = data.get("options")
    options_dict = _as_dict(raw_options, "params.options") if raw_options is not None else {}
    options = RunOptions(
        max_steps=_optional_int(options_dict.get("max_steps")) or DEFAULT_MAX_STEPS,
        temperature=_optional_float(options_dict.get("temperature")),
        max_output_tokens=_optional_int(options_dict.get("max_output_tokens")),
    )

    return RunRequest(
        run_id=run_id,
        session_id=session_id,
        agent=agent_spec,
        provider=credentials,
        messages=messages,
        tools=tools,
        options=options,
    )
