// SPDX-License-Identifier: MIT

import { OhsApiError, parseRetryAfter } from "./errors.ts";
import {
  type CategorySample,
  type Connection,
  type Coverage,
  type DailySummary,
  type DeletionJob,
  type FhirBundle,
  type Freshness,
  type JsonObject,
  type NutritionLog,
  type Page,
  type Provider,
  type RoutePoint,
  type Sample,
  type SleepPhase,
  type SleepSession,
  type TokenScope,
  type UploadResult,
  type User,
  type UserToken,
  type Workout,
  OhsDecodeError,
  decodeCategorySample,
  decodeConnection,
  decodeCoverage,
  decodeDailySummary,
  decodeDeletionJob,
  decodeFhirBundle,
  decodeFreshness,
  decodeList,
  decodeNutritionLog,
  decodePage,
  decodeProvider,
  decodeRoutePoint,
  decodeSample,
  decodeSleepPhase,
  decodeSleepSession,
  decodeUploadResult,
  decodeUser,
  decodeUserToken,
  decodeWorkout,
} from "./models.ts";
import type { UploadPart } from "./parts.ts";
import { formatTimestamp } from "./time.ts";

export const SDK_VERSION = "0.1.0";
export const DEFAULT_USER_AGENT = `open-health-sync-react-native/${SDK_VERSION}`;

/** The part of a `fetch` Response the client reads. */
export interface FetchResponse {
  readonly status: number;
  readonly statusText?: string;
  readonly headers: { get(name: string): string | null };
  text(): Promise<string>;
}

/** The request the client passes to `fetch`. */
export interface FetchRequestInit {
  method: string;
  headers: Record<string, string>;
  body?: string | Uint8Array;
}

/** A `fetch` compatible function. The global `fetch` of React Native, Node 18+ and browsers fits. */
export type FetchLike = (url: string, init: FetchRequestInit) => Promise<FetchResponse>;

export interface OhsClientOptions {
  /** Base URL of the instance, for example `https://ohs.example.org`. */
  baseUrl: string;
  /** A fixed bearer credential. Give this or `tokenProvider`. */
  token?: string;
  /**
   * Returns the bearer credential. Called before every request, retries included, so it can
   * hand out a fresh user token from your server when the previous one expired.
   */
  tokenProvider?: () => string | Promise<string>;
  /** `fetch` implementation; defaults to the global `fetch`. */
  fetch?: FetchLike;
  /** `User-Agent` header; default `open-health-sync-react-native/0.1.0`. Browsers may ignore it. */
  userAgent?: string;
  /** Waits between upload retries; defaults to `setTimeout`. Tests pass a fake. */
  sleep?: (ms: number) => Promise<void>;
  /** Attempts per upload part, retries included. Default 5. */
  maxUploadAttempts?: number;
}

/** A time bound: RFC 3339 or `YYYY-MM-DD` string sent as is, or a `Date` sent as RFC 3339 UTC. */
export type TimeBound = string | Date;

/** Window and paging parameters shared by the list routes. */
export interface RangeQuery {
  /** Inclusive lower bound. */
  start?: TimeBound;
  /** Exclusive upper bound. */
  end?: TimeBound;
  /** The `nextCursor` of the previous page. */
  cursor?: string;
  /** Page size, 1 to 1000 (server default 100). */
  limit?: number;
}

export interface SampleQuery extends RangeQuery {
  /** Canonical quantity type, for example `heart_rate` or `step_count`. */
  type: string;
}

export interface TypedRangeQuery extends RangeQuery {
  /** Canonical type; all types when absent. */
  type?: string;
}

export interface CoverageQuery {
  provider?: string;
  start?: TimeBound;
  end?: TimeBound;
}

export interface FhirObservationQuery extends RangeQuery {
  /** Canonical quantity type, for example `heart_rate`. */
  type?: string;
  /** A FHIR token `system|code`, for example `http://loinc.org|8867-4`. */
  code?: string;
  /** An observation category, for example `vital-signs`. */
  category?: string;
}

export interface CreateUserOptions {
  /** Your own reference for the user, unique per tenant. */
  externalRef?: string;
  /** Sent as `Idempotency-Key`; a retry with the same key gets the first answer back. */
  idempotencyKey?: string;
}

type Query = Record<string, string | number | TimeBound | undefined>;

interface RequestSpec {
  method: "GET" | "POST" | "DELETE";
  path: string;
  query?: Query;
  json?: unknown;
  headers?: Record<string, string>;
  accept?: string;
}

const RETRY_BASE_MS = 1000;
const RETRY_CAP_MS = 30_000;

function seg(s: string | number): string {
  return encodeURIComponent(String(s));
}

function defaultSleep(ms: number): Promise<void> {
  const st = (globalThis as unknown as { setTimeout: (cb: () => void, ms: number) => unknown }).setTimeout;
  return new Promise((resolve) => {
    st(resolve, ms);
  });
}

function isAbort(err: unknown): boolean {
  return typeof err === "object" && err !== null && (err as { name?: unknown }).name === "AbortError";
}

/**
 * Client of the Open Health Sync REST API. One method per route. Methods return decoded
 * records and throw `OhsApiError` on a non-2xx answer.
 */
export class OhsClient {
  private readonly baseUrl: string;
  private readonly tokenProvider: () => string | Promise<string>;
  private readonly fetchImpl: FetchLike;
  private readonly userAgent: string;
  private readonly sleep: (ms: number) => Promise<void>;
  private readonly maxUploadAttempts: number;

  constructor(options: OhsClientOptions) {
    if (!options || typeof options.baseUrl !== "string" || options.baseUrl === "") {
      throw new TypeError("Open Health Sync: baseUrl is required");
    }
    this.baseUrl = options.baseUrl.replace(/\/+$/, "");
    if (options.tokenProvider) {
      this.tokenProvider = options.tokenProvider;
    } else if (typeof options.token === "string" && options.token !== "") {
      const token = options.token;
      this.tokenProvider = () => token;
    } else {
      throw new TypeError("Open Health Sync: give token or tokenProvider");
    }
    const f = options.fetch ?? (globalThis as { fetch?: FetchLike }).fetch;
    if (typeof f !== "function") {
      throw new TypeError("Open Health Sync: no global fetch; pass the fetch option");
    }
    // Call the global fetch unbound from its object, as browsers require.
    this.fetchImpl = options.fetch ?? ((url, init) => f(url, init));
    this.userAgent = options.userAgent ?? DEFAULT_USER_AGENT;
    this.sleep = options.sleep ?? defaultSleep;
    this.maxUploadAttempts = Math.max(1, options.maxUploadAttempts ?? 5);
  }

  // ---- providers and users (tenant key) ------------------------------------------------

  /** `GET /v1/providers`: connectors enabled on this instance. */
  async listProviders(): Promise<Provider[]> {
    const body = await this.json({ method: "GET", path: "/v1/providers" });
    return decodeList(body, "providers", decodeProvider);
  }

  /** `POST /v1/users`: creates an end user. */
  async createUser(options: CreateUserOptions = {}): Promise<User> {
    const headers: Record<string, string> = {};
    if (options.idempotencyKey !== undefined) headers["Idempotency-Key"] = options.idempotencyKey;
    const spec: RequestSpec = { method: "POST", path: "/v1/users", headers };
    if (options.externalRef !== undefined) spec.json = { external_ref: options.externalRef };
    return decodeUser(await this.json(spec));
  }

  /** `GET /v1/users`: one page of the tenant's users, oldest first. */
  async listUsers(query: { cursor?: string; limit?: number } = {}): Promise<Page<User>> {
    const body = await this.json({ method: "GET", path: "/v1/users", query: { ...query } });
    return decodePage(body, "users", decodeUser);
  }

  /** `GET /v1/users/{id}`. */
  async getUser(userId: string): Promise<User> {
    return decodeUser(await this.json({ method: "GET", path: `/v1/users/${seg(userId)}` }));
  }

  /** `DELETE /v1/users/{id}`: starts a hard delete and returns its job id. */
  async deleteUser(userId: string): Promise<string> {
    const body = await this.json({ method: "DELETE", path: `/v1/users/${seg(userId)}` });
    const jobId = (body as JsonObject | null)?.["job_id"];
    if (typeof jobId !== "string") throw new OhsDecodeError("Open Health Sync: no job_id in the answer");
    return jobId;
  }

  /** `GET /v1/deletions/{job_id}`: poll until `status` is `done`. */
  async getDeletion(jobId: string): Promise<DeletionJob> {
    return decodeDeletionJob(await this.json({ method: "GET", path: `/v1/deletions/${seg(jobId)}` }));
  }

  /** `POST /v1/users/{id}/tokens`: mints a user token. Never log `token`. */
  async mintUserToken(userId: string, scopes: TokenScope[], ttlSeconds?: number): Promise<UserToken> {
    const json: JsonObject = { scopes };
    if (ttlSeconds !== undefined) json["ttl_seconds"] = ttlSeconds;
    return decodeUserToken(
      await this.json({ method: "POST", path: `/v1/users/${seg(userId)}/tokens`, json }),
    );
  }

  // ---- connections ---------------------------------------------------------------------

  /** `GET /v1/users/{id}/connections`. */
  async listConnections(userId: string): Promise<Connection[]> {
    const body = await this.json({ method: "GET", path: `/v1/users/${seg(userId)}/connections` });
    return decodeList(body, "connections", decodeConnection);
  }

  /** `GET /v1/users/{id}/freshness`. */
  async getFreshness(userId: string): Promise<Freshness> {
    return decodeFreshness(await this.json({ method: "GET", path: `/v1/users/${seg(userId)}/freshness` }));
  }

  /**
   * `POST /v1/users/{id}/connections/{provider}`: returns the provider's consent URL. Open it
   * in a browser; after consent the user lands on `redirectUri?status=...&provider=...`.
   */
  async startConnection(userId: string, provider: string, redirectUri?: string): Promise<string> {
    const spec: RequestSpec = {
      method: "POST",
      path: `/v1/users/${seg(userId)}/connections/${seg(provider)}`,
    };
    if (redirectUri !== undefined) spec.json = { redirect_uri: redirectUri };
    const body = await this.json(spec);
    const url = (body as JsonObject | null)?.["authorize_url"];
    if (typeof url !== "string") throw new OhsDecodeError("Open Health Sync: no authorize_url in the answer");
    return url;
  }

  /** `DELETE /v1/users/{id}/connections/{provider}`. */
  async deleteConnection(userId: string, provider: string): Promise<void> {
    await this.send({ method: "DELETE", path: `/v1/users/${seg(userId)}/connections/${seg(provider)}` });
  }

  // ---- read routes ---------------------------------------------------------------------

  /** `GET /v1/users/{id}/samples`: quantity samples of one type. */
  async listSamples(userId: string, query: SampleQuery): Promise<Page<Sample>> {
    if (!query || !query.type) throw new TypeError("Open Health Sync: listSamples needs a type");
    return this.page(`/v1/users/${seg(userId)}/samples`, { ...query }, "samples", decodeSample);
  }

  /** `GET /v1/users/{id}/category-samples`. */
  async listCategorySamples(userId: string, query: TypedRangeQuery = {}): Promise<Page<CategorySample>> {
    return this.page(`/v1/users/${seg(userId)}/category-samples`, { ...query }, "category samples", decodeCategorySample);
  }

  /** `GET /v1/users/{id}/sleep`. */
  async listSleep(userId: string, query: RangeQuery = {}): Promise<Page<SleepSession>> {
    return this.page(`/v1/users/${seg(userId)}/sleep`, { ...query }, "sleep sessions", decodeSleepSession);
  }

  /** `GET /v1/users/{id}/sleep/{session_id}/phases`. */
  async listSleepPhases(userId: string, sessionId: number, query: RangeQuery = {}): Promise<Page<SleepPhase>> {
    return this.page(
      `/v1/users/${seg(userId)}/sleep/${seg(sessionId)}/phases`,
      { ...query },
      "sleep phases",
      decodeSleepPhase,
    );
  }

  /** `GET /v1/users/{id}/workouts`. */
  async listWorkouts(userId: string, query: TypedRangeQuery = {}): Promise<Page<Workout>> {
    return this.page(`/v1/users/${seg(userId)}/workouts`, { ...query }, "workouts", decodeWorkout);
  }

  /** `GET /v1/users/{id}/workouts/{workout_id}/route`. */
  async getWorkoutRoute(userId: string, workoutId: number, query: RangeQuery = {}): Promise<Page<RoutePoint>> {
    return this.page(
      `/v1/users/${seg(userId)}/workouts/${seg(workoutId)}/route`,
      { ...query },
      "route points",
      decodeRoutePoint,
    );
  }

  /** `GET /v1/users/{id}/nutrition`. */
  async listNutrition(userId: string, query: RangeQuery = {}): Promise<Page<NutritionLog>> {
    return this.page(`/v1/users/${seg(userId)}/nutrition`, { ...query }, "nutrition logs", decodeNutritionLog);
  }

  /** `GET /v1/users/{id}/daily`. */
  async listDaily(userId: string, query: RangeQuery = {}): Promise<Page<DailySummary>> {
    return this.page(`/v1/users/${seg(userId)}/daily`, { ...query }, "daily summaries", decodeDailySummary);
  }

  /** `GET /v1/users/{id}/coverage`: after an upload, poll until `pendingJobs` is 0. */
  async getCoverage(userId: string, query: CoverageQuery = {}): Promise<Coverage> {
    return decodeCoverage(
      await this.json({ method: "GET", path: `/v1/users/${seg(userId)}/coverage`, query: { ...query } }),
    );
  }

  // ---- FHIR ----------------------------------------------------------------------------

  /** `GET /v1/users/{id}/fhir/Observation`: one page as a FHIR R4 searchset Bundle. */
  async searchFhirObservations(userId: string, query: FhirObservationQuery = {}): Promise<FhirBundle> {
    const body = await this.json({
      method: "GET",
      path: `/v1/users/${seg(userId)}/fhir/Observation`,
      query: { ...query },
      accept: "application/fhir+json",
    });
    return decodeFhirBundle(body);
  }

  /** `GET /v1/users/{id}/fhir/Observation/{observation_id}`: the raw Observation. */
  async readFhirObservation(userId: string, observationId: string): Promise<JsonObject> {
    const body = await this.json({
      method: "GET",
      path: `/v1/users/${seg(userId)}/fhir/Observation/${seg(observationId)}`,
      accept: "application/fhir+json",
    });
    if (body === null || typeof body !== "object" || Array.isArray(body)) {
      throw new OhsDecodeError("Open Health Sync: expected a JSON object for Observation");
    }
    return body as JsonObject;
  }

  // ---- upload --------------------------------------------------------------------------

  /**
   * `POST /v1/ingest/apple-health`: uploads one part. Needs a user token with the `upload`
   * scope. Retries 429, 5xx and network errors with exponential backoff (1 s doubling, at
   * most 30 s, or `Retry-After` when sent), up to `maxUploadAttempts` attempts in all. Any
   * other non-2xx answer throws at once.
   */
  async uploadAppleHealthPart(part: UploadPart): Promise<UploadResult> {
    const spec: RequestSpec = {
      method: "POST",
      path: "/v1/ingest/apple-health",
      headers: {
        "Content-Type": "application/gzip",
        "X-OHS-Type": part.type,
        "X-OHS-Day": part.day,
        "X-OHS-Batch": part.batch,
        "X-OHS-Part": String(part.partNumber),
      },
    };
    for (let attempt = 1; ; attempt++) {
      let res: FetchResponse;
      try {
        res = await this.fetchRaw(spec, part.body);
      } catch (err) {
        if (isAbort(err) || attempt >= this.maxUploadAttempts) throw err;
        await this.sleep(this.backoff(attempt));
        continue;
      }
      if (res.status >= 200 && res.status < 300) {
        return decodeUploadResult(await this.parseBody(res));
      }
      const error = await this.toError(res);
      const retryable = res.status === 429 || res.status >= 500;
      if (!retryable || attempt >= this.maxUploadAttempts) throw error;
      const delay =
        error.retryAfterSeconds !== undefined ? error.retryAfterSeconds * 1000 : this.backoff(attempt);
      await this.sleep(delay);
    }
  }

  /** Uploads parts one after the other and returns their results in order. */
  async uploadParts(parts: Iterable<UploadPart>): Promise<UploadResult[]> {
    const out: UploadResult[] = [];
    for (const p of parts) out.push(await this.uploadAppleHealthPart(p));
    return out;
  }

  // ---- plumbing ------------------------------------------------------------------------

  private backoff(attempt: number): number {
    return Math.min(RETRY_CAP_MS, RETRY_BASE_MS * 2 ** (attempt - 1));
  }

  private async page<T>(path: string, query: Query, what: string, item: (raw: unknown) => T): Promise<Page<T>> {
    return decodePage(await this.json({ method: "GET", path, query }), what, item);
  }

  private url(path: string, query?: Query): string {
    let url = this.baseUrl + path;
    if (query) {
      const pairs: string[] = [];
      for (const [k, v] of Object.entries(query)) {
        if (v === undefined || v === null) continue;
        const s = v instanceof Date ? formatTimestamp(v) : String(v);
        pairs.push(`${encodeURIComponent(k)}=${encodeURIComponent(s)}`);
      }
      if (pairs.length > 0) url += "?" + pairs.join("&");
    }
    return url;
  }

  private async fetchRaw(spec: RequestSpec, rawBody?: Uint8Array): Promise<FetchResponse> {
    const token = await this.tokenProvider();
    const headers: Record<string, string> = {
      Accept: spec.accept ?? "application/json",
      "User-Agent": this.userAgent,
      ...spec.headers,
      Authorization: `Bearer ${token}`,
    };
    const init: FetchRequestInit = { method: spec.method, headers };
    if (rawBody !== undefined) {
      init.body = rawBody;
    } else if (spec.json !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(spec.json);
    }
    return this.fetchImpl(this.url(spec.path, spec.query), init);
  }

  private async send(spec: RequestSpec): Promise<FetchResponse> {
    const res = await this.fetchRaw(spec);
    if (res.status < 200 || res.status >= 300) throw await this.toError(res);
    return res;
  }

  private async json(spec: RequestSpec): Promise<unknown> {
    return this.parseBody(await this.send(spec));
  }

  private async parseBody(res: FetchResponse): Promise<unknown> {
    const text = await res.text();
    if (text === "") return null;
    try {
      return JSON.parse(text);
    } catch {
      throw new OhsDecodeError(`Open Health Sync: the answer to a ${res.status} is not JSON`);
    }
  }

  private async toError(res: FetchResponse): Promise<OhsApiError> {
    let body: JsonObject | null = null;
    try {
      const text = await res.text();
      const parsed: unknown = text ? JSON.parse(text) : null;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) body = parsed as JsonObject;
    } catch {
      body = null;
    }
    const str = (k: string): string | undefined => {
      const v = body?.[k];
      return typeof v === "string" && v !== "" ? v : undefined;
    };
    const init: ConstructorParameters<typeof OhsApiError>[0] = { status: res.status };
    const title = str("title") ?? (res.statusText ? res.statusText : undefined);
    if (title !== undefined) init.title = title;
    const detail = str("detail");
    if (detail !== undefined) init.detail = detail;
    const type = str("type");
    if (type !== undefined) init.type = type;
    const requestId = str("request_id") ?? res.headers.get("X-Request-Id") ?? undefined;
    if (requestId) init.requestId = requestId;
    const retryAfter = parseRetryAfter(res.headers.get("Retry-After"));
    if (retryAfter !== undefined) init.retryAfterSeconds = retryAfter;
    return new OhsApiError(init);
  }
}
