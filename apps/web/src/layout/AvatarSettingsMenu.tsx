// 头像仅作为本地设置入口；复用现有设置路由，不创建账号状态或后台请求。
import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { Link, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Icon } from "../components/Icon";
import { settingsHref, settingsReturnTarget } from "../lib/settingsNavigation";
import { readUiPreferences } from "../lib/uiPreferences";
import { APP_RELEASE_LABEL } from "../lib/release";
import personIcon from "../assets/icons/person-fill.svg";
import "./avatarSettingsMenu.css";

const shortcuts = [
  { section: "general", label: "settings.title", icon: "settings" },
  { section: "appearance", label: "preferences.appearance", icon: "spark" },
  { section: "models", label: "settingsLayout.models", icon: "model" },
] as const;

/** 复用已有设置内容，菜单随路由切换与失焦关闭，不影响页面对话框。 */
export function AvatarSettingsMenu({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void; resourcesAvailable: boolean }) {
  const { t } = useTranslation();
  const location = useLocation();
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState({ left: 8, top: 8, maxHeight: 480 });
  const returnTo = location.pathname === "/settings"
    ? settingsReturnTarget(location.state, readUiPreferences().lastSessionId)
    : location.pathname + location.search;

  useEffect(() => { onOpenChange(false); }, [location.pathname, location.search, onOpenChange]);
  useLayoutEffect(() => {
    if (!open) return;
    const place = () => {
      if (!trigger.current || !menu.current) return;
      const anchor = trigger.current.getBoundingClientRect();
      const availableAbove = Math.max(0, anchor.top - 16);
      const availableBelow = Math.max(0, window.innerHeight - anchor.bottom - 16);
      const above = availableAbove >= Math.min(menu.current.scrollHeight, 480) || availableAbove >= availableBelow;
      const maxHeight = Math.max(0, Math.min(480, above ? availableAbove : availableBelow));
      const height = Math.min(menu.current.scrollHeight, maxHeight);
      const left = Math.max(8, Math.min(anchor.left, window.innerWidth - menu.current.getBoundingClientRect().width - 8));
      setPosition({ left, top: above ? Math.max(8, anchor.top - height - 8) : anchor.bottom + 8, maxHeight });
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => { window.removeEventListener("resize", place); window.removeEventListener("scroll", place, true); };
  }, [open]);
  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLAnchorElement>("[role=menuitem]")?.focus();
    const outside = (event: Event) => {
      if (event.target instanceof Node && !menu.current?.contains(event.target) && !trigger.current?.contains(event.target)) onOpenChange(false);
    };
    const blur = () => onOpenChange(false);
    window.addEventListener("pointerdown", outside);
    window.addEventListener("focusin", outside);
    window.addEventListener("blur", blur);
    return () => { window.removeEventListener("pointerdown", outside); window.removeEventListener("focusin", outside); window.removeEventListener("blur", blur); };
  }, [open, onOpenChange]);
  const navigateMenu = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape" || event.key === "Tab") {
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); }
      onOpenChange(false); trigger.current?.focus();
      return;
    }
    if (!["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const items = Array.from(event.currentTarget.querySelectorAll<HTMLAnchorElement>("[role=menuitem]"));
    const index = items.indexOf(document.activeElement as HTMLAnchorElement);
    const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1 : (index + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
    items[next]?.focus();
  };
  const version = t("profile.version", { version: APP_RELEASE_LABEL });
  return <>
    <button ref={trigger} type="button" className="nav-avatar-trigger" aria-label={t("profile.menuLabel")} aria-haspopup="menu" aria-expanded={open} aria-controls={open ? id : undefined} onClick={() => onOpenChange(!open)} onKeyDown={(event) => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); onOpenChange(true); }
    }}>
      <span className="nav-avatar"><img src={personIcon} alt="" aria-hidden="true" /></span>
      <span className="nav-avatar-copy"><strong>{t("app.title")}</strong><span>{version}</span></span>
      <Icon name="more" size={16} />
    </button>
    {open ? createPortal(<div id={id} ref={menu} className="avatar-settings-menu" role="menu" aria-label={t("profile.menuLabel")} style={position} onKeyDown={navigateMenu}>
      <div className="avatar-settings-heading" role="presentation"><span className="nav-avatar"><img src={personIcon} alt="" aria-hidden="true" /></span><div><strong>{t("app.title")}</strong><span>{version}</span></div></div>
      {shortcuts.map(({ section, label, icon }) => <Link key={section} role="menuitem" tabIndex={-1} className="avatar-settings-item" to={settingsHref(section, location.search)} state={{ returnTo }} onClick={() => onOpenChange(false)}><Icon name={icon} size={17} /><span>{t(label)}</span><Icon name="chevron" size={13} /></Link>)}
    </div>, document.body) : null}
  </>;
}
