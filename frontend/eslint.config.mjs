import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

export default defineConfig([
  ...nextVitals,
  ...nextTs,
  // Build output and Next.js-generated files. `next lint` skipped these
  // implicitly; plain `eslint .` does not.
  globalIgnores([".next/**", "out/**", "next-env.d.ts"]),
]);
