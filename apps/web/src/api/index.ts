// 把各资源模块组合成一个 Api 对象；页面只通过它访问守护进程（AGENTS.md §5）。

import { ApiClient, type ApiClientOptions } from "./client";
import { agentsApi } from "./modules/agents";
import { approvalsApi } from "./modules/approvals";
import { auditApi } from "./modules/audit";
import { channelsApi } from "./modules/channels";
import { extensionsApi } from "./modules/extensions";
import { jobsApi } from "./modules/jobs";
import { providersApi } from "./modules/providers";
import { runsApi } from "./modules/runs";
import { sessionsApi } from "./modules/sessions";
import { settingsApi } from "./modules/settings";
import { systemApi } from "./modules/system";
import { previewApi } from "./modules/preview";
import { workbenchApi } from "./modules/workbench";

export interface Api {
  client: ApiClient;
  preview: ReturnType<typeof previewApi>;
  agents: ReturnType<typeof agentsApi>;
  providers: ReturnType<typeof providersApi>;
  sessions: ReturnType<typeof sessionsApi>;
  runs: ReturnType<typeof runsApi>;
  approvals: ReturnType<typeof approvalsApi>;
  channels: ReturnType<typeof channelsApi>;
  audit: ReturnType<typeof auditApi>;
  jobs: ReturnType<typeof jobsApi>;
  extensions: ReturnType<typeof extensionsApi>;
  settings: ReturnType<typeof settingsApi>;
  system: ReturnType<typeof systemApi>;
  workbench: ReturnType<typeof workbenchApi>;
}

export function createApi(options: ApiClientOptions): Api {
  const client = new ApiClient(options);
  return {
    client,
    preview: previewApi(client),
    agents: agentsApi(client),
    providers: providersApi(client),
    sessions: sessionsApi(client),
    runs: runsApi(client),
    approvals: approvalsApi(client),
    channels: channelsApi(client),
    audit: auditApi(client),
    jobs: jobsApi(client),
    extensions: extensionsApi(client),
    settings: settingsApi(client),
    system: systemApi(client),
    workbench: workbenchApi(client),
  };
}

export { ApiClient, ApiError } from "./client";
export * from "./types";
