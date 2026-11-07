import React from "react";
import { createRoot } from "react-dom/client";
import { ThemeModeProvider } from "@grain/ui-shared";
import App from "./App.jsx";
import "./style.css";

const container = document.getElementById("root");
if (container) {
  createRoot(container).render(
    <React.StrictMode>
      <ThemeModeProvider>
        <App />
      </ThemeModeProvider>
    </React.StrictMode>,
  );
}
