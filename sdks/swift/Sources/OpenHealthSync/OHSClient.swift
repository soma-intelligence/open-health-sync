// SPDX-License-Identifier: AGPL-3.0-only

import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// A `start` or `end` bound: an RFC 3339 time or a `YYYY-MM-DD` date, sent as is, or a
/// `Date`, sent as RFC 3339 in UTC.
public struct OHSTime: Sendable, Equatable, ExpressibleByStringLiteral, CustomStringConvertible {
    public let value: String

    public init(_ value: String) {
        self.value = value
    }

    public init(_ date: Date) {
        self.value = RFC3339.format(date)
    }

    public init(stringLiteral value: String) {
        self.value = value
    }

    public var description: String { value }
}

/// A client of the Open Health Sync REST API.
///
/// Build it with the base URL of the instance and either a fixed bearer credential or a
/// token provider called before every request. Every route but the upload takes the tenant
/// key (`ohs_sk_`), which belongs on your server only. An app on a phone holds a user token
/// (`ohs_ut_`) with the `upload` scope, minted by your server, and calls only
/// `uploadAppleHealthPart`, `uploadParts` and `uploadBluetoothPart` with it.
public final class OHSClient: Sendable {
    public static let version = "0.1.0"
    public static let defaultUserAgent = "open-health-sync-swift/0.1.0"
    /// The most attempts an upload makes (the first try included).
    public static let maxUploadAttempts = 5

    /// Sleeps with `Task.sleep`.
    public static let defaultSleeper: Sleeper = { seconds in
        if seconds > 0 {
            try await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
        }
    }

    public let baseURL: URL
    let base: String
    let tokenProvider: TokenProvider
    let transport: HTTPTransport
    let userAgent: String
    let sleeper: Sleeper

    /// - Parameters:
    ///   - baseURL: the instance, for example `https://ohs.example.org`.
    ///   - tokenProvider: returns the bearer credential; called before every request.
    ///   - transport: sends the requests; `URLSessionTransport` by default.
    ///   - userAgent: the `User-Agent` header.
    ///   - sleeper: waits between upload retries.
    public init(
        baseURL: URL,
        tokenProvider: @escaping TokenProvider,
        transport: HTTPTransport = URLSessionTransport(),
        userAgent: String = OHSClient.defaultUserAgent,
        sleeper: @escaping Sleeper = OHSClient.defaultSleeper
    ) {
        self.baseURL = baseURL
        var b = baseURL.absoluteString
        while b.hasSuffix("/") {
            b.removeLast()
        }
        self.base = b
        self.tokenProvider = tokenProvider
        self.transport = transport
        self.userAgent = userAgent
        self.sleeper = sleeper
    }

    /// A client with a fixed bearer credential.
    public convenience init(
        baseURL: URL,
        token: String,
        transport: HTTPTransport = URLSessionTransport(),
        userAgent: String = OHSClient.defaultUserAgent,
        sleeper: @escaping Sleeper = OHSClient.defaultSleeper
    ) {
        self.init(
            baseURL: baseURL,
            tokenProvider: { token },
            transport: transport,
            userAgent: userAgent,
            sleeper: sleeper)
    }

    // MARK: - Providers and users

    /// The connectors enabled on this instance.
    public func listProviders() async throws -> [Provider] {
        let data = try await send(Spec(method: "GET", path: "/v1/providers"))
        return try decode(DataEnvelope<Provider>.self, from: data).data
    }

    /// Creates an end user. `idempotencyKey` makes a retry return the first answer.
    public func createUser(externalRef: String? = nil, idempotencyKey: String? = nil) async throws -> User {
        var body: JSONObject = [:]
        if let externalRef = externalRef {
            body["external_ref"] = .string(externalRef)
        }
        var spec = Spec(method: "POST", path: "/v1/users")
        spec.json(body)
        if let key = idempotencyKey {
            spec.headers.append(("Idempotency-Key", key))
        }
        return try decode(User.self, from: try await send(spec))
    }

    /// One page of the tenant's users, oldest first.
    public func listUsers(cursor: String? = nil, limit: Int? = nil) async throws -> Page<User> {
        var spec = Spec(method: "GET", path: "/v1/users")
        spec.query.add("cursor", cursor)
        spec.query.add("limit", limit)
        return try decode(Page<User>.self, from: try await send(spec))
    }

    public func getUser(userId: String) async throws -> User {
        let spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId))
        return try decode(User.self, from: try await send(spec))
    }

    /// Starts deleting a user and returns the deletion job id.
    public func deleteUser(userId: String) async throws -> String {
        let spec = Spec(method: "DELETE", path: "/v1/users/" + pathSegment(userId))
        return try decode(JobIdEnvelope.self, from: try await send(spec)).jobId
    }

    public func getDeletion(jobId: String) async throws -> DeletionJob {
        let spec = Spec(method: "GET", path: "/v1/deletions/" + pathSegment(jobId))
        return try decode(DeletionJob.self, from: try await send(spec))
    }

    /// Mints a user token. `ttlSeconds` nil or 0 means one hour.
    public func mintUserToken(userId: String, scopes: [TokenScope], ttlSeconds: Int? = nil) async throws -> UserToken {
        guard !scopes.isEmpty else {
            throw OHSError.invalidArgument("scopes must not be empty")
        }
        var body: JSONObject = ["scopes": .array(scopes.map { JSONValue.string($0.rawValue) })]
        if let ttl = ttlSeconds {
            body["ttl_seconds"] = .number(Double(ttl))
        }
        var spec = Spec(method: "POST", path: "/v1/users/" + pathSegment(userId) + "/tokens")
        spec.json(body)
        return try decode(UserToken.self, from: try await send(spec))
    }

    // MARK: - Connections

    public func listConnections(userId: String) async throws -> [Connection] {
        let spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/connections")
        return try decode(DataEnvelope<Connection>.self, from: try await send(spec)).data
    }

    public func getFreshness(userId: String) async throws -> Freshness {
        let spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/freshness")
        return try decode(Freshness.self, from: try await send(spec))
    }

    /// Starts connecting a provider and returns the consent URL to open for the user.
    public func startConnection(userId: String, provider: String, redirectUri: String? = nil) async throws -> String {
        var body: JSONObject = [:]
        if let redirectUri = redirectUri {
            body["redirect_uri"] = .string(redirectUri)
        }
        var spec = Spec(
            method: "POST",
            path: "/v1/users/" + pathSegment(userId) + "/connections/" + pathSegment(provider))
        spec.json(body)
        return try decode(AuthorizeEnvelope.self, from: try await send(spec)).authorizeUrl
    }

    public func deleteConnection(userId: String, provider: String) async throws {
        let spec = Spec(
            method: "DELETE",
            path: "/v1/users/" + pathSegment(userId) + "/connections/" + pathSegment(provider))
        _ = try await send(spec)
    }

    // MARK: - Reads

    /// Quantity samples of one canonical type (`heart_rate`, `step_count`...).
    public func listSamples(
        userId: String, type: String, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<Sample> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/samples")
        spec.query.add("type", type)
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<Sample>.self, from: try await send(spec))
    }

    public func listCategorySamples(
        userId: String, type: String? = nil, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<CategorySample> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/category-samples")
        spec.query.add("type", type)
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<CategorySample>.self, from: try await send(spec))
    }

    public func listSleep(
        userId: String, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<SleepSession> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/sleep")
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<SleepSession>.self, from: try await send(spec))
    }

    public func listSleepPhases(
        userId: String, sessionId: Int64, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<SleepPhase> {
        var spec = Spec(
            method: "GET",
            path: "/v1/users/" + pathSegment(userId) + "/sleep/" + String(sessionId) + "/phases")
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<SleepPhase>.self, from: try await send(spec))
    }

    /// Workouts; `type` is a canonical activity type (`running`, `cycling`...).
    public func listWorkouts(
        userId: String, type: String? = nil, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<Workout> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/workouts")
        spec.query.add("type", type)
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<Workout>.self, from: try await send(spec))
    }

    public func getWorkoutRoute(
        userId: String, workoutId: Int64, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<RoutePoint> {
        var spec = Spec(
            method: "GET",
            path: "/v1/users/" + pathSegment(userId) + "/workouts/" + String(workoutId) + "/route")
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<RoutePoint>.self, from: try await send(spec))
    }

    public func listNutrition(
        userId: String, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<NutritionLog> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/nutrition")
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<NutritionLog>.self, from: try await send(spec))
    }

    /// Daily summaries; `start` and `end` compare as dates.
    public func listDaily(
        userId: String, start: OHSTime? = nil, end: OHSTime? = nil,
        cursor: String? = nil, limit: Int? = nil
    ) async throws -> Page<DailySummary> {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/daily")
        spec.query.range(start, end, cursor, limit)
        return try decode(Page<DailySummary>.self, from: try await send(spec))
    }

    /// Record counts per provider and day, and the jobs still processing.
    public func getCoverage(
        userId: String, provider: String? = nil, start: OHSTime? = nil, end: OHSTime? = nil
    ) async throws -> Coverage {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/coverage")
        spec.query.add("provider", provider)
        spec.query.add("start", start?.value)
        spec.query.add("end", end?.value)
        return try decode(Coverage.self, from: try await send(spec))
    }

    /// Polls `getCoverage` until `pendingJobs` is 0 and returns that coverage.
    /// Throws `OHSError.timeout` when `timeout` seconds pass first.
    public func waitUntilProcessed(
        userId: String, timeout: Double = 120, pollInterval: Double = 1
    ) async throws -> Coverage {
        let deadline = Date().addingTimeInterval(timeout)
        while true {
            let coverage = try await getCoverage(userId: userId)
            if coverage.pendingJobs == 0 {
                return coverage
            }
            if Date() >= deadline {
                throw OHSError.timeout("jobs still pending after \(Int(timeout)) s")
            }
            try await sleeper(pollInterval)
        }
    }

    // MARK: - FHIR

    /// One page of the user's records as FHIR R4 Observations.
    public func searchFHIRObservations(
        userId: String, type: String? = nil, code: String? = nil, category: String? = nil,
        start: OHSTime? = nil, end: OHSTime? = nil, cursor: String? = nil, limit: Int? = nil
    ) async throws -> FHIRBundle {
        var spec = Spec(method: "GET", path: "/v1/users/" + pathSegment(userId) + "/fhir/Observation")
        spec.accept = "application/fhir+json"
        spec.query.add("type", type)
        spec.query.add("code", code)
        spec.query.add("category", category)
        spec.query.range(start, end, cursor, limit)
        let value = try decode(JSONValue.self, from: try await send(spec))
        guard let raw = value.objectValue else {
            throw OHSError.invalidResponse("the FHIR Bundle is not a JSON object")
        }
        return FHIRBundle(raw: raw)
    }

    /// One FHIR R4 Observation as raw JSON.
    public func readFHIRObservation(userId: String, observationId: String) async throws -> JSONObject {
        var spec = Spec(
            method: "GET",
            path: "/v1/users/" + pathSegment(userId) + "/fhir/Observation/" + pathSegment(observationId))
        spec.accept = "application/fhir+json"
        let value = try decode(JSONValue.self, from: try await send(spec))
        guard let raw = value.objectValue else {
            throw OHSError.invalidResponse("the FHIR Observation is not a JSON object")
        }
        return raw
    }

    // MARK: - Apple Health upload

    /// Uploads one part with a user token holding the `upload` scope. 429, 5xx answers and
    /// network errors are retried with exponential backoff (1 s, doubling, at most 30 s, or
    /// `Retry-After` when sent), up to `maxUploadAttempts` attempts; another 4xx throws at once.
    public func uploadAppleHealthPart(_ part: AppleHealthPart) async throws -> UploadResult {
        guard AppleHealthPartBuilder.isValidToken(part.type) else {
            throw OHSError.invalidArgument("part type must be 1 to 128 letters, digits, '_' or '-'")
        }
        guard AppleHealthPartBuilder.isValidToken(part.batch) else {
            throw OHSError.invalidArgument("part batch must be 1 to 128 letters, digits, '_' or '-'")
        }
        guard RFC3339.isValidDay(part.day) else {
            throw OHSError.invalidArgument("part day must be a YYYY-MM-DD date")
        }
        guard part.partNumber >= 0 && part.partNumber <= 1_000_000 else {
            throw OHSError.invalidArgument("part number must be between 0 and 1000000")
        }
        guard !part.body.isEmpty else {
            throw OHSError.invalidArgument("part body is empty")
        }
        var spec = Spec(method: "POST", path: "/v1/ingest/apple-health")
        spec.headers.append(("X-OHS-Type", part.type))
        spec.headers.append(("X-OHS-Day", part.day))
        spec.headers.append(("X-OHS-Batch", part.batch))
        spec.headers.append(("X-OHS-Part", String(part.partNumber)))
        spec.contentType = "application/gzip"
        spec.body = part.body
        spec.retry = true
        return try decode(UploadResult.self, from: try await send(spec))
    }

    // MARK: - Bluetooth upload

    /// Uploads one part of Bluetooth device values (`BluetoothRecorder`) with a user token
    /// holding the `upload` scope, with the retries of `uploadAppleHealthPart`. Send the
    /// same part again until it is stored: a retry is answered `duplicate: true`.
    public func uploadBluetoothPart(_ part: BluetoothPart) async throws -> UploadResult {
        guard AppleHealthPartBuilder.isValidToken(part.batch) else {
            throw OHSError.invalidArgument("part batch must be 1 to 128 letters, digits, '_' or '-'")
        }
        guard RFC3339.isValidDay(part.day) else {
            throw OHSError.invalidArgument("part day must be a YYYY-MM-DD date")
        }
        guard part.partNumber >= 0 && part.partNumber <= 1_000_000 else {
            throw OHSError.invalidArgument("part number must be between 0 and 1000000")
        }
        guard !part.body.isEmpty else {
            throw OHSError.invalidArgument("part body is empty")
        }
        var spec = Spec(method: "POST", path: "/v1/ingest/bluetooth")
        spec.headers.append(("X-OHS-Type", BluetoothPart.type))
        spec.headers.append(("X-OHS-Day", part.day))
        spec.headers.append(("X-OHS-Batch", part.batch))
        spec.headers.append(("X-OHS-Part", String(part.partNumber)))
        spec.contentType = "application/gzip"
        spec.body = part.body
        spec.retry = true
        return try decode(UploadResult.self, from: try await send(spec))
    }

    /// Uploads parts one after the other and returns their results in order.
    public func uploadParts(_ parts: [AppleHealthPart]) async throws -> [UploadResult] {
        var results: [UploadResult] = []
        results.reserveCapacity(parts.count)
        for part in parts {
            results.append(try await uploadAppleHealthPart(part))
        }
        return results
    }

    // MARK: - Paging

    /// Walks every page of a list call, fetching the next page only when asked:
    /// `for try await page in OHSClient.pages({ cursor in try await client.listSleep(userId: id, cursor: cursor) })`.
    public static func pages<T: Codable & Sendable>(
        _ fetch: @escaping @Sendable (_ cursor: String?) async throws -> Page<T>
    ) -> AsyncThrowingStream<Page<T>, Error> {
        let walker = PageWalker<T>(fetch: fetch)
        return AsyncThrowingStream<Page<T>, Error>(unfolding: {
            try await walker.nextPage()
        })
    }

    /// Like `pages`, one record at a time.
    public static func items<T: Codable & Sendable>(
        _ fetch: @escaping @Sendable (_ cursor: String?) async throws -> Page<T>
    ) -> AsyncThrowingStream<T, Error> {
        let walker = PageWalker<T>(fetch: fetch)
        return AsyncThrowingStream<T, Error>(unfolding: {
            try await walker.nextItem()
        })
    }

    /// Collects every record of a list call into one array.
    public static func allItems<T: Codable & Sendable>(
        _ fetch: @escaping @Sendable (_ cursor: String?) async throws -> Page<T>
    ) async throws -> [T] {
        var all: [T] = []
        for try await page in pages(fetch) {
            all.append(contentsOf: page.data)
        }
        return all
    }

    // MARK: - Requests

    struct Spec {
        var method: String
        var path: String
        var query = QueryItems()
        var headers: [(String, String)] = []
        var body: Data?
        var contentType: String?
        var accept = "application/json"
        var retry = false

        init(method: String, path: String) {
            self.method = method
            self.path = path
        }

        mutating func json(_ object: JSONObject) {
            body = JSONValue.object(object).compactJSONData()
            contentType = "application/json"
        }
    }

    struct QueryItems {
        var items: [(String, String)] = []

        mutating func add(_ name: String, _ value: String?) {
            if let value = value {
                items.append((name, value))
            }
        }

        mutating func add(_ name: String, _ value: Int?) {
            if let value = value {
                items.append((name, String(value)))
            }
        }

        mutating func range(_ start: OHSTime?, _ end: OHSTime?, _ cursor: String?, _ limit: Int?) {
            add("start", start?.value)
            add("end", end?.value)
            add("cursor", cursor)
            add("limit", limit)
        }

        var encoded: String {
            if items.isEmpty {
                return ""
            }
            return "?" + items.map { OHSClient.percentEncode($0.0) + "=" + OHSClient.percentEncode($0.1) }
                .joined(separator: "&")
        }
    }

    func makeRequest(_ spec: Spec, token: String) throws -> URLRequest {
        guard let url = URL(string: base + spec.path + spec.query.encoded) else {
            throw OHSError.invalidArgument("could not build the request URL")
        }
        var request = URLRequest(url: url)
        request.httpMethod = spec.method
        request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        request.setValue(spec.accept, forHTTPHeaderField: "Accept")
        request.setValue(userAgent, forHTTPHeaderField: "User-Agent")
        if let contentType = spec.contentType {
            request.setValue(contentType, forHTTPHeaderField: "Content-Type")
        }
        for (name, value) in spec.headers {
            request.setValue(value, forHTTPHeaderField: name)
        }
        request.httpBody = spec.body
        return request
    }

    /// Sends the request (with retries when `spec.retry`) and returns the body of a 2xx
    /// answer; any other status throws `OHSAPIError`.
    func send(_ spec: Spec) async throws -> Data {
        var attempt = 0
        while true {
            attempt += 1
            let token = try await tokenProvider()
            let request = try makeRequest(spec, token: token)
            var delay: Double = 0
            do {
                let (data, response) = try await transport.send(request)
                if (200..<300).contains(response.statusCode) {
                    return data
                }
                let error = OHSClient.apiError(data: data, response: response)
                let retryable = error.status == 429 || error.status >= 500
                guard spec.retry, retryable, attempt < OHSClient.maxUploadAttempts else {
                    throw error
                }
                if let seconds = error.retryAfterSeconds, seconds >= 0 {
                    delay = Double(seconds)
                } else {
                    delay = OHSClient.backoff(attempt: attempt)
                }
            } catch let error as OHSAPIError {
                throw error
            } catch let error as CancellationError {
                throw error
            } catch {
                if let urlError = error as? URLError, urlError.code == .cancelled {
                    throw error
                }
                guard spec.retry, attempt < OHSClient.maxUploadAttempts else {
                    throw error
                }
                delay = OHSClient.backoff(attempt: attempt)
            }
            try await sleeper(delay)
        }
    }

    /// 1 s after the first attempt, then doubling, at most 30 s.
    static func backoff(attempt: Int) -> Double {
        let exponent = max(0, min(attempt - 1, 10))
        return min(30, Double(1 << exponent))
    }

    static func apiError(data: Data, response: HTTPURLResponse) -> OHSAPIError {
        var title: String?
        var detail: String?
        var type: String?
        var requestId: String?
        if !data.isEmpty, let value = try? JSONValue.parse(data), let problem = value.objectValue {
            title = problem["title"]?.stringValue
            detail = problem["detail"]?.stringValue
            type = problem["type"]?.stringValue
            requestId = problem["request_id"]?.stringValue
        }
        if requestId == nil || requestId == "" {
            requestId = header(response, "X-Request-Id")
        }
        let retryAfter = header(response, "Retry-After")
            .flatMap { Int($0.trimmingCharacters(in: .whitespaces)) }
        return OHSAPIError(
            status: response.statusCode, title: title, detail: detail, type: type,
            requestId: requestId, retryAfterSeconds: retryAfter)
    }

    static func header(_ response: HTTPURLResponse, _ name: String) -> String? {
        let wanted = name.lowercased()
        for (key, value) in response.allHeaderFields {
            if let k = key as? String, k.lowercased() == wanted {
                if let s = value as? String {
                    return s
                }
                return "\(value)"
            }
        }
        return nil
    }

    func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        do {
            return try OHSClient.makeDecoder().decode(T.self, from: data)
        } catch {
            throw OHSError.invalidResponse("could not decode \(T.self)" + OHSClient.describe(error))
        }
    }

    /// A JSON decoder that reads RFC 3339 times with up to nine fractional digits.
    public static func makeDecoder() -> JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let text = try container.decode(String.self)
            guard let date = RFC3339.parse(text) else {
                throw DecodingError.dataCorruptedError(
                    in: container, debugDescription: "not an RFC 3339 time")
            }
            return date
        }
        return decoder
    }

    /// Names the field that failed, never its value.
    static func describe(_ error: Error) -> String {
        func path(_ codingPath: [CodingKey]) -> String {
            var parts: [String] = []
            for key in codingPath {
                if let i = key.intValue {
                    parts.append("[\(i)]")
                } else {
                    parts.append(key.stringValue)
                }
            }
            let p = parts.joined(separator: ".")
            return p.isEmpty ? "" : " at \(p)"
        }
        guard let decodingError = error as? DecodingError else {
            return ""
        }
        switch decodingError {
        case .typeMismatch(_, let context):
            return ": wrong type" + path(context.codingPath)
        case .valueNotFound(_, let context):
            return ": missing value" + path(context.codingPath)
        case .keyNotFound(let key, let context):
            return ": missing field \(key.stringValue)" + path(context.codingPath)
        case .dataCorrupted(let context):
            return ": invalid data" + path(context.codingPath)
        @unknown default:
            return ""
        }
    }

    /// Percent-encodes everything but the RFC 3986 unreserved characters, so `+` in a time
    /// offset and `/` in an id reach the server as they are.
    static func percentEncode(_ s: String) -> String {
        let hex: [Character] = ["0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "A", "B", "C", "D", "E", "F"]
        var out = ""
        for byte in s.utf8 {
            switch byte {
            case 0x30...0x39, 0x41...0x5a, 0x61...0x7a, 0x2d, 0x2e, 0x5f, 0x7e:
                out.append(Character(Unicode.Scalar(byte)))
            default:
                out.append("%")
                out.append(hex[Int(byte >> 4)])
                out.append(hex[Int(byte & 0x0f)])
            }
        }
        return out
    }

    func pathSegment(_ s: String) -> String {
        return OHSClient.percentEncode(s)
    }
}

// MARK: - Private envelopes and paging state

struct DataEnvelope<T: Decodable>: Decodable {
    let data: [T]
}

struct JobIdEnvelope: Decodable {
    let jobId: String

    enum CodingKeys: String, CodingKey {
        case jobId = "job_id"
    }
}

struct AuthorizeEnvelope: Decodable {
    let authorizeUrl: String

    enum CodingKeys: String, CodingKey {
        case authorizeUrl = "authorize_url"
    }
}

/// Holds the cursor between pages, so the stream fetches one page per request for more.
actor PageWalker<T: Codable & Sendable> {
    private let fetch: @Sendable (String?) async throws -> Page<T>
    private var cursor: String?
    private var done = false
    private var buffer: [T] = []
    private var index = 0

    init(fetch: @escaping @Sendable (String?) async throws -> Page<T>) {
        self.fetch = fetch
    }

    func nextPage() async throws -> Page<T>? {
        if done {
            return nil
        }
        let page = try await fetch(cursor)
        if let next = page.nextCursor, !next.isEmpty {
            cursor = next
        } else {
            done = true
        }
        return page
    }

    func nextItem() async throws -> T? {
        while index >= buffer.count {
            guard let page = try await nextPage() else {
                return nil
            }
            buffer = page.data
            index = 0
        }
        let item = buffer[index]
        index += 1
        return item
    }
}
