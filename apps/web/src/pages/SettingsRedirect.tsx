import { Navigate, useLocation } from "react-router-dom";
import { settingsHref, type SettingsSection } from "../lib/settingsNavigation";

/** 旧路由仍有效；replace 避免浏览器后退时再次陷入中转页。 */
export function SettingsRedirect({ section }: { section: SettingsSection }) {
  const location = useLocation();
  return <Navigate to={settingsHref(section, location.search)} state={location.state} replace />;
}
