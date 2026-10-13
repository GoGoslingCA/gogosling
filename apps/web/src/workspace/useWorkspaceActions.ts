import { useTranslation } from "react-i18next";
import { workspaceBrowser } from "../api/workspaceBrowser";
import { useToast } from "../components/Toast";
import { useWorkspaceTabs } from "../state/WorkspaceTabsProvider";
import { WORKSPACE_TAB_LIMIT } from "../state/workspaceTabs";

export function useWorkspaceActions() {
  const tabs = useWorkspaceTabs(); const { t } = useTranslation(); const toast = useToast();
  return {
    tabs,
    openFile: (input: { agentId: string; path: string; title?: string }) => {
      const result = tabs?.store.openFile(input);
      if (result === "limit") toast.show(t("workspaceTabs.limit", { n: WORKSPACE_TAB_LIMIT }), "info");
      return result;
    },
    close: async (id: string) => {
      const originActiveId = tabs?.store.getSnapshot().activeId;
      const tab = tabs?.store.getSnapshot().tabs.find(item => item.id === id);
      if (!tab || !tabs) return;
      if (tab.kind === "web" && workspaceBrowser()) {
        try {
          const result = await workspaceBrowser()!.close(id);
          if (result?.error) { toast.show(t(`workspaceTabs.${result.error}`), "info"); if (result.error === "cancelled" && originActiveId === id && tabs.store.getSnapshot().activeId === originActiveId) tabs.store.activate(result.tabId); return; }
        } catch { toast.show(t("workspaceTabs.unavailable"), "error"); return; }
      }
      tabs.store.close(id);
    },
  };
}
