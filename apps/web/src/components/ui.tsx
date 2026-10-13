// 少量手写基础组件：对话框、表单字段、徽标、空态、错误条。

import { useEffect, useId, useRef, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { describeError } from "../lib/errorMessage";
import { Icon } from "./Icon";

export function Dialog({
  open,
  title,
  onClose,
  children,
  footer,
  dismissible = true,
  className,
}: {
  open: boolean;
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  /** false 时没有关闭按钮，Esc 也不关闭：必须通过底部按钮做出决定（例如审批）。 */
  dismissible?: boolean;
  className?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const { t } = useTranslation();

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && el.open) el.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      className={className ? `dialog ${className}` : "dialog"}
      aria-labelledby={titleId}
      onClose={() => {
        if (dismissible) onClose();
      }}
      onCancel={(e) => {
        e.preventDefault();
        if (dismissible) onClose();
      }}
    >
      {open ? (
        <>
          <div className="dialog-head">
            <span id={titleId}>{title}</span>
            <span className="spacer" />
            {dismissible ? (
              <button type="button" className="btn ghost sm" onClick={onClose} aria-label={t("common.close")}>
                <Icon name="close" size={16} />
              </button>
            ) : null}
          </div>
          <div className="dialog-body">{children}</div>
          {footer ? <div className="dialog-foot">{footer}</div> : null}
        </>
      ) : null}
    </dialog>
  );
}

export function Field({ label, hint, children, inline }: { label: ReactNode; hint?: ReactNode; children: ReactNode; inline?: boolean }) {
  return (
    <div className={inline ? "field inline" : "field"}>
      <label>{label}</label>
      {children}
      {hint ? <span className="hint">{hint}</span> : null}
    </div>
  );
}

export function Badge({ tone, children, title }: { tone?: "ok" | "warn" | "bad" | "accent"; children: ReactNode; title?: string }) {
  return <span className={tone ? `badge ${tone}` : "badge"} title={title}>{children}</span>;
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function ErrorBanner({ error }: { error: unknown }) {
  const { t } = useTranslation();
  if (!error) return null;
  return (
    <div className="banner error" role="alert">
      {describeError(error, t)}
    </div>
  );
}

export function Spinner() {
  const { t } = useTranslation();
  return <span className="status-item">{t("common.loading")}</span>;
}
