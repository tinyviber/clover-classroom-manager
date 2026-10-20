import { defineConfig } from "mygo-cli";

export default defineConfig({
  name: "lab-assistant",
  identifier: "com.example.labassistant",
  version: "0.0.2",
  // `mygo dev` runs devCommand and loads devUrl; `mygo build` runs
  // buildCommand and embeds frontendDist into the app.
  devUrl: "http://localhost:5173",
  devCommand: "bun run dev:web",
  buildCommand: "bun run build:web",
  frontendDist: "dist",
  bindings: "src/mygo.ts",
  out: "build",
});
