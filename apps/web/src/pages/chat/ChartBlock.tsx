// 只解析本地 chart JSON 并生成 React/SVG；不执行脚本、原始 HTML 或外部资源。
import { useId } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/Icon";
import { CopyButton } from "./CopyButton";
import "./chart.css";

/** 图表仅支持具名有限数值，最多 50 个数据点。 */
export interface ChartSpec {
  type: "bar" | "line";
  title?: string;
  unit?: string;
  data: Array<{ label: string; value: number }>;
}

/** 无法完整验证时返回 null，调用方应显示完整原代码而不是丢弃内容。 */
export function parseChart(source: string): ChartSpec | null {
  if (source.length > 64_000) return null;
  let value: unknown;
  try { value = JSON.parse(source); } catch { return null; }
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (Object.keys(record).some((key) => !["type", "title", "unit", "data"].includes(key))) return null;
  if (record.type !== "bar" && record.type !== "line") return null;
  if (record.title !== undefined && (typeof record.title !== "string" || record.title.length > 160)) return null;
  if (record.unit !== undefined && (typeof record.unit !== "string" || record.unit.length > 40)) return null;
  if (!Array.isArray(record.data) || !record.data.length || record.data.length > 50) return null;
  const data: ChartSpec["data"] = [];
  for (const item of record.data) {
    if (!item || typeof item !== "object" || Array.isArray(item)) return null;
    const point = item as Record<string, unknown>;
    if (Object.keys(point).some((key) => !["label", "value"].includes(key))) return null;
    if (typeof point.label !== "string" || !point.label.trim() || point.label.length > 160 || typeof point.value !== "number" || !Number.isFinite(point.value)) return null;
    data.push({ label: point.label, value: point.value });
  }
  return { type: record.type, title: record.title as string | undefined, unit: record.unit as string | undefined, data };
}

/** 本地图形保留零基线和真实正负比例，完整值可通过键盘展开数据表读取。 */
export function ChartBlock({ chart, source }: { chart: ChartSpec; source: string }) {
  const { t } = useTranslation();
  const id = useId();
  const title = chart.title?.trim() || t(`chart.${chart.type}`);
  const width = 600;
  const height = 238;
  const left = 56;
  const right = 18;
  const top = 18;
  const bottom = 42;
  const innerWidth = width - left - right;
  const innerHeight = height - top - bottom;
  // 先归一化，避免两个各自有限的极大正负值相减时溢出 Infinity。
  const magnitude = Math.max(1, ...chart.data.map((point) => Math.abs(point.value)));
  const low = Math.min(0, ...chart.data.map((point) => point.value / magnitude));
  const high = Math.max(0, ...chart.data.map((point) => point.value / magnitude));
  const range = high - low;
  const y = (value: number) => range ? top + (high - value / magnitude) / range * innerHeight : top + innerHeight / 2;
  const zero = y(0);
  const step = innerWidth / chart.data.length;
  const x = (index: number) => chart.type === "bar" || chart.data.length === 1 ? left + (index + 0.5) * step : left + index / (chart.data.length - 1) * innerWidth;
  const ticks = [...new Set([Math.min(0, ...chart.data.map((point) => point.value)), 0, Math.max(0, ...chart.data.map((point) => point.value))])];
  const labelEvery = Math.max(1, Math.ceil(chart.data.length / 7));
  const shortLabel = (label: string) => Array.from(label).length > 12 ? `${Array.from(label).slice(0, 12).join("")}…` : label;
  const tickLabel = (value: number) => Math.abs(value) >= 1e12 || (value !== 0 && Math.abs(value) < 0.001) ? value.toExponential(2) : new Intl.NumberFormat(undefined, { notation: "compact", maximumSignificantDigits: 3 }).format(value);
  return <figure className="chart-block" aria-labelledby={`${id}-caption`}>
    <figcaption id={`${id}-caption`}><span title={title}>{title}</span>{chart.unit ? <small>{chart.unit}</small> : null}</figcaption>
    <svg className="chart-svg" viewBox={`0 0 ${width} ${height}`} role="img" aria-labelledby={`${id}-title ${id}-description`}>
      <title id={`${id}-title`}>{title}</title>
      <desc id={`${id}-description`}>{t("chart.description", { type: t(`chart.${chart.type}`), n: chart.data.length })}</desc>
      {ticks.map((tick) => <g key={tick} className="chart-tick"><line x1={left} x2={width - right} y1={y(tick)} y2={y(tick)} /><text x={left - 8} y={y(tick) + 4} textAnchor="end"><title>{String(tick)}</title>{tickLabel(tick)}</text></g>)}
      <line className="chart-baseline" x1={left} x2={width - right} y1={zero} y2={zero} />
      {chart.type === "line" ? <polyline className="chart-line" points={chart.data.map((point, index) => `${x(index)},${y(point.value)}`).join(" ")} /> : null}
      {chart.data.map((point, index) => <g key={index} data-label={point.label} data-value={point.value}>
        {chart.type === "bar" ? <>
          <rect className={`chart-bar${point.value < 0 ? " chart-bar-negative" : ""}`} x={x(index) - step * 0.32} y={Math.min(zero, y(point.value))} width={step * 0.64} height={Math.abs(y(point.value) - zero)} rx={2}><title>{point.label}: {String(point.value)}{chart.unit ? ` ${chart.unit}` : ""}</title></rect>
          {point.value === 0 ? <line className="chart-zero-mark" x1={x(index) - step * 0.22} x2={x(index) + step * 0.22} y1={zero} y2={zero} /> : null}
        </> : <circle className="chart-point" cx={x(index)} cy={y(point.value)} r={3.5}><title>{point.label}: {String(point.value)}{chart.unit ? ` ${chart.unit}` : ""}</title></circle>}
        {index % labelEvery === 0 || index === chart.data.length - 1 ? <text className="chart-x-label" x={x(index)} y={height - 18} textAnchor="middle"><title>{point.label}</title>{shortLabel(point.label)}</text> : null}
      </g>)}
    </svg>
    <div className="chart-inspect">
      <details className="chart-data"><summary><Icon name="document" size={13} />{t("chart.showData", { n: chart.data.length })}</summary><div className="message-table"><table><caption className="sr-only">{title}</caption><thead><tr><th scope="col">{t("chart.label")}</th><th scope="col">{chart.unit ? t("chart.valueWithUnit", { unit: chart.unit }) : t("chart.value")}</th></tr></thead><tbody>{chart.data.map((point, index) => <tr key={index}><th scope="row">{point.label}</th><td>{String(point.value)}</td></tr>)}</tbody></table></div></details>
      <details className="chart-source"><summary><Icon name="code" size={13} />{t("chart.showSource")}</summary><div className="message-code"><div className="message-code-head"><span>{t("chart.source")}</span><CopyButton text={source} code /></div><pre><code>{source}</code></pre></div></details>
    </div>
  </figure>;
}
