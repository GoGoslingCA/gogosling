// 发布诊断标记没有密钥；源码启动和默认社区构建均不启用上报。
export function diagnosticsBuildArgs(args) {
  const enabled = args.includes("--diagnostics");
  return [
    ...args.filter(arg => arg !== "--diagnostics"),
    "--config.extraMetadata.gogoslingDiagnostics.version=1",
    `--config.extraMetadata.gogoslingDiagnostics.enabled=${enabled}`,
  ];
}
