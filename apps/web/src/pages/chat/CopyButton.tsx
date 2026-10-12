// 浏览器剪贴板操作统一提示结果；正文与代码块共用。
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useToast } from "../../components/Toast";

export function CopyButton({ text, code = false }: { text: string; code?: boolean }) {
  const { t } = useTranslation();
  const toast = useToast();
  const [copied, setCopied] = useState(false);
  const reset = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(reset.current), []);
  const copy = async () => {
    try {
      if (!navigator.clipboard?.writeText) throw new Error();
      await navigator.clipboard.writeText(text);
      setCopied(true);
      clearTimeout(reset.current);
      reset.current = setTimeout(() => setCopied(false), 2000);
    } catch {
      toast.show(t("chat.copyFailed"), "error");
    }
  };
  return <button type="button" className="btn ghost sm" onClick={() => void copy()} aria-label={t(code ? "chat.copyCode" : "chat.copyMessage")}>{t(copied ? "chat.copied" : code ? "chat.copyCode" : "chat.copyMessage")}</button>;
}
