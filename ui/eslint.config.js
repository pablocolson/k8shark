import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

// Bug-focused flat config: TypeScript's own recommended rules plus the
// react-hooks rules (exhaustive-deps / rules-of-hooks) that matter most on
// useHub.ts / useWorkers.ts, where a stale closure or a missing dependency in
// a WS-reconnect effect is a real defect. Not a style crusade — Prettier-style
// formatting is left to the editor.
export default tseslint.config(
  { ignores: ["dist/", "node_modules/", "*.config.*"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}"],
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // The codebase deliberately uses a few `any`-ish escape hatches at the
      // WS/JSON boundary; keep them a warning, not a hard error.
      "@typescript-eslint/no-explicit-any": "warn",
    },
  },
  {
    files: ["**/*.test.{ts,tsx}", "src/test-setup.ts"],
    rules: {
      "@typescript-eslint/no-explicit-any": "off",
    },
  }
);
