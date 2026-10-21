// SPDX-License-Identifier: AGPL-3.0-only
//
// Settings from the environment (.env.local in development). Read on the server only: none of
// these names starts with NEXT_PUBLIC_, so Next.js never copies them into browser code.

export interface Config {
  /** Base URL of the Open Health Sync instance, without a trailing slash. */
  ohsUrl: string;
  /** Tenant API key, ohs_sk_... It never leaves the server. */
  apiKey: string;
  /** whsec_... secret of the webhook endpoint, when one is registered. */
  webhookSecret: string | undefined;
  /** Where this app is reached, for redirects back from the connection page and the webhook URL. */
  appUrl: string;
}

export interface ConfigState {
  config: Config | undefined;
  /** Required variables that are not set. */
  missing: string[];
}

export function readConfig(env: Record<string, string | undefined> = process.env): ConfigState {
  const ohsUrl = env.OHS_URL?.trim().replace(/\/+$/, "") ?? "";
  const apiKey = env.OHS_API_KEY?.trim() ?? "";
  const missing: string[] = [];
  if (!ohsUrl) missing.push("OHS_URL");
  if (!apiKey) missing.push("OHS_API_KEY");
  if (missing.length > 0) return { config: undefined, missing };
  return {
    config: {
      ohsUrl,
      apiKey,
      webhookSecret: env.OHS_WEBHOOK_SECRET?.trim() || undefined,
      appUrl: appUrl(env),
    },
    missing,
  };
}

// Where the app is reached: APP_URL, else the production address Vercel
// gives a deployment, else this machine.
function appUrl(env: Record<string, string | undefined>): string {
  const vercel = env.VERCEL_PROJECT_PRODUCTION_URL?.trim();
  return (env.APP_URL?.trim() || (vercel ? `https://${vercel}` : "http://localhost:3000")).replace(/\/+$/, "");
}

/** The settings, or an error naming what is missing. */
export function requireConfig(): Config {
  const { config, missing } = readConfig();
  if (!config) throw new Error(`Set ${missing.join(" and ")} in .env.local, then restart the app.`);
  return config;
}

/** The URL this app receives webhooks on. */
export function webhookUrl(config: Config): string {
  return `${config.appUrl}/api/webhooks`;
}
