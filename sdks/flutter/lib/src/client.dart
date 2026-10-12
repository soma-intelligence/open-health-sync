// SPDX-License-Identifier: MIT

import 'dart:async';
import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';

import 'package:http/http.dart' as http;

import 'errors.dart';
import 'json.dart';
import 'models.dart';
import 'part_builder.dart';

/// The default `User-Agent` header.
const String defaultUserAgent = 'open-health-sync-flutter/0.1.0';

/// Most attempts for one upload part, the first one included.
const int maxUploadAttempts = 5;

/// A client of the Open Health Sync REST API.
///
/// Give it either a fixed [token] or a [tokenProvider], called before every
/// request, so an app can fetch a fresh user token from its own server when
/// the current one expires.
class OhsClient {
  OhsClient({
    required Object baseUrl,
    String? token,
    Future<String> Function()? tokenProvider,
    http.Client? httpClient,
    String? userAgent = defaultUserAgent,
    Future<void> Function(Duration)? sleep,
  })  : _base = _normalizeBase(baseUrl),
        _token = token,
        _tokenProvider = tokenProvider,
        _http = httpClient ?? http.Client(),
        _ownsHttp = httpClient == null,
        _userAgent = userAgent,
        _sleep = sleep ?? _defaultSleep {
    if ((token == null) == (tokenProvider == null)) {
      throw ArgumentError('give exactly one of token and tokenProvider');
    }
  }

  final String _base;
  final String? _token;
  final Future<String> Function()? _tokenProvider;
  final http.Client _http;
  final bool _ownsHttp;
  final String? _userAgent;
  final Future<void> Function(Duration) _sleep;

  static Future<void> _defaultSleep(Duration d) => Future<void>.delayed(d);

  static String _normalizeBase(Object baseUrl) {
    final String s;
    if (baseUrl is Uri) {
      s = baseUrl.toString();
    } else if (baseUrl is String) {
      s = baseUrl;
    } else {
      throw ArgumentError.value(baseUrl, 'baseUrl', 'must be a Uri or String');
    }
    final uri = Uri.parse(s);
    if (!uri.hasScheme || uri.host.isEmpty) {
      throw ArgumentError.value(s, 'baseUrl', 'must be an absolute URL');
    }
    return s.endsWith('/') ? s.substring(0, s.length - 1) : s;
  }

  /// Closes the underlying HTTP client when this client created it.
  void close() {
    if (_ownsHttp) _http.close();
  }

  // ---- providers and users (tenant key) ----

  /// The connectors enabled on this instance.
  Future<List<Provider>> listProviders() async {
    final json = await _getJson('/v1/providers');
    return objectList(json, 'data', Provider.fromJson);
  }

  /// Creates an end user. [idempotencyKey] makes a retry safe.
  Future<User> createUser({String? externalRef, String? idempotencyKey}) async {
    final json = await _json(
      'POST',
      '/v1/users',
      body: {if (externalRef != null) 'external_ref': externalRef},
      headers: {if (idempotencyKey != null) 'Idempotency-Key': idempotencyKey},
    );
    return User.fromJson(json);
  }

  /// One page of the tenant's users, oldest first.
  Future<Page<User>> listUsers({String? cursor, int? limit}) async {
    final json = await _getJson(
      '/v1/users',
      query: _q({'cursor': cursor, 'limit': limit}),
    );
    return Page.fromJson(json, User.fromJson);
  }

  Future<User> getUser(String userId) async =>
      User.fromJson(await _getJson('/v1/users/${_seg(userId)}'));

  /// Starts deleting a user; returns the deletion job id.
  Future<String> deleteUser(String userId) async {
    final json = await _json('DELETE', '/v1/users/${_seg(userId)}');
    return reqString(json, 'job_id');
  }

  Future<DeletionJob> getDeletion(String jobId) async =>
      DeletionJob.fromJson(await _getJson('/v1/deletions/${_seg(jobId)}'));

  /// Mints a user token with [scopes] (`upload`, `connect`, `read`).
  Future<UserToken> mintUserToken(
    String userId,
    List<String> scopes, {
    int? ttlSeconds,
  }) async {
    final json = await _json(
      'POST',
      '/v1/users/${_seg(userId)}/tokens',
      body: {
        'scopes': scopes,
        if (ttlSeconds != null) 'ttl_seconds': ttlSeconds,
      },
    );
    return UserToken.fromJson(json);
  }

  // ---- connections ----

  Future<List<Connection>> listConnections(String userId) async {
    final json = await _getJson('/v1/users/${_seg(userId)}/connections');
    return objectList(json, 'data', Connection.fromJson);
  }

  Future<Freshness> getFreshness(String userId) async => Freshness.fromJson(
        await _getJson('/v1/users/${_seg(userId)}/freshness'),
      );

  /// Starts connecting [provider]; returns the consent URL to open.
  Future<Uri> startConnection(
    String userId,
    String provider, {
    String? redirectUri,
  }) async {
    final json = await _json(
      'POST',
      '/v1/users/${_seg(userId)}/connections/${_seg(provider)}',
      body: {if (redirectUri != null) 'redirect_uri': redirectUri},
    );
    return Uri.parse(reqString(json, 'authorize_url'));
  }

  Future<void> deleteConnection(String userId, String provider) async {
    await _send(
      'DELETE',
      '/v1/users/${_seg(userId)}/connections/${_seg(provider)}',
    );
  }

  // ---- read routes ----
  //
  // `start` and `end` take an RFC 3339 time or a YYYY-MM-DD date, as strings
  // passed as is; `formatTimestamp` turns a DateTime into one.

  /// Quantity samples of one canonical [type], such as `heart_rate`.
  Future<Page<Sample>> listSamples(
    String userId, {
    required String type,
    String? start,
    String? end,
    String? cursor,
    int? limit,
  }) =>
      _page('/v1/users/${_seg(userId)}/samples', Sample.fromJson, {
        'type': type,
        'start': start,
        'end': end,
        'cursor': cursor,
        'limit': limit,
      });

  Future<Page<CategorySample>> listCategorySamples(
    String userId, {
    String? type,
    String? start,
    String? end,
    String? cursor,
    int? limit,
  }) =>
      _page('/v1/users/${_seg(userId)}/category-samples',
          CategorySample.fromJson, {
        'type': type,
        'start': start,
        'end': end,
        'cursor': cursor,
        'limit': limit,
      });

  Future<Page<SleepSession>> listSleep(
    String userId, {
    String? start,
    String? end,
    String? cursor,
    int? limit,
  }) =>
      _page('/v1/users/${_seg(userId)}/sleep', SleepSession.fromJson, {
        'start': start,
        'end': end,
        'cursor': cursor,
        'limit': limit,
      });

  Future<Page<SleepPhase>> listSleepPhases(
    String userId,
    int sessionId, {
    String? start,
    String? end,
    String? cursor,
    int? limit,
  }) =>
      _page(
        '/v1/users/${_seg(userId)}/sleep/$sessionId/phases',
        SleepPhase.fromJson,
        {'start': start, 'end': end, 'cursor': cursor, 'limit': limit},
      );

  Future<Page<Workout>> listWorkouts(
    String userId, {
    String? type,
    String? start,
    String? end,
    String? cursor,
    int? limit,
  }) =>
      _page('/v1/users/${_seg(userId)}/workouts', Workout.fromJson, {
        'type': type,
        'start': start,
        'end': end,
        'cursor': cursor,
        'limit': limit,
      });

  Future<Page<RoutePoint>> getWorkoutRoute(
    String userId,
    int workoutId, {
    String? start,
    String? end,
    String? cursor,
    int? limit,
  }) =>
      _page(
        '/v1/users/${_seg(userId)}/workouts/$workoutId/route',
        RoutePoint.fromJson,
        {'start': start, 'end': end, 'cursor': cursor, 'limit': limit},
      );

  Future<Page<NutritionLog>> listNutrition(
    String userId, {
    String? start,
    String? end,
    String? cursor,
    int? limit,
  }) =>
      _page('/v1/users/${_seg(userId)}/nutrition', NutritionLog.fromJson, {
        'start': start,
        'end': end,
        'cursor': cursor,
        'limit': limit,
      });

  Future<Page<DailySummary>> listDaily(
    String userId, {
    String? start,
    String? end,
    String? cursor,
    int? limit,
  }) =>
      _page('/v1/users/${_seg(userId)}/daily', DailySummary.fromJson, {
        'start': start,
        'end': end,
        'cursor': cursor,
        'limit': limit,
      });

  /// Record counts per provider and day, and the jobs still processing.
  Future<Coverage> getCoverage(
    String userId, {
    String? provider,
    String? start,
    String? end,
  }) async {
    final json = await _getJson(
      '/v1/users/${_seg(userId)}/coverage',
      query: _q({'provider': provider, 'start': start, 'end': end}),
    );
    return Coverage.fromJson(json);
  }

  // ---- FHIR ----

  /// The user's records as FHIR R4 Observations, one Bundle page.
  Future<FhirBundle> searchFhirObservations(
    String userId, {
    String? type,
    String? code,
    String? category,
    String? start,
    String? end,
    String? cursor,
    int? limit,
  }) async {
    final json = await _getJson(
      '/v1/users/${_seg(userId)}/fhir/Observation',
      query: _q({
        'type': type,
        'code': code,
        'category': category,
        'start': start,
        'end': end,
        'cursor': cursor,
        'limit': limit,
      }),
      accept: _fhirAccept,
    );
    return FhirBundle.fromJson(json);
  }

  /// One FHIR R4 Observation, as the raw JSON object.
  Future<Map<String, Object?>> readFhirObservation(
    String userId,
    String observationId,
  ) =>
      _getJson(
        '/v1/users/${_seg(userId)}/fhir/Observation/${_seg(observationId)}',
        accept: _fhirAccept,
      );

  // ---- upload ----

  /// Uploads one part. Needs a user token with the `upload` scope.
  ///
  /// Retries on 429, 5xx and network errors with exponential backoff (1 s,
  /// doubling, at most 30 s), waiting `Retry-After` seconds instead when the
  /// server sends it, for at most [maxUploadAttempts] attempts. Any other
  /// non-2xx answer throws [OhsApiException] at once.
  Future<UploadResult> uploadAppleHealthPart(AppleHealthPart part) async {
    final headers = {
      'Content-Type': 'application/gzip',
      'X-OHS-Type': part.type,
      'X-OHS-Day': part.day,
      'X-OHS-Batch': part.batch,
      'X-OHS-Part': part.partNumber.toString(),
    };
    for (var attempt = 1;; attempt++) {
      http.Response response;
      try {
        response = await _rawSend(
          'POST',
          '/v1/ingest/apple-health',
          bodyBytes: part.body,
          headers: headers,
        );
      } on http.ClientException {
        if (attempt >= maxUploadAttempts) rethrow;
        await _sleep(_backoff(attempt));
        continue;
      } on TimeoutException {
        if (attempt >= maxUploadAttempts) rethrow;
        await _sleep(_backoff(attempt));
        continue;
      }
      final status = response.statusCode;
      if (status >= 200 && status < 300) {
        return UploadResult.fromJson(_decodeObject(response));
      }
      final error = _error(response);
      final retryable = status == 429 || status >= 500;
      if (!retryable || attempt >= maxUploadAttempts) throw error;
      final retryAfter = error.retryAfterSeconds;
      await _sleep(
        retryAfter != null ? Duration(seconds: retryAfter) : _backoff(attempt),
      );
    }
  }

  /// Uploads [parts] one after the other, in order.
  Future<List<UploadResult>> uploadParts(
    Iterable<AppleHealthPart> parts,
  ) async {
    final results = <UploadResult>[];
    for (final p in parts) {
      results.add(await uploadAppleHealthPart(p));
    }
    return results;
  }

  static Duration _backoff(int attempt) {
    final seconds = min(30, 1 << min(attempt - 1, 5));
    return Duration(seconds: seconds);
  }

  // ---- plumbing ----

  static const String _fhirAccept =
      'application/fhir+json, application/problem+json';

  static Map<String, String> _q(Map<String, Object?> params) => {
        for (final e in params.entries)
          if (e.value != null) e.key: e.value.toString(),
      };

  static String _seg(String s) => Uri.encodeComponent(s);

  Future<Page<T>> _page<T>(
    String path,
    T Function(Map<String, Object?>) fromJson,
    Map<String, Object?> query,
  ) async =>
      Page.fromJson(await _getJson(path, query: _q(query)), fromJson);

  Future<Map<String, Object?>> _getJson(
    String path, {
    Map<String, String> query = const {},
    String accept = 'application/json',
  }) =>
      _json('GET', path, query: query, accept: accept);

  Future<Map<String, Object?>> _json(
    String method,
    String path, {
    Map<String, String> query = const {},
    Map<String, Object?>? body,
    Map<String, String> headers = const {},
    String accept = 'application/json',
  }) async {
    final response = await _send(
      method,
      path,
      query: query,
      bodyBytes: body == null ? null : utf8.encode(jsonEncode(body)),
      headers: {
        if (body != null) 'Content-Type': 'application/json',
        ...headers,
      },
      accept: accept,
    );
    return _decodeObject(response);
  }

  Future<http.Response> _send(
    String method,
    String path, {
    Map<String, String> query = const {},
    Uint8List? bodyBytes,
    Map<String, String> headers = const {},
    String accept = 'application/json',
  }) async {
    final response = await _rawSend(
      method,
      path,
      query: query,
      bodyBytes: bodyBytes,
      headers: headers,
      accept: accept,
    );
    if (response.statusCode < 200 || response.statusCode >= 300) {
      throw _error(response);
    }
    return response;
  }

  Future<http.Response> _rawSend(
    String method,
    String path, {
    Map<String, String> query = const {},
    Uint8List? bodyBytes,
    Map<String, String> headers = const {},
    String accept = 'application/json',
  }) async {
    var uri = Uri.parse('$_base$path');
    if (query.isNotEmpty) uri = uri.replace(queryParameters: query);
    final token = _token ?? await _tokenProvider!();
    final request = http.Request(method, uri)
      ..headers['Authorization'] = 'Bearer $token'
      ..headers['Accept'] = accept;
    if (_userAgent != null) request.headers['User-Agent'] = _userAgent;
    request.headers.addAll(headers);
    if (bodyBytes != null) request.bodyBytes = bodyBytes;
    return http.Response.fromStream(await _http.send(request));
  }

  static Map<String, Object?> _decodeObject(http.Response response) {
    final Object? decoded;
    try {
      decoded = jsonDecode(utf8.decode(response.bodyBytes));
    } on FormatException {
      throw FormatException(
        'response of status ${response.statusCode} is not JSON',
      );
    }
    return asMap(decoded, 'the response body');
  }

  static OhsApiException _error(http.Response response) {
    Map<String, Object?>? problem;
    try {
      final decoded = jsonDecode(utf8.decode(response.bodyBytes));
      if (decoded is Map) problem = decoded.cast<String, Object?>();
    } on FormatException {
      problem = null;
    }
    String? str(String key) {
      final v = problem?[key];
      return v is String ? v : null;
    }

    final retryAfter = int.tryParse(
      (response.headers['retry-after'] ?? '').trim(),
    );
    return OhsApiException(
      status: response.statusCode,
      title: str('title'),
      detail: str('detail'),
      type: str('type'),
      requestId: str('request_id') ?? response.headers['x-request-id'],
      retryAfterSeconds:
          retryAfter != null && retryAfter >= 0 ? retryAfter : null,
    );
  }
}
