import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  {
    // next/image buys nothing here. next.config.ts sets `output: 'export'` and
    // `images.unoptimized: true`, so <Image> renders the same bytes as <img>
    // while demanding width/height (a layout risk on the preview thumbnails and
    // channel logos it would replace) and shipping a client component for it.
    // The rule's premise — a Next optimizer to route through — is absent, so it
    // is off project-wide rather than disabled ten times inline. Re-enable it
    // if this ever becomes a server deployment with image optimization on.
    rules: {
      "@next/next/no-img-element": "off",
    },
  },
  // Override default ignores of eslint-config-next.
  globalIgnores([
    // Default ignores of eslint-config-next:
    ".next/**",
    "out/**",
    "build/**",
    "next-env.d.ts",
  ]),
]);

export default eslintConfig;
