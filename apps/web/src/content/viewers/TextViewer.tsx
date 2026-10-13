import { useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { prepareMarkdownPreview } from "../../pages/chat/Markdown";
import { Icon } from "../../components/Icon";
import { highlightArtifactCode } from "../../lib/artifacts";
import { clearReadingSelection, findReadingMatches, MAX_FIND_QUERY, MAX_OUTLINE_HEADINGS, readingHeadings, readingText, revealReadingMatch, textPage, type ReadingHeading, type ReadingText } from "../readingNavigation";
import type { ViewerProps } from "../types";
import "../viewer-navigation.css";

export default function TextViewer(props: ViewerProps) {
  return <TextDocument key={`${props.agentId}:${props.path}:${props.document.version}:${props.document.name}`} {...props} />;
}
function TextDocument({ document: doc }: ViewerProps) {
  const { t } = useTranslation();
  const prefix = useId();
  const body = useRef<HTMLDivElement>(null);
  const [source, setSource] = useState(false);
  const [requestedPage, setPage] = useState(0);
  const [requestedMarkdownPage, setMarkdownPage] = useState(0);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [findOpen, setFindOpen] = useState(false);
  const [outlineOpen, setOutlineOpen] = useState(false);
  const [headings, setHeadings] = useState<ReadingHeading[]>([]);
  const [outlineLimited, setOutlineLimited] = useState(false);
  const [snapshot, setSnapshot] = useState<ReadingText>({ text: "", segments: [] });
  const { text, page, pages } = textPage(doc.text ?? "", requestedPage, doc.kind === "markdown" ? 20_000 : undefined);
  const large = pages > 1;
  const candidate = useMemo(() => doc.kind === "markdown" && !source ? prepareMarkdownPreview(doc.text ?? "") : null, [doc.kind, source, doc.text]);
  const sourceRequired = candidate?.sourceRequired ?? false;
  const sourceMode = source || sourceRequired;
  const markdown = doc.kind === "markdown" && !sourceMode;
  const prepared = sourceRequired ? null : candidate;
  const markdownPage = Math.min(requestedMarkdownPage, (prepared?.pageCount ?? 1) - 1);
  const ext = doc.name.split(".").at(-1) ?? "text";
  const rendered = useMemo(() => prepared ? prepared.renderPage(markdownPage) : <pre className="preview-code"><code>{doc.kind === "markdown" ? text : highlightArtifactCode(text, ext).map((token, index) => <span key={index} className={`syntax-${token.kind}`}>{token.text}</span>)}</code></pre>, [prepared, markdownPage, text, ext, doc.kind]);
  useLayoutEffect(() => {
    if (!body.current) return;
    const outline = markdown && outlineOpen ? readingHeadings(body.current, prefix) : { headings: [], truncated: false };
    setHeadings(outline.headings);
    setOutlineLimited(outline.truncated);
  }, [rendered, markdown, prefix, outlineOpen]);
  useLayoutEffect(() => {
    if (!body.current) return;
    setSnapshot(findOpen ? readingText(body.current) : { text: "", segments: [] });
    setActive(0);
    // 当前页切换后清除旧页命中，避免显示属于上一页的位置。
    clearReadingSelection(body.current);
  }, [rendered, findOpen]);
  const result = useMemo(() => findReadingMatches(snapshot.text, query), [snapshot.text, query]);
  const selected = result.matches.length ? Math.min(active, result.matches.length - 1) : 0;
  const move = (step: number) => { if (result.matches.length) setActive((selected + step + result.matches.length) % result.matches.length); };
  useLayoutEffect(() => {
    if (!body.current) return;
    if (result.matches[selected]) revealReadingMatch(body.current, snapshot, result.matches[selected]);
    else clearReadingSelection(body.current);
  }, [result, selected, snapshot]);
  return <div className="preview-text">
    {doc.kind === "markdown" ? <div className="preview-mode">{sourceRequired ? <span className="muted">{t("preview.source")}</span> : <button type="button" className="btn ghost sm" aria-pressed={source} onClick={() => setSource(!source)}>{t(source ? "preview.read" : "preview.source")}</button>}{prepared && prepared.pageCount > 1 ? <><span className="spacer"/><button type="button" className="btn ghost sm" disabled={!markdownPage} onClick={() => setMarkdownPage(markdownPage - 1)}>{t("preview.previous")}</button><span>{markdownPage + 1} / {prepared.pageCount}</span><button type="button" className="btn ghost sm" disabled={markdownPage + 1 >= prepared.pageCount} onClick={() => setMarkdownPage(markdownPage + 1)}>{t("preview.next")}</button></> : sourceMode && large ? <><span className="spacer"/><button type="button" className="btn ghost sm" disabled={!page} onClick={() => setPage(page - 1)}>{t("preview.previous")}</button><span>{page + 1} / {pages}</span><button type="button" className="btn ghost sm" disabled={page + 1 >= pages} onClick={() => setPage(page + 1)}>{t("preview.next")}</button></> : null}</div> : large ? <div className="preview-pager"><span className="muted">{t("preview.source")}</span><span className="spacer" /><button type="button" className="btn ghost sm" disabled={!page} onClick={() => setPage(page - 1)}>{t("preview.previous")}</button><span>{page + 1} / {pages}</span><button type="button" className="btn ghost sm" disabled={page + 1 >= pages} onClick={() => setPage(page + 1)}>{t("preview.next")}</button></div> : null}
    <div className="reading-navigation">
      {markdown ? <details className="reading-outline" open={outlineOpen} onToggle={event => setOutlineOpen(event.currentTarget.open)}><summary><Icon name="chevron" size={12} />{t("readingNavigation.outline")}</summary>{outlineOpen ? <>{headings.length ? <nav aria-label={t("readingNavigation.outline")}>
        {headings.map(heading => <a key={heading.id} href={`#${heading.id}`} style={{ paddingInlineStart: `${8 + (heading.level - 1) * 12}px` }} onClick={event => {
          event.preventDefault();
          const target = document.getElementById(heading.id);
          target?.scrollIntoView?.({ block: "start" });
          target?.focus({ preventScroll: true });
        }}>{heading.title}</a>)}
      </nav> : <p className="reading-find-hint">{t("readingNavigation.outlineEmpty")}</p>}{outlineLimited ? <p className="reading-find-hint">{t("readingNavigation.outlineLimited", { count: MAX_OUTLINE_HEADINGS })}</p> : null}</> : null}</details> : null}
      <details className="reading-find-panel" onToggle={event => { setFindOpen(event.currentTarget.open); if (!event.currentTarget.open && query) { setQuery(""); clearReadingSelection(body.current); } }}>
        <summary><Icon name="search" size={14} />{t("readingNavigation.find")}</summary>
        <div className="reading-find">
          <input type="search" maxLength={MAX_FIND_QUERY} aria-label={t("readingNavigation.find")} placeholder={t("readingNavigation.find")} value={query} onChange={event => { setQuery(event.target.value.slice(0, MAX_FIND_QUERY)); setActive(0); }} onKeyDown={event => {
            if (event.key === "Enter") { event.preventDefault(); move(event.shiftKey ? -1 : 1); }
            if (event.key === "Escape") { event.preventDefault(); setQuery(""); clearReadingSelection(body.current); }
          }} />
          <span className="reading-find-status" role="status">{query ? t(result.truncated ? "readingNavigation.matchesLimited" : "readingNavigation.matches", { current: result.matches.length ? selected + 1 : 0, count: result.matches.length }) : ""}</span>
          <button type="button" className="btn ghost sm" aria-label={t("readingNavigation.previousMatch")} disabled={!result.matches.length} onClick={() => move(-1)}><Icon name="chevron" size={13} style={{ transform: "rotate(-90deg)" }} /></button>
          <button type="button" className="btn ghost sm" aria-label={t("readingNavigation.nextMatch")} disabled={!result.matches.length} onClick={() => move(1)}><Icon name="chevron" size={13} style={{ transform: "rotate(90deg)" }} /></button>
        </div>
        <p className="reading-find-hint">{t("readingNavigation.scope")}</p>
      </details>
    </div>
    <div ref={body} className="reading-text-body">{rendered}</div>
  </div>;
}
