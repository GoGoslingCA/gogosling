// 历史步骤只挂载当前页；原始结果留在调用方，翻页和折叠均不删除内容。
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ReadonlyList } from "../../state/persistentList";

export const TOOL_HISTORY_PAGE_SIZE = 40;

export function ToolHistoryPage<T>({ items, end = items.length, renderItem, listClassName }: { items: ReadonlyList<T>; end?: number; renderItem: (item: T) => ReactNode; listClassName?: string }) {
  const { t } = useTranslation();
  const [selectedPage, setSelectedPage] = useState(0);
  const length = Math.min(items.length, Math.max(0, end));
  const pages = Math.max(1, Math.ceil(length / TOOL_HISTORY_PAGE_SIZE));
  const page = Math.min(selectedPage, pages - 1);
  const start = page * TOOL_HISTORY_PAGE_SIZE;
  return <>
    {pages > 1 ? <div className="tool-history-pages">
      <button type="button" className="btn ghost sm" disabled={page === 0} onClick={() => setSelectedPage(page - 1)}>{t("compact.previousSteps")}</button>
      <span className="muted" role="status">{start + 1}–{Math.min(start + TOOL_HISTORY_PAGE_SIZE, length)} / {length}</span>
      <button type="button" className="btn ghost sm" disabled={page === pages - 1} onClick={() => setSelectedPage(page + 1)}>{t("compact.nextSteps")}</button>
    </div> : null}
    {listClassName ? <ol className={listClassName}>{items.slice(start, Math.min(start + TOOL_HISTORY_PAGE_SIZE, length)).map(renderItem)}</ol> : items.slice(start, Math.min(start + TOOL_HISTORY_PAGE_SIZE, length)).map(renderItem)}
  </>;
}
