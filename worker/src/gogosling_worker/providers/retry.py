"""模型服务明确拒绝请求后的有界重试；响应流开始后不重放模型调用。"""

from __future__ import annotations

import math
from datetime import UTC, datetime
from email.utils import parsedate_to_datetime

import httpx

MAX_TRANSIENT_RETRIES = 2
MAX_RETRY_DELAY = 30.0


def retry_delay(response: httpx.Response, retries: int) -> float | None:
    """429 / 临时 5xx 最多重试两次；超出等待预算时直接保留原错误。

    ``Retry-After`` 支持秒数与 HTTP 日期；服务要求等待超过 30 秒时不提前重试。
    501 / 505 表示能力不支持，重试不会修复，直接返回失败。
    """
    status = response.status_code
    if retries >= MAX_TRANSIENT_RETRIES or (
        status != 429 and not (500 <= status <= 599 and status not in (501, 505))
    ):
        return None
    delay = min(0.5 * 2**retries, MAX_RETRY_DELAY)
    header = response.headers.get("retry-after")
    if header:
        try:
            requested = float(header)
        except ValueError:
            try:
                at = parsedate_to_datetime(header)
                if at.tzinfo is None:
                    at = at.replace(tzinfo=UTC)
                requested = (at - datetime.now(UTC)).total_seconds()
            except (TypeError, ValueError, OverflowError):
                requested = delay
        if math.isfinite(requested) and requested >= 0:
            delay = requested
    return delay if delay <= MAX_RETRY_DELAY else None
