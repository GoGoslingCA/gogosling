from __future__ import annotations

import pytest

from gogosling_worker.providers.sse import ServerSentEvent, SSEParser, SSEProtocolError, iter_sse


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


@pytest.mark.parametrize("ending", [b"", b"\n", b"\r\n", b"\n\n"])
@pytest.mark.parametrize("split", [False, True])
def test_sse_line_limit_covers_terminated_lines_and_chunk_boundaries(
    ending: bytes, split: bool
) -> None:
    parser = SSEParser(max_line_bytes=16)
    if split:
        parser.feed(b"data: ")
    prefix = b"" if split else b"data: "
    with pytest.raises(SSEProtocolError, match="line exceeds"):
        parser.feed(prefix + b"x" * 17 + ending)
    assert not parser._buf


def test_sse_exact_line_boundary_accepts_split_crlf_and_utf8() -> None:
    parser = SSEParser(max_line_bytes=9)
    assert parser.feed(b"data: \xe4\xbd") == []
    assert parser.feed(b"\xa0\r") == []
    assert parser.feed(b"\n\r\n") == [ServerSentEvent("你")]
    assert parser.feed(b"data: \xe4\xbd\xa0") == []
    assert parser.flush() == [ServerSentEvent("你")]


@pytest.mark.parametrize("ending", [b"\n", b"\r\n"])
def test_sse_multiline_event_limit_is_aggregate_not_per_line(ending: bytes) -> None:
    parser = SSEParser(max_line_bytes=16, max_event_bytes=8)
    assert parser.feed(b"data: abc" + ending) == []
    assert parser.feed(b"data: def" + ending) == []
    with pytest.raises(SSEProtocolError, match="event exceeds 8 bytes"):
        parser.feed(b"data: g" + ending)
    assert not parser._buf and not parser._data and parser._event is None
    with pytest.raises(SSEProtocolError, match="closed"):
        parser.flush()
    with pytest.raises(SSEProtocolError, match="closed"):
        parser.feed(b"data: new\n\n")


def test_sse_empty_data_lines_are_bounded() -> None:
    parser = SSEParser(max_event_bytes=3)
    assert parser.feed(b"data:\ndata:\ndata:\n") == []
    with pytest.raises(SSEProtocolError, match="event exceeds"):
        parser.feed(b"data:\n")


def test_sse_event_budget_counts_utf8_bytes_and_event_name() -> None:
    parser = SSEParser(max_event_bytes=8)
    assert parser.feed(b"event: hi\ndata: \xe4\xbd\xa0\n") == []
    with pytest.raises(SSEProtocolError, match="event exceeds"):
        parser.feed(b"data: \xe5\xa5\xbd\n")


def test_sse_event_name_cannot_bypass_event_budget_or_leak_prior_data() -> None:
    parser = SSEParser(max_event_bytes=8)
    assert parser.feed(b"data: abc\n") == []
    with pytest.raises(SSEProtocolError, match="event exceeds"):
        parser.feed(b"event: abcde\n")
    assert not parser._data and parser._event is None


def test_sse_event_budget_resets_and_comments_do_not_accumulate() -> None:
    parser = SSEParser(max_event_bytes=4)
    assert parser.feed(b"event: old\nevent: x\n\n") == []
    for _ in range(50):
        assert parser.feed(b": comment\nid: ignored\nretry: 2\n") == []
    assert parser.feed(b"data: abc\n\n") == [ServerSentEvent("abc")]
    assert parser.feed(b"data: def\n\n") == [ServerSentEvent("def")]
    assert parser.feed(b"data:\n\n") == [ServerSentEvent("")]


def test_sse_eof_applies_event_limit_before_dispatch() -> None:
    parser = SSEParser(max_event_bytes=4)
    assert parser.feed(b"data: abc\ndata: x") == []
    with pytest.raises(SSEProtocolError, match="event exceeds"):
        parser.flush()
    assert not parser._buf and not parser._data


@pytest.mark.parametrize("limit", [0, -1])
def test_sse_byte_limits_must_be_positive(limit: int) -> None:
    with pytest.raises(ValueError, match="positive"):
        SSEParser(max_line_bytes=limit)
    with pytest.raises(ValueError, match="positive"):
        SSEParser(max_event_bytes=limit)


async def test_iter_sse_forwards_event_budget() -> None:
    from collections.abc import AsyncIterator

    async def chunks() -> AsyncIterator[bytes]:
        yield b"data: abc\n"
        yield b"data: d\n\n"

    with pytest.raises(SSEProtocolError, match="event exceeds"):
        [event async for event in iter_sse(chunks(), max_event_bytes=4)]
