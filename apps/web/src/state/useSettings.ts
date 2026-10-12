// 全局设置的读取与时区/语言的派生值。

import { useQuery } from "@tanstack/react-query";
import { useApi } from "./ConnectionContext";
import { queryKeys } from "./queryKeys";
import type { Settings } from "../api/types";

export function useSettingsQuery() {
  const api = useApi();
  return useQuery<Settings>({ queryKey: queryKeys.settings, queryFn: () => api.settings.get(), staleTime: 60_000 });
}

/** 展示时间使用的时区；未设置时为 undefined（浏览器时区）。 */
export function useTimeZone(): string | undefined {
  const { data } = useSettingsQuery();
  return data?.timezone || undefined;
}
