// 应用根：连接状态 → react-query → 提示 → hash 路由（file:// 与 app:// 下 BrowserRouter 不可用）。

import { lazy, Suspense, useMemo } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createHashRouter, Navigate, RouterProvider } from "react-router-dom";
import { ApiError } from "./api/client";
import type { Connection } from "./connection";
import { ConnectionProvider } from "./state/ConnectionContext";
import { ToastProvider } from "./components/Toast";
import { AppShell } from "./layout/AppShell";
import { readUiPreferences } from "./lib/uiPreferences";
import { Spinner } from "./components/ui";
import { SettingsRedirect } from "./pages/SettingsRedirect";
import { legacySettingsRoutes } from "./lib/settingsNavigation";

// 各页面按路由加载，连接页与聊天页不必下载所有管理页面。
const ConnectPage = lazy(() => import("./pages/ConnectPage").then((page) => ({ default: page.ConnectPage })));
const ChatPage = lazy(() => import("./pages/chat/ChatPage").then((page) => ({ default: page.ChatPage })));
const JobsPage = lazy(() => import("./pages/JobsPage").then((page) => ({ default: page.JobsPage })));
const ProjectsPage = lazy(() => import("./pages/ProjectsPage").then((page) => ({ default: page.ProjectsPage })));
const SettingsPage = lazy(() => import("./pages/SettingsPage").then((page) => ({ default: page.SettingsPage })));

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // 4xx 类错误重试没有意义；网络抖动最多重试两次。
        retry: (count, err) => {
          if (ApiError.is(err)) return err.code === "network" && count < 2;
          return count < 1;
        },
        staleTime: 5_000,
        refetchOnWindowFocus: true,
      },
      mutations: { retry: 0 },
    },
  });
}

function ResumeLastTask() {
  const prefs = readUiPreferences();
  return <Navigate to={prefs.lastSessionId ? `/chat/${prefs.lastSessionId}${prefs.lastArtifactId ? `?artifact=${prefs.lastArtifactId}` : ""}` : "/chat"} replace />;
}

const router = createHashRouter([
  { path: "/connect", element: <ConnectPage /> },
  {
    path: "/",
    element: <AppShell />,
    children: [
      { index: true, element: <ResumeLastTask /> },
      { path: "chat", element: <ChatPage /> },
      { path: "chat/:sessionId", element: <ChatPage /> },
      { path: "projects", element: <ProjectsPage /> },
      { path: "projects/:projectId", element: <ProjectsPage /> },
      { path: "jobs", element: <JobsPage /> },
      { path: "settings", element: <SettingsPage /> },
      ...Object.entries(legacySettingsRoutes).map(([path, section]) => ({ path: path.slice(1), element: <SettingsRedirect section={section} /> })),
      { path: "*", element: <Navigate to="/chat" replace /> },
    ],
  },
]);

export function App({ initialConnection }: { initialConnection: Connection | null }) {
  const queryClient = useMemo(createQueryClient, []);
  return (
    <QueryClientProvider client={queryClient}>
      <ConnectionProvider initial={initialConnection} queryClient={queryClient}>
        <ToastProvider>
          <Suspense fallback={<Spinner />}>
            <RouterProvider router={router} />
          </Suspense>
        </ToastProvider>
      </ConnectionProvider>
    </QueryClientProvider>
  );
}
