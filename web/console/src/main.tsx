import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./App";
import { readPref } from "./lib/prefs";
import { applyTheme, parseTheme } from "./shell/theme";
import "./styles/base.css";
import "./styles/tokens.css";
import "./styles/components.css";
import "./styles/shell.css";
import "./styles/inspector.css";
import "./styles/overview.css";

const root = document.getElementById("root");
if (!root) {
  throw new Error("index.html lacks #root");
}
applyTheme(parseTheme(readPref("theme")));
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
