"""工作进程内的文案（zh / en / fr）。

工作进程没有内核的 ``internal/i18n``，但同样不在代码里硬编码提示：这里集中保存少量键值，
由 ``worker.hello`` 协商的 ``locale`` 选择语言，默认中文。这些文案有两类去向：

- 作为 ``tool_result.content`` 回填给模型（例如参数不是合法 JSON）；
- 作为 ``run.failed.error`` 展示给用户（例如模型服务返回 HTTP 401）。

协议层面的错误（JSON-RPC 的 ``message``）面向开发者，保持英文，不走这里。
"""

from __future__ import annotations

DEFAULT_LOCALE = "zh"

_TEXTS: dict[str, dict[str, str]] = {
    "zh": {
        "message.empty": "（空消息）",
        "tool.invalid_arguments": "工具参数不是合法 JSON：{excerpt}",
        "tool.missing_name": (
            "模型给出的工具调用缺少名称（{call_id}），未执行。请指明要调用的工具名称后重试，或直接回答。"
        ),
        "tool.result_missing": "（该工具调用没有可用的结果）",
        "tool.host_error": "工具执行失败：{reason}",
        "tool.arguments_too_large": "工具参数超过单条消息大小上限，未执行",
        "tool.malformed_result": "守护进程返回的工具结果无法解析",
        "run.max_steps": "已达到最大步数（{max_steps}），运行终止",
        "run.tool_repeat": "工具 {name} 使用相同参数连续调用了 {count} 次，已停止，避免反复重试",
        "run.internal_error": "工作进程内部错误（{error}）",
        "run.no_terminal": "引擎结束时没有产生终态事件",
        "run.event_too_large": "运行结果超过单条消息大小上限，无法回传",
        "run.continue_answer": "上一段回复因输出长度限制而中断。请从中断处继续，避免重复已有内容。",
        "run.output_truncated": (
            "模型回复达到输出长度限制，自动续写后仍未完成，请调整输出限制后重试"
        ),
        "run.tool_call_truncated": (
            "模型工具调用达到输出长度限制，未执行不完整的调用，请调整输出限制后重试"
        ),
        "provider.http_error": "模型 {model} 返回 HTTP {status}：{detail}",
        "provider.transport_error": "无法连接模型服务（{reason}）",
        "provider.timeout": "等待模型服务响应超时",
        "provider.stream_error": "模型服务在流式响应中报错：{detail}",
        "provider.bad_response": "模型服务的响应无法识别：{detail}",
        "provider.incomplete_response": "模型服务的响应在完成前中断，请重试",
        "provider.unsupported_kind": "不支持的供应商类型：{kind}",
        "provider.missing_base_url": "供应商 {kind} 缺少 base_url",
    },
    "en": {
        "message.empty": "(empty message)",
        "tool.invalid_arguments": "tool arguments are not valid JSON: {excerpt}",
        "tool.missing_name": (
            "the model returned a tool call without a name ({call_id}); it was not executed. "
            "Name the tool to call and try again, or answer directly."
        ),
        "tool.result_missing": "(no result is available for this tool call)",
        "tool.host_error": "tool execution failed: {reason}",
        "tool.arguments_too_large": "tool arguments exceed the message size limit; not executed",
        "tool.malformed_result": "the daemon returned an unreadable tool result",
        "run.max_steps": "reached the maximum number of steps ({max_steps}); run stopped",
        "run.tool_repeat": (
            "tool {name} was called {count} times in a row with the same arguments; the run stopped"
        ),
        "run.internal_error": "internal worker error ({error})",
        "run.no_terminal": "the engine finished without a terminal event",
        "run.event_too_large": "the run result exceeds the message size limit and cannot be sent",
        "run.continue_answer": (
            "The previous response reached the output limit. Continue from where it stopped "
            "without repeating existing content."
        ),
        "run.output_truncated": (
            "the response still reached the output limit after automatic continuation; "
            "adjust the output limit and retry"
        ),
        "run.tool_call_truncated": (
            "the model tool call reached the output limit; incomplete calls were not executed; "
            "adjust the output limit and retry"
        ),
        "provider.http_error": "model {model} returned HTTP {status}: {detail}",
        "provider.transport_error": "cannot reach the model service ({reason})",
        "provider.timeout": "timed out waiting for the model service",
        "provider.stream_error": "model service reported an error in the stream: {detail}",
        "provider.bad_response": "unrecognised response from the model service: {detail}",
        "provider.incomplete_response": "the model response ended before completion; please retry",
        "provider.unsupported_kind": "unsupported provider kind: {kind}",
        "provider.missing_base_url": "provider {kind} has no base_url",
    },
    "fr": {
        "message.empty": "(message vide)",
        "tool.invalid_arguments": "les arguments de l'outil ne sont pas un JSON valide : {excerpt}",
        "tool.missing_name": (
            "le modèle a renvoyé un appel d'outil sans nom ({call_id}) ; il n'a pas été exécuté. "
            "Indiquez le nom de l'outil à appeler et réessayez, ou répondez directement."
        ),
        "tool.result_missing": "(aucun résultat n'est disponible pour cet appel d'outil)",
        "tool.host_error": "échec de l'exécution de l'outil : {reason}",
        "tool.arguments_too_large": (
            "les arguments de l'outil dépassent la taille maximale d'un message ; non exécuté"
        ),
        "tool.malformed_result": "le démon a renvoyé un résultat d'outil illisible",
        "run.max_steps": "nombre maximal d'étapes atteint ({max_steps}) ; exécution arrêtée",
        "run.tool_repeat": (
            "l'outil {name} a été appelé {count} fois de suite avec les mêmes arguments ; "
            "l'exécution s'est arrêtée"
        ),
        "run.internal_error": "erreur interne du processus de travail ({error})",
        "run.no_terminal": "le moteur s'est terminé sans événement final",
        "run.event_too_large": (
            "le résultat de l'exécution dépasse la taille maximale d'un message "
            "et ne peut pas être renvoyé"
        ),
        "run.continue_answer": (
            "La réponse précédente a atteint la limite de sortie. "
            "Reprenez là où elle s'est arrêtée sans répéter le contenu déjà produit."
        ),
        "run.output_truncated": (
            "la réponse a encore atteint la limite de sortie après la poursuite automatique ; "
            "ajustez la limite de sortie et réessayez"
        ),
        "run.tool_call_truncated": (
            "l'appel d'outil du modèle a atteint la limite de sortie ; "
            "les appels incomplets n'ont pas été exécutés ; "
            "ajustez la limite de sortie et réessayez"
        ),
        "provider.http_error": "le modèle {model} a renvoyé HTTP {status} : {detail}",
        "provider.transport_error": "impossible de joindre le service de modèle ({reason})",
        "provider.timeout": "délai dépassé en attendant le service de modèle",
        "provider.stream_error": (
            "le service de modèle a signalé une erreur dans le flux : {detail}"
        ),
        "provider.bad_response": "réponse du service de modèle non reconnue : {detail}",
        "provider.incomplete_response": (
            "la réponse du modèle s'est interrompue avant la fin ; veuillez réessayer"
        ),
        "provider.unsupported_kind": "type de fournisseur non pris en charge : {kind}",
        "provider.missing_base_url": "le fournisseur {kind} n'a pas de base_url",
    },
}


def normalize_locale(value: object) -> str:
    """把任意 locale 字符串归一为支持的语言（``zh``、``en`` 或 ``fr``）；未知值回退中文。"""
    if not isinstance(value, str):
        return DEFAULT_LOCALE
    tag = value.strip().lower().replace("_", "-").split("-", 1)[0]
    if tag == "fr":
        return "fr"
    if tag == "en":
        return "en"
    return DEFAULT_LOCALE


def tr(key: str, locale: str = DEFAULT_LOCALE, **kwargs: object) -> str:
    """按键取文案并格式化；缺键时返回键名本身，保证永远不会抛异常。"""
    table = _TEXTS.get(normalize_locale(locale), _TEXTS[DEFAULT_LOCALE])
    template = table.get(key) or _TEXTS[DEFAULT_LOCALE].get(key) or key
    try:
        return template.format(**kwargs)
    except (KeyError, IndexError, ValueError):
        return template
