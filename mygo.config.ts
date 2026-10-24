import { defineConfig } from "mygo-cli";

export default defineConfig({
  name: "LZSync",
  identifier: "com.example.lzsync",
  version: "0.1.0",
  // `mygo dev` runs devCommand and loads devUrl; `mygo build` runs
  // buildCommand and embeds frontendDist into the app.
  devUrl: "http://localhost:5173",
  // npm 而非 bun：mygo 子进程按 PATH 裸调命令，bun 只装在
  // ~/.npm-global（不在系统 PATH），npm 恒在 guix home profile。
  devCommand: "npm run dev:web",
  buildCommand: "npm run build:web",
  frontendDist: "dist",
  bindings: "src/mygo.ts",
  out: "build",
});
