import { defineConfig } from "tsup";

export default defineConfig({
  entry: [
    "src/index.ts",
    "src/tools/adapters/vercel.ts",
    "src/tools/adapters/langchain.ts",
    "src/tools/adapters/mastra.ts",
  ],
  format: ["esm"],
  dts: true,
  sourcemap: true,
  clean: true,
  target: "node18",
  splitting: true,
});
