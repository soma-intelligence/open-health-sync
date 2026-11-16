import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import { defineConfig } from "eslint/config";
import globals from "globals";
import tseslint from "typescript-eslint";

const jsx = (attr, why) => ({
  selector: `JSXAttribute[name.name='${attr}']`,
  message: why,
});
const member = (prop, why) => ({
  selector: `MemberExpression[property.name='${prop}']`,
  message: why,
});

const syntax = [
  jsx("style", "The CSP has style-src 'self': an inline style is blocked. Use a class."),
  jsx("dangerouslySetInnerHTML", "Never inject HTML: API data is rendered as text."),
  member("innerHTML", "Never write HTML from a string: it is an XSS sink."),
  member("outerHTML", "Never write HTML from a string: it is an XSS sink."),
  member("insertAdjacentHTML", "Never write HTML from a string: it is an XSS sink."),
  { selector: "CallExpression[callee.name='eval']", message: "eval is blocked by the CSP and is an injection sink." },
  { selector: "NewExpression[callee.name='Function']", message: "Function is eval in disguise and is blocked by the CSP." },
  { selector: "NewExpression[callee.name='XMLHttpRequest']", message: "Every request goes through src/api/client.ts (fetch)." },
  { selector: "MemberExpression[object.name='console']", message: "No console output in shipped code: it can leak health data." },
];

export default defineConfig(
  { ignores: ["src/api/schema.ts", "../../internal/console/dist", "eslint.config.js", "node_modules", "playwright-report", "test-results"] },
  js.configs.recommended,
  tseslint.configs.strictTypeChecked,
  reactHooks.configs.flat.recommended,
  {
    languageOptions: {
      globals: globals.browser,
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
    },
    rules: {
      "no-restricted-syntax": ["error", ...syntax],
      "no-restricted-globals": [
        "error",
        { name: "fetch", message: "Every request goes through src/api/client.ts: it adds the CSRF token and handles the session." },
        { name: "XMLHttpRequest", message: "Every request goes through src/api/client.ts (fetch)." },
        { name: "localStorage", message: "Only src/lib/prefs.ts reads or writes localStorage (three preferences)." },
        { name: "sessionStorage", message: "The page never stores a key or a token: sessionStorage is not used." },
      ],
    },
  },
  {
    files: ["src/api/client.ts", "e2e/**"],
    rules: {
      "no-restricted-globals": [
        "error",
        { name: "XMLHttpRequest", message: "Every request goes through src/api/client.ts (fetch)." },
        { name: "localStorage", message: "Only src/lib/prefs.ts reads or writes localStorage (three preferences)." },
        { name: "sessionStorage", message: "The page never stores a key or a token: sessionStorage is not used." },
      ],
    },
  },
  {
    files: ["src/lib/prefs.ts"],
    rules: {
      "no-restricted-globals": [
        "error",
        { name: "fetch", message: "Every request goes through src/api/client.ts." },
        { name: "XMLHttpRequest", message: "Every request goes through src/api/client.ts (fetch)." },
        { name: "sessionStorage", message: "The page never stores a key or a token: sessionStorage is not used." },
      ],
    },
  },
);
