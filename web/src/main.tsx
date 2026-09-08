import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "@/App";
import { watchInteraction } from "@/lib/activity";
import "@/index.css";

// Started once, for the life of the page. What it records is what tells the
// host somebody is still here, as opposed to a window left open on a page that
// refreshes itself.
watchInteraction();

const root = document.getElementById("root");
if (!root) throw new Error("no #root element");

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
