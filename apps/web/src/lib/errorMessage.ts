// 错误 → 面向用户的文案。优先级：服务端已本地化的 message > errors.<code> 文案 > 兜底。
//
// 不拼接错误详情，按优先级选择一条完整文案。

import type { TFunction } from "i18next";
import { ApiError } from "../api/client";

export function describeError(err: unknown, t: TFunction): string {
  if (ApiError.is(err)) {
    const key = `errors.${err.code}`;
    const translated = t(key, { defaultValue: "" });
    // 服务端 message 已按 Accept-Language 本地化；客户端错误（network/aborted）没有服务端 message。
    if (err.status > 0 && err.message && !/^\d{3}\b/.test(err.message)) return err.message;
    if (translated) return translated;
    return err.message || t("errors.internal");
  }
  if (err instanceof Error && err.message) return err.message;
  return t("errors.internal");
}
