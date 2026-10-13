// 诊断上报只在明确启用的发行构建中开放；开发环境不读取发布标记。

/** 发布标记只控制功能入口，不是客户端身份或服务器鉴权证明。 */
export function releaseDiagnosticsEnabled(options: {
  isPackaged: boolean;
  hasDevOverride: boolean;
  readMetadata: () => unknown;
}): boolean {
  if (!options.isPackaged || options.hasDevOverride) return false;
  try {
    const metadata = options.readMetadata();
    if (!metadata || typeof metadata !== "object") return false;
    const policy = (metadata as Record<string, unknown>).gogoslingDiagnostics;
    if (!policy || typeof policy !== "object") return false;
    const fields = policy as Record<string, unknown>;
    // electron-builder 的命令行配置保留数字标记为字符串；只接受同一协议版本。
    return (fields.version === 1 || fields.version === "1") && fields.enabled === true;
  } catch {
    return false;
  }
}
