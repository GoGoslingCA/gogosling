import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { discoverConnection } from "./connection";
import { initI18n } from "./i18n";
import "./styles.css";

async function boot(): Promise<void> {
  initI18n();
  // 先发现连接（桌面壳 / URL hash），再挂载，避免首屏先闪一次连接页。
  const connection = await discoverConnection();
  const root = document.getElementById("root");
  if (!root) throw new Error("#root not found");
  createRoot(root).render(
    <StrictMode>
      <App initialConnection={connection} />
    </StrictMode>,
  );
}

void boot();
