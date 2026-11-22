// Realistic examples for the API reference. The OpenAPI file gives an `example` to about one field
// in eight; the sampler fills the rest with "string", 0 and a 2019 date, which reads as a template,
// not as data. This fills every primitive field and parameter that has no example, enum or const
// with a value guessed from its name, type and format, so the request and response samples of all
// endpoint pages look like a real heart rate reading from a real ring. An example written in the
// OpenAPI file always wins.

type Schema = {
  type?: string | string[];
  format?: string;
  example?: unknown;
  examples?: unknown;
  enum?: unknown[];
  const?: unknown;
  default?: unknown;
  $ref?: string;
  properties?: Record<string, Schema>;
  items?: Schema;
  [key: string]: unknown;
};

/** A stable, uuid-shaped id per field name, so the same field reads the same on every page. */
function uuidFor(name: string): string {
  let h = 2166136261;
  for (const c of name) h = Math.imul(h ^ c.charCodeAt(0), 16777619) >>> 0;
  const hex = (n: number) => (n >>> 0).toString(16).padStart(8, "0");
  const a = hex(h);
  const b = hex(Math.imul(h, 2654435761));
  const c = hex(Math.imul(h ^ 0x9e3779b9, 40503));
  const d = hex(Math.imul(h, 2246822519));
  return `${a}-${b.slice(0, 4)}-4${b.slice(5, 8)}-${"89ab"[h % 4]}${c.slice(1, 4)}-${c.slice(4)}${d}`;
}

// The user every sample is about, so a path, a body and a response agree.
const userId = "3f6c1e2a-8b4d-4c7e-9a51-2d0b7e64f9c3";

const strings: Record<string, string> = {
  value: "62",
  provider: "oura",
  source: "oura",
  slug: "oura",
  source_name: "Oura Ring",
  device: "Oura Ring Gen3",
  device_model: "Gen3",
  product_type: "Ring",
  source_bundle_id: "com.ouraring.oura",
  unit: "bpm",
  normalized_unit: "bpm",
  type: "heart_rate",
  data_type: "heart_rate",
  event_type: "samples.created",
  timezone: "America/Los_Angeles",
  tz: "America/Los_Angeles",
  next_cursor: "c2FtcGxlczoyMDI2LTExLTIwVDA3OjAyOjAwWg",
  cursor: "c2FtcGxlczoyMDI2LTExLTIwVDA3OjAyOjAwWg",
  last_event_id: "1842",
  name: "iOS app backend",
  app_name: "Pulse Journal",
  tenant_name: "Pulse Journal",
  label: "Morning reading",
  description: "Backend of the iOS app",
  note: "After a 5 km run",
  text: "Slept badly, late coffee",
  email: "dev@example.com",
  recipient: "care-team@example.com",
  key: "ohs_sk_EXAMPLE7Hq2Lw9x",
  api_key: "ohs_sk_EXAMPLE7Hq2Lw9x",
  prefix: "ohs_sk_EXAM",
  key_prefix: "ohs_sk_EXAM",
  token: "ohs_ut_EXAMPLEk3pQ8vN",
  setup_token: "ohs_st_0123456789abcdef",
  session: "ses_5d1e9b2c7a",
  csrf_token: "csrf_0123456789abcdef",
  state: "c9f2a71e4b",
  code: "8867-4",
  loinc: "8867-4",
  display: "Heart rate",
  system: "http://loinc.org",
  external_id: "user-1842",
  external_ref: "user-1842",
  request_id: "req_8f2c1a6d",
  series_id: "hr-2026-11-20",
  meal_type: "breakfast",
  specimen: "Serum",
  performer: "Example Lab",
  orderer: "Example Clinic",
  comparator: "<",
  value_string: "Negative",
  value_code: "N",
  value_system: "http://terminology.hl7.org/CodeSystem/v3-ObservationInterpretation",
  interpretation_text: "Within the reference range",
  reference_text: "60 to 100 bpm",
  value_label: "Light",
  symptoms_status: "present",
  error: "provider answered 429, retrying",
  last_error: "provider answered 429, retrying",
  reload_error: "",
  message: "ok",
  title: "Not found",
  detail: "No user with this id.",
  host: "ohs.example.com",
  version: "0.3.0",
  method: "GET",
  route: "/v1/users/{id}/samples",
  action: "user.create",
  tier: "standard",
  section: "Retention",
  help: "Days a raw payload is kept.",
  evidence: "heart rate above 180 bpm for 10 minutes",
  passcode: "482913",
  password: "correct-horse-battery-staple",
};

const numbers: Record<string, number> = {
  value: 62,
  normalized_value: 62,
  rr_ms: 845,
  mean_rr_ms: 912,
  resting_hr: 54,
  avg_heart_rate: 128,
  max_heart_rate: 171,
  average_heart_rate: 64,
  mean_heart_rate_bpm: 66,
  hrv_avg_ms: 48.2,
  sdnn_ms: 52.4,
  rmssd_ms: 41.7,
  pnn50_percent: 18.5,
  respiratory_rate: 14.2,
  spo2_avg: 97.1,
  latitude: 37.4419,
  longitude: -122.143,
  altitude_m: 18.4,
  speed_mps: 2.9,
  course_deg: 214,
  vo2_max: 47.3,
  sampling_frequency_hz: 512,
  reference_low: 60,
  reference_high: 100,
  sum: 8421,
  avg: 62,
  min: 48,
  max: 128,
};

const integers: Record<string, number> = {
  status: 200,
  status_code: 200,
  last_status_code: 200,
  step_count: 8421,
  floors_climbed: 6,
  limit: 100,
  days: 30,
  attempt: 1,
  attempts: 1,
  duration_ms: 184,
  revision: 12,
  count: 42,
  records: 1284,
  intervals: 412,
  accepted_intervals: 405,
  successive_pairs: 404,
  number_of_measurements: 15360,
  period_length_days: 5,
  cycle_length_days: 28,
  classification_value: 1,
  value_int: 1,
  value: 62,
  pull_every_seconds: 900,
  ttl_seconds: 3600,
  lag_seconds: 120,
};

function dateTimeFor(n: string): string {
  if (/expires|until|next_|retry/.test(n)) return "2026-12-20T07:02:00Z";
  if (/(^|_)end|finished|_to$|bedtime_end|window_end|collected_end/.test(n)) return "2026-11-20T07:03:00Z";
  if (/created|granted|connected|issued|ingested|updated|seen|delivered|last_/.test(n)) return "2026-11-20T07:04:12Z";
  return "2026-11-20T07:02:00Z";
}

/** The id of a record: the user's own id on a user, a stable id per schema elsewhere. */
function idFor(n: string, ctx: string): string {
  if (n === "user_id" || n === "patient_id") return userId;
  // webhook_id reads the same as the id of a Webhook, so a path and a response agree
  if (n !== "id") return uuidFor(`${n.charAt(0).toUpperCase()}${n.slice(1, -3)}.id`);
  return ctx === "" || /^user$/i.test(ctx) ? userId : uuidFor(`${ctx}.id`);
}

// Nullable fields that are null on a healthy record: an error, a revocation, a failure streak.
const nullWhenHealthy = /revoked|disabled|failing|error|paused|replay_of|previous_|deleted|broken|invalid|reason/;

function stringFor(n: string, format: string | undefined, ctx: string): string {
  if (format === "uuid") return idFor(n, ctx);
  if (format === "date-time" || /_at$|^at$|timestamp|^(start|end|from|to|since|as_of)$/.test(n)) return dateTimeFor(n);
  if (format === "date" || /_day$|^date$/.test(n)) return /end|last/.test(n) ? "2026-11-20" : "2026-11-14";
  if (format === "password") return strings.password;
  if (n in strings) return strings[n];
  if (format === "uri" || /url|uri$/.test(n)) {
    if (/redirect/.test(n)) return "https://app.example.com/oauth/callback";
    if (/logo/.test(n)) return "https://app.example.com/logo.png";
    if (/authorize/.test(n)) return "https://cloud.ouraring.com/oauth/authorize?client_id=...";
    return "https://app.example.com/webhooks/ohs";
  }
  if (n === "id" || n.endsWith("_id")) return idFor(n, ctx);
  // Last resort: the field name in words, still better than "string".
  return n
    .replace(/([a-z])([A-Z])/g, "$1 $2")
    .replaceAll("_", " ")
    .toLowerCase();
}

function numberFor(n: string, integer: boolean): number {
  if (integer) {
    if (n in integers) return integers[n];
    // counters of trouble read 0 on a healthy record
    if (/fail|reject|park|broken|disabled|retried|reauth|revoked|deleting/.test(n)) return 0;
    if (n === "id" || n.endsWith("_id")) return 48213;
    if (/_seconds$|_s$/.test(n)) return /sleep/.test(n) ? 5400 : 3600;
    if (/bytes/.test(n)) return 482133;
    return 3;
  }
  if (n in numbers) return numbers[n];
  if (/_score$/.test(n)) return 82;
  if (/kcal$/.test(n)) return 420;
  if (/_mps$/.test(n)) return 2.9;
  if (/_ms$/.test(n)) return 845;
  if (/_m$/.test(n)) return 1250;
  if (/_g$/.test(n)) return 24;
  if (/_mg$/.test(n)) return 410;
  if (/percent|_pct$/.test(n)) return 18.5;
  return 1.5;
}

function exampleFor(name: string, schema: Schema, ctx = ""): unknown {
  if (schema.$ref || schema.example !== undefined || schema.examples !== undefined) return undefined;
  if (schema.enum || schema.const !== undefined || schema.default !== undefined) return undefined;
  // "date-time or date" and the like: read the first variant that says its type
  const variants = ([] as Schema[]).concat((schema.anyOf as Schema[]) ?? [], (schema.oneOf as Schema[]) ?? []);
  if (schema.type === undefined && variants.length) {
    const first = variants.find((v) => v && typeof v === "object" && !v.$ref && v.type !== undefined);
    if (!first) return undefined;
    const nullable = variants.some((v) => v?.type === "null");
    return exampleFor(
      name,
      { type: nullable ? [first.type as string, "null"] : first.type, format: first.format },
      ctx,
    );
  }
  const all = Array.isArray(schema.type) ? schema.type : [schema.type];
  const types = all.filter((t) => t && t !== "null");
  const n = name.toLowerCase();
  if (all.includes("null") && nullWhenHealthy.test(n)) return null;
  switch (types[0]) {
    case "string":
      return stringFor(n, schema.format, ctx);
    case "integer":
      return numberFor(n, true);
    case "number":
      return numberFor(n, false);
    default:
      return undefined;
  }
}

function fill(name: string, schema: Schema | undefined, ctx: string) {
  if (!schema || typeof schema !== "object") return;
  // The source each daily metric came from: a map of metric to provider.
  if (name === "sources" && typeof schema.additionalProperties === "object" && schema.examples === undefined) {
    schema.examples = [{ step_count: "apple_health", sleep_score: "oura", resting_hr: "oura" }];
    return;
  }
  const value = exampleFor(name, schema, ctx);
  // JSON Schema form: the sampler of fumadocs reads `examples`, not the OpenAPI 3.0 `example`.
  if (value !== undefined) schema.examples = [value];
}

/** Walks the whole document and gives an example to every field and parameter left without one. */
export function addExamples(node: unknown, ctx = "", schemaMap = false): void {
  if (Array.isArray(node)) {
    for (const item of node) addExamples(item, ctx);
    return;
  }
  if (!node || typeof node !== "object") return;
  const obj = node as Record<string, unknown>;
  // A parameter: its example goes on the parameter, where the request samples read it. Only the
  // required ones, so a sample is the smallest call that works (no cursor on a first page).
  if (
    typeof obj.name === "string" &&
    typeof obj.in === "string" &&
    obj.schema &&
    obj.example === undefined &&
    (obj.required === true || obj.in === "path")
  ) {
    const schema = obj.schema as Schema;
    const value = exampleFor(obj.name, schema);
    if (value !== undefined) obj.example = value;
  }
  // A schema with an OpenAPI 3.0 `example`: the sampler reads `examples`, so the example written
  // in the file shows in the samples.
  if (
    obj.in === undefined &&
    obj.example !== undefined &&
    obj.examples === undefined &&
    (obj.type !== undefined || obj.properties !== undefined || obj.items !== undefined)
  ) {
    obj.examples = [obj.example];
  }
  if (obj.properties && typeof obj.properties === "object") {
    for (const [key, prop] of Object.entries(obj.properties as Record<string, Schema>)) fill(key, prop, ctx);
  }
  // A named primitive schema (TimeTagTime, a date-time) gets an example from its own name.
  if (schemaMap) {
    for (const [key, value] of Object.entries(obj)) {
      fill(key.replace(/([a-z0-9])([A-Z])/g, "$1_$2").toLowerCase(), value as Schema, key);
    }
  }
  // Under components.schemas, each key names the record its fields belong to.
  for (const [key, value] of Object.entries(obj)) addExamples(value, schemaMap ? key : ctx, key === "schemas");
}
