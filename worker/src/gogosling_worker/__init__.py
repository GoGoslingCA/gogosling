"""gogosling 的 Python 智能体工作进程。

本包由 Go 守护进程作为子进程启动，不直接面向用户，也不监听任何网络端口；
它通过 stdin / stdout 上的行分隔 JSON-RPC 2.0 与守护进程通信（``api/worker-rpc/protocol.md``）。

模块：
- ``rpc``：全双工 JSON-RPC 连接、错误码、分帧；
- ``transport``：stdio 传输；
- ``protocol``：协议数据形状与 ``run.start`` 参数校验；
- ``methods``：``worker.*`` / ``run.*`` 方法实现与运行表；
- ``runtime``：引擎接口、默认推理循环、宿主适配；
- ``providers``：OpenAI 兼容 / Anthropic / Ollama 客户端与消息格式转换；
- ``i18n``：面向模型与用户的少量文案。

待实现（本轮范围外）：内置工具声明、记忆、知识库检索；思考（reasoning）内容的转发；
多模态消息（图片）的供应商格式。
"""

__version__ = "0.1.0"
