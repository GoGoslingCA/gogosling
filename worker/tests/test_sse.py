from __future__ import annotations

import pytest

from gogosling_worker.providers.sse import ServerSentEvent, SSEParser, SSEProtocolError


def test_sse_basic_event_and_comment_are_parsed() -> None:
    parser = SSEParser()
    events = parser.feed(b': keep-alive\ndata: {"a":1}\n\n')
    assert events == [ServerSentEvent(data='{"a":1}')]


def test_sse_multiline_data_is_joined_with_newline() -> None:
    parser = SSEParser()
    events = parser.feed(b"event: message_delta\ndata: line1\ndata:line2\n\n")
    assert events == [ServerSentEvent(data="line1\nline2", event="message_delta")]


def test_sse_crlf_and_split_chunks() -> None:
    parser = SSEParser()
    first = parser.feed(b'data: {"text": "\xe4\xbd')
    assert first == []
    second = parser.feed(b'\xa0\xe5\xa5\xbd"}\r\n\r\n')
    assert second == [ServerSentEvent(data='{"text": "你好"}')]


def test_sse_empty_data_block_is_not_dispatched() -> None:
    parser = SSEParser()
    assert parser.feed(b"event: ping\n\n") == []
    # 事件名在空派发后被重置，不会污染下一条事件。
    assert parser.feed(b"data: x\n\n") == [ServerSentEvent(data="x")]


def test_sse_flush_dispatches_trailing_event_without_blank_line() -> None:
    parser = SSEParser()
    assert parser.feed(b"data: tail") == []
    assert parser.flush() == [ServerSentEvent(data="tail")]
    assert parser.flush() == []


def test_sse_overlong_line_raises_instead_of_growing_forever() -> None:
    parser = SSEParser(max_line_bytes=16)
    with pytest.raises(SSEProtocolError):
        parser.feed(b"data: " + b"x" * 32)
