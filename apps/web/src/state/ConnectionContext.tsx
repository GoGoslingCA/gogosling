// 连接状态机：保存当前连接（内存）、对应的 Api 实例与失效原因。
// 令牌失效（401）不做整页跳转，只把状态切换到"未连接"并带上原因，由路由守卫送回 /connect。

import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { QueryClient } from "@tanstack/react-query";
import { createApi, type Api } from "../api";
import type { ApiError } from "../api/client";
import type { Connection } from "../connection";
import { currentLocale } from "../i18n";

export type DisconnectReason = "unauthorized" | "manual" | null;

interface ConnectionContextValue {
  connection: Connection | null;
  api: Api | null;
  /** 最近一次断开的原因，用于连接页提示。 */
  reason: DisconnectReason;
  connect: (conn: Connection) => void;
  disconnect: (reason?: DisconnectReason) => void;
}

const ConnectionContext = createContext<ConnectionContextValue | null>(null);

export interface ConnectionProviderProps {
  initial: Connection | null;
  queryClient: QueryClient;
  children: ReactNode;
}

export function ConnectionProvider({ initial, queryClient, children }: ConnectionProviderProps) {
  const [connection, setConnection] = useState<Connection | null>(initial);
  const [reason, setReason] = useState<DisconnectReason>(null);
  // 用 ref 让 onUnauthorized 回调总是作用于"当前"连接，避免旧实例的 401 把新连接踢掉。
  const activeToken = useRef<string | null>(initial?.token ?? null);

  const disconnect = useCallback(
    (why: DisconnectReason = "manual") => {
      activeToken.current = null;
      setConnection(null);
      setReason(why);
      queryClient.clear();
    },
    [queryClient],
  );

  const connect = useCallback(
    (conn: Connection) => {
      activeToken.current = conn.token;
      setReason(null);
      queryClient.clear();
      setConnection(conn);
    },
    [queryClient],
  );

  const api = useMemo<Api | null>(() => {
    if (!connection) return null;
    const token = connection.token;
    return createApi({
      baseUrl: connection.endpoint,
      token,
      locale: currentLocale,
      onUnauthorized: (_err: ApiError) => {
        if (activeToken.current === token) disconnect("unauthorized");
      },
    });
  }, [connection, disconnect]);

  const value = useMemo<ConnectionContextValue>(
    () => ({ connection, api, reason, connect, disconnect }),
    [connection, api, reason, connect, disconnect],
  );

  return <ConnectionContext.Provider value={value}>{children}</ConnectionContext.Provider>;
}

export function useConnection(): ConnectionContextValue {
  const ctx = useContext(ConnectionContext);
  if (!ctx) throw new Error("useConnection must be used within ConnectionProvider");
  return ctx;
}

/** 只在已连接的页面树里使用；未连接时抛错（路由守卫保证不会发生）。 */
export function useApi(): Api {
  const { api } = useConnection();
  if (!api) throw new Error("not connected");
  return api;
}
