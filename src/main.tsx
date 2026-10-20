import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { AppProviders } from "./AppProviders";
import { AdminApp } from "./admin/AdminApp";
import "./index.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppProviders>
      <AdminApp />
    </AppProviders>
  </StrictMode>,
);
