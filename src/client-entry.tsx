import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { AppProviders } from "./AppProviders";
import { ClientApp } from "./client/ClientApp";
import "./index.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppProviders>
      <ClientApp />
    </AppProviders>
  </StrictMode>,
);
