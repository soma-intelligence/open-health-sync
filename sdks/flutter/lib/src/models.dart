// SPDX-License-Identifier: MIT

/// Typed records of the Open Health Sync API, one class per schema of
/// `api/openapi.yaml`. Field names are the JSON names in lowerCamelCase.
library;

import 'json.dart';

/// One page of a list route: the records and the cursor of the next page.
class Page<T> {
  const Page({required this.data, required this.nextCursor});

  /// Decodes `{"data": [...], "next_cursor": ...}` with [itemFromJson].
  factory Page.fromJson(
    Map<String, Object?> json,
    T Function(Map<String, Object?>) itemFromJson,
  ) =>
      Page(
        data: objectList(json, 'data', itemFromJson),
        nextCursor: optString(json, 'next_cursor'),
      );

  final List<T> data;

  /// Null on the last page.
  final String? nextCursor;
}

/// Where a record came from. Every record type implements it.
abstract interface class Provenance {
  /// The integration the record came through (apple_health, withings...).
  String get provider;

  /// The vendor that produced the record, when it differs or is known.
  String? get source;
  String? get device;
  DateTime get ingestedAt;
}

/// A connector enabled on the instance.
class Provider {
  const Provider({required this.slug, required this.kinds});

  factory Provider.fromJson(Map<String, Object?> json) => Provider(
        slug: reqString(json, 'slug'),
        kinds: stringList(json, 'kinds'),
      );

  final String slug;

  /// `oauth`, `push` and/or `device`.
  final List<String> kinds;
}

/// An end user of the tenant.
class User {
  const User({
    required this.id,
    required this.externalRef,
    required this.status,
    required this.createdAt,
  });

  factory User.fromJson(Map<String, Object?> json) => User(
        id: reqString(json, 'id'),
        externalRef: optString(json, 'external_ref'),
        status: reqString(json, 'status'),
        createdAt: reqTime(json, 'created_at'),
      );

  final String id;
  final String? externalRef;

  /// `active` or `deleting`.
  final String status;
  final DateTime createdAt;
}

/// The background job deleting a user.
class DeletionJob {
  const DeletionJob({
    required this.jobId,
    required this.userId,
    required this.status,
    required this.createdAt,
    required this.finishedAt,
  });

  factory DeletionJob.fromJson(Map<String, Object?> json) => DeletionJob(
        jobId: reqString(json, 'job_id'),
        userId: reqString(json, 'user_id'),
        status: reqString(json, 'status'),
        createdAt: reqTime(json, 'created_at'),
        finishedAt: optTime(json, 'finished_at'),
      );

  final String jobId;
  final String userId;

  /// `pending`, `running`, `done` or `failed`.
  final String status;
  final DateTime createdAt;
  final DateTime? finishedAt;
}

/// A user token, shown only once. [toString] never prints [token].
class UserToken {
  const UserToken({
    required this.token,
    required this.expiresAt,
    required this.scopes,
  });

  factory UserToken.fromJson(Map<String, Object?> json) => UserToken(
        token: reqString(json, 'token'),
        expiresAt: reqTime(json, 'expires_at'),
        scopes: stringList(json, 'scopes'),
      );

  final String token;
  final DateTime expiresAt;
  final List<String> scopes;

  @override
  String toString() => 'UserToken(expiresAt: $expiresAt, scopes: $scopes)';
}

/// A user's connection to one provider.
class Connection {
  const Connection({
    required this.provider,
    required this.status,
    required this.scopes,
    required this.lastSyncAt,
    required this.initialBackfillAt,
    required this.createdAt,
    required this.updatedAt,
  });

  factory Connection.fromJson(Map<String, Object?> json) => Connection(
        provider: reqString(json, 'provider'),
        status: reqString(json, 'status'),
        scopes: stringList(json, 'scopes'),
        lastSyncAt: optTime(json, 'last_sync_at'),
        initialBackfillAt: optTime(json, 'initial_backfill_at'),
        createdAt: reqTime(json, 'created_at'),
        updatedAt: optTime(json, 'updated_at'),
      );

  final String provider;

  /// `active`, `needs_reauth` or `disconnected`.
  final String status;
  final List<String> scopes;
  final DateTime? lastSyncAt;
  final DateTime? initialBackfillAt;
  final DateTime createdAt;
  final DateTime? updatedAt;
}

/// How up to date one source of a user is.
class SourceFreshness {
  const SourceFreshness({
    required this.provider,
    required this.kind,
    required this.status,
    required this.state,
    required this.syncedThrough,
    required this.lagSeconds,
    required this.lastSyncAt,
    required this.lastEventAt,
    required this.pushHealthy,
    required this.pullEverySeconds,
    required this.nextPullAt,
    required this.lastAttemptAt,
    required this.lastError,
    required this.consecutiveFailures,
  });

  factory SourceFreshness.fromJson(Map<String, Object?> json) =>
      SourceFreshness(
        provider: reqString(json, 'provider'),
        kind: reqString(json, 'kind'),
        status: reqString(json, 'status'),
        state: reqString(json, 'state'),
        syncedThrough: optTime(json, 'synced_through'),
        lagSeconds: optInt(json, 'lag_seconds'),
        lastSyncAt: optTime(json, 'last_sync_at'),
        lastEventAt: optTime(json, 'last_event_at'),
        pushHealthy: optBool(json, 'push_healthy') ?? false,
        pullEverySeconds: optInt(json, 'pull_every_seconds') ?? 0,
        nextPullAt: optTime(json, 'next_pull_at'),
        lastAttemptAt: optTime(json, 'last_attempt_at'),
        lastError: optString(json, 'last_error'),
        consecutiveFailures: optInt(json, 'consecutive_failures') ?? 0,
      );

  final String provider;

  /// `oauth` or `device`.
  final String kind;

  /// `active`, `needs_reauth` or `disconnected`.
  final String status;

  /// `fresh`, `stale`, `failing`, `syncing`, `needs_reauth` or `disconnected`.
  final String state;
  final DateTime? syncedThrough;
  final int? lagSeconds;
  final DateTime? lastSyncAt;
  final DateTime? lastEventAt;
  final bool pushHealthy;

  /// Regular pull interval; 0 for a device source.
  final int pullEverySeconds;
  final DateTime? nextPullAt;
  final DateTime? lastAttemptAt;
  final String? lastError;
  final int consecutiveFailures;
}

/// The answer of `getFreshness`.
class Freshness {
  const Freshness({required this.asOf, required this.data});

  factory Freshness.fromJson(Map<String, Object?> json) => Freshness(
        asOf: reqTime(json, 'as_of'),
        data: objectList(json, 'data', SourceFreshness.fromJson),
      );

  final DateTime asOf;
  final List<SourceFreshness> data;
}

/// A quantity sample (heart rate, step count...).
class Sample implements Provenance {
  const Sample({
    required this.id,
    required this.type,
    required this.value,
    required this.unit,
    required this.startDate,
    required this.endDate,
    required this.sourceName,
    required this.sourceBundleId,
    required this.deviceModel,
    required this.productType,
    required this.externalId,
    required this.isPrimarySource,
    required this.wasUserEntered,
    required this.metadata,
    required this.provider,
    required this.source,
    required this.device,
    required this.ingestedAt,
  });

  factory Sample.fromJson(Map<String, Object?> json) => Sample(
        id: reqInt(json, 'id'),
        type: reqString(json, 'type'),
        value: reqDouble(json, 'value'),
        unit: optString(json, 'unit'),
        startDate: reqTime(json, 'start_date'),
        endDate: optTime(json, 'end_date'),
        sourceName: optString(json, 'source_name'),
        sourceBundleId: optString(json, 'source_bundle_id'),
        deviceModel: optString(json, 'device_model'),
        productType: optString(json, 'product_type'),
        externalId: optString(json, 'external_id'),
        isPrimarySource: optBool(json, 'is_primary_source'),
        wasUserEntered: optBool(json, 'was_user_entered'),
        metadata: jsonObject(json, 'metadata'),
        provider: reqString(json, 'provider'),
        source: optString(json, 'source'),
        device: optString(json, 'device'),
        ingestedAt: reqTime(json, 'ingested_at'),
      );

  final int id;
  final String type;
  final double value;
  final String? unit;
  final DateTime startDate;
  final DateTime? endDate;
  final String? sourceName;
  final String? sourceBundleId;
  final String? deviceModel;
  final String? productType;
  final String? externalId;
  final bool? isPrimarySource;
  final bool? wasUserEntered;
  final Map<String, Object?> metadata;
  @override
  final String provider;
  @override
  final String? source;
  @override
  final String? device;
  @override
  final DateTime ingestedAt;
}

/// A category sample (an event, such as a mindful session).
class CategorySample implements Provenance {
  const CategorySample({
    required this.id,
    required this.type,
    required this.valueInt,
    required this.valueLabel,
    required this.startDate,
    required this.endDate,
    required this.sourceName,
    required this.externalId,
    required this.metadata,
    required this.provider,
    required this.source,
    required this.device,
    required this.ingestedAt,
  });

  factory CategorySample.fromJson(Map<String, Object?> json) => CategorySample(
        id: reqInt(json, 'id'),
        type: reqString(json, 'type'),
        valueInt: optInt(json, 'value_int'),
        valueLabel: optString(json, 'value_label'),
        startDate: reqTime(json, 'start_date'),
        endDate: optTime(json, 'end_date'),
        sourceName: optString(json, 'source_name'),
        externalId: optString(json, 'external_id'),
        metadata: jsonObject(json, 'metadata'),
        provider: reqString(json, 'provider'),
        source: optString(json, 'source'),
        device: optString(json, 'device'),
        ingestedAt: reqTime(json, 'ingested_at'),
      );

  final int id;
  final String type;
  final int? valueInt;
  final String? valueLabel;
  final DateTime startDate;
  final DateTime? endDate;
  final String? sourceName;
  final String? externalId;
  final Map<String, Object?> metadata;
  @override
  final String provider;
  @override
  final String? source;
  @override
  final String? device;
  @override
  final DateTime ingestedAt;
}

/// A night (or nap) of sleep.
class SleepSession implements Provenance {
  const SleepSession({
    required this.id,
    required this.bedtimeStart,
    required this.bedtimeEnd,
    required this.totalSleepSeconds,
    required this.deepSleepSeconds,
    required this.remSleepSeconds,
    required this.lightSleepSeconds,
    required this.awakeSeconds,
    required this.sleepScore,
    required this.hrvAvgMs,
    required this.restingHr,
    required this.respiratoryRate,
    required this.spo2Avg,
    required this.externalId,
    required this.metadata,
    required this.provider,
    required this.source,
    required this.device,
    required this.ingestedAt,
  });

  factory SleepSession.fromJson(Map<String, Object?> json) => SleepSession(
        id: reqInt(json, 'id'),
        bedtimeStart: reqTime(json, 'bedtime_start'),
        bedtimeEnd: optTime(json, 'bedtime_end'),
        totalSleepSeconds: optInt(json, 'total_sleep_seconds'),
        deepSleepSeconds: optInt(json, 'deep_sleep_seconds'),
        remSleepSeconds: optInt(json, 'rem_sleep_seconds'),
        lightSleepSeconds: optInt(json, 'light_sleep_seconds'),
        awakeSeconds: optInt(json, 'awake_seconds'),
        sleepScore: optDouble(json, 'sleep_score'),
        hrvAvgMs: optDouble(json, 'hrv_avg_ms'),
        restingHr: optDouble(json, 'resting_hr'),
        respiratoryRate: optDouble(json, 'respiratory_rate'),
        spo2Avg: optDouble(json, 'spo2_avg'),
        externalId: optString(json, 'external_id'),
        metadata: jsonObject(json, 'metadata'),
        provider: reqString(json, 'provider'),
        source: optString(json, 'source'),
        device: optString(json, 'device'),
        ingestedAt: reqTime(json, 'ingested_at'),
      );

  final int id;
  final DateTime bedtimeStart;
  final DateTime? bedtimeEnd;
  final int? totalSleepSeconds;
  final int? deepSleepSeconds;
  final int? remSleepSeconds;
  final int? lightSleepSeconds;
  final int? awakeSeconds;
  final double? sleepScore;
  final double? hrvAvgMs;
  final double? restingHr;
  final double? respiratoryRate;
  final double? spo2Avg;
  final String? externalId;
  final Map<String, Object?> metadata;
  @override
  final String provider;
  @override
  final String? source;
  @override
  final String? device;
  @override
  final DateTime ingestedAt;
}

/// One phase (deep, rem, light, awake) of a sleep session.
class SleepPhase implements Provenance {
  const SleepPhase({
    required this.id,
    required this.sessionId,
    required this.phase,
    required this.startDate,
    required this.endDate,
    required this.durationS,
    required this.provider,
    required this.source,
    required this.device,
    required this.ingestedAt,
  });

  factory SleepPhase.fromJson(Map<String, Object?> json) => SleepPhase(
        id: reqInt(json, 'id'),
        sessionId: reqInt(json, 'session_id'),
        phase: reqString(json, 'phase'),
        startDate: reqTime(json, 'start_date'),
        endDate: optTime(json, 'end_date'),
        durationS: reqInt(json, 'duration_s'),
        provider: reqString(json, 'provider'),
        source: optString(json, 'source'),
        device: optString(json, 'device'),
        ingestedAt: reqTime(json, 'ingested_at'),
      );

  final int id;
  final int sessionId;
  final String phase;
  final DateTime startDate;
  final DateTime? endDate;
  final int durationS;
  @override
  final String provider;
  @override
  final String? source;
  @override
  final String? device;
  @override
  final DateTime ingestedAt;
}

/// A workout.
class Workout implements Provenance {
  const Workout({
    required this.id,
    required this.activityType,
    required this.startDate,
    required this.endDate,
    required this.durationS,
    required this.activeEnergyKcal,
    required this.distanceM,
    required this.avgHeartRate,
    required this.maxHeartRate,
    required this.avgSpeedMps,
    required this.elevationGainM,
    required this.vo2Max,
    required this.externalId,
    required this.metadata,
    required this.provider,
    required this.source,
    required this.device,
    required this.ingestedAt,
  });

  factory Workout.fromJson(Map<String, Object?> json) => Workout(
        id: reqInt(json, 'id'),
        activityType: reqString(json, 'activity_type'),
        startDate: reqTime(json, 'start_date'),
        endDate: optTime(json, 'end_date'),
        durationS: optInt(json, 'duration_s'),
        activeEnergyKcal: optDouble(json, 'active_energy_kcal'),
        distanceM: optDouble(json, 'distance_m'),
        avgHeartRate: optDouble(json, 'avg_heart_rate'),
        maxHeartRate: optDouble(json, 'max_heart_rate'),
        avgSpeedMps: optDouble(json, 'avg_speed_mps'),
        elevationGainM: optDouble(json, 'elevation_gain_m'),
        vo2Max: optDouble(json, 'vo2_max'),
        externalId: optString(json, 'external_id'),
        metadata: jsonObject(json, 'metadata'),
        provider: reqString(json, 'provider'),
        source: optString(json, 'source'),
        device: optString(json, 'device'),
        ingestedAt: reqTime(json, 'ingested_at'),
      );

  final int id;
  final String activityType;
  final DateTime startDate;
  final DateTime? endDate;
  final int? durationS;
  final double? activeEnergyKcal;
  final double? distanceM;
  final double? avgHeartRate;
  final double? maxHeartRate;
  final double? avgSpeedMps;
  final double? elevationGainM;
  final double? vo2Max;
  final String? externalId;
  final Map<String, Object?> metadata;
  @override
  final String provider;
  @override
  final String? source;
  @override
  final String? device;
  @override
  final DateTime ingestedAt;
}

/// One GPS point of a workout route.
class RoutePoint implements Provenance {
  const RoutePoint({
    required this.id,
    required this.workoutId,
    required this.timestamp,
    required this.latitude,
    required this.longitude,
    required this.altitudeM,
    required this.speedMps,
    required this.courseDeg,
    required this.provider,
    required this.source,
    required this.device,
    required this.ingestedAt,
  });

  factory RoutePoint.fromJson(Map<String, Object?> json) => RoutePoint(
        id: reqInt(json, 'id'),
        workoutId: reqInt(json, 'workout_id'),
        timestamp: reqTime(json, 'timestamp'),
        latitude: reqDouble(json, 'latitude'),
        longitude: reqDouble(json, 'longitude'),
        altitudeM: optDouble(json, 'altitude_m'),
        speedMps: optDouble(json, 'speed_mps'),
        courseDeg: optDouble(json, 'course_deg'),
        provider: reqString(json, 'provider'),
        source: optString(json, 'source'),
        device: optString(json, 'device'),
        ingestedAt: reqTime(json, 'ingested_at'),
      );

  final int id;
  final int workoutId;
  final DateTime timestamp;
  final double latitude;
  final double longitude;
  final double? altitudeM;
  final double? speedMps;
  final double? courseDeg;
  @override
  final String provider;
  @override
  final String? source;
  @override
  final String? device;
  @override
  final DateTime ingestedAt;
}

/// A meal or food entry.
class NutritionLog implements Provenance {
  const NutritionLog({
    required this.id,
    required this.startDate,
    required this.endDate,
    required this.mealType,
    required this.energyKcal,
    required this.proteinG,
    required this.carbsG,
    required this.fatG,
    required this.fiberG,
    required this.sugarG,
    required this.sodiumMg,
    required this.nutrients,
    required this.externalId,
    required this.metadata,
    required this.provider,
    required this.source,
    required this.device,
    required this.ingestedAt,
  });

  factory NutritionLog.fromJson(Map<String, Object?> json) => NutritionLog(
        id: reqInt(json, 'id'),
        startDate: reqTime(json, 'start_date'),
        endDate: optTime(json, 'end_date'),
        mealType: optString(json, 'meal_type'),
        energyKcal: optDouble(json, 'energy_kcal'),
        proteinG: optDouble(json, 'protein_g'),
        carbsG: optDouble(json, 'carbs_g'),
        fatG: optDouble(json, 'fat_g'),
        fiberG: optDouble(json, 'fiber_g'),
        sugarG: optDouble(json, 'sugar_g'),
        sodiumMg: optDouble(json, 'sodium_mg'),
        nutrients: optJsonObject(json, 'nutrients'),
        externalId: optString(json, 'external_id'),
        metadata: jsonObject(json, 'metadata'),
        provider: reqString(json, 'provider'),
        source: optString(json, 'source'),
        device: optString(json, 'device'),
        ingestedAt: reqTime(json, 'ingested_at'),
      );

  final int id;
  final DateTime startDate;
  final DateTime? endDate;
  final String? mealType;
  final double? energyKcal;
  final double? proteinG;
  final double? carbsG;
  final double? fatG;
  final double? fiberG;
  final double? sugarG;
  final double? sodiumMg;

  /// Extended nutrients by name.
  final Map<String, Object?>? nutrients;
  final String? externalId;
  final Map<String, Object?> metadata;
  @override
  final String provider;
  @override
  final String? source;
  @override
  final String? device;
  @override
  final DateTime ingestedAt;
}

/// One provider's summary of one local calendar day.
class DailySummary implements Provenance {
  const DailySummary({
    required this.id,
    required this.date,
    required this.readinessScore,
    required this.activityScore,
    required this.sleepScore,
    required this.recoveryScore,
    required this.strainScore,
    required this.stepCount,
    required this.activeKcal,
    required this.totalKcal,
    required this.distanceM,
    required this.floorsClimbed,
    required this.restingHr,
    required this.hrvAvgMs,
    required this.metadata,
    required this.provider,
    required this.source,
    required this.device,
    required this.ingestedAt,
  });

  factory DailySummary.fromJson(Map<String, Object?> json) => DailySummary(
        id: reqInt(json, 'id'),
        date: reqString(json, 'date'),
        readinessScore: optDouble(json, 'readiness_score'),
        activityScore: optDouble(json, 'activity_score'),
        sleepScore: optDouble(json, 'sleep_score'),
        recoveryScore: optDouble(json, 'recovery_score'),
        strainScore: optDouble(json, 'strain_score'),
        stepCount: optInt(json, 'step_count'),
        activeKcal: optDouble(json, 'active_kcal'),
        totalKcal: optDouble(json, 'total_kcal'),
        distanceM: optDouble(json, 'distance_m'),
        floorsClimbed: optInt(json, 'floors_climbed'),
        restingHr: optDouble(json, 'resting_hr'),
        hrvAvgMs: optDouble(json, 'hrv_avg_ms'),
        metadata: jsonObject(json, 'metadata'),
        provider: reqString(json, 'provider'),
        source: optString(json, 'source'),
        device: optString(json, 'device'),
        ingestedAt: reqTime(json, 'ingested_at'),
      );

  final int id;

  /// The user's local calendar day, `YYYY-MM-DD`.
  final String date;
  final double? readinessScore;
  final double? activityScore;
  final double? sleepScore;
  final double? recoveryScore;
  final double? strainScore;
  final int? stepCount;
  final double? activeKcal;
  final double? totalKcal;
  final double? distanceM;
  final int? floorsClimbed;
  final double? restingHr;
  final double? hrvAvgMs;
  final Map<String, Object?> metadata;
  @override
  final String provider;
  @override
  final String? source;
  @override
  final String? device;
  @override
  final DateTime ingestedAt;
}

/// Record counts of one provider on one local day.
class CoverageDay {
  const CoverageDay({
    required this.provider,
    required this.date,
    required this.records,
    required this.metrics,
    required this.lastSyncedAt,
  });

  factory CoverageDay.fromJson(Map<String, Object?> json) {
    final metrics = jsonObject(json, 'metrics');
    return CoverageDay(
      provider: reqString(json, 'provider'),
      date: reqString(json, 'date'),
      records: reqInt(json, 'records'),
      metrics: Map.unmodifiable({
        for (final e in metrics.entries) e.key: (e.value as num).toInt(),
      }),
      lastSyncedAt: reqTime(json, 'last_synced_at'),
    );
  }

  final String provider;

  /// `YYYY-MM-DD`.
  final String date;

  /// Sum over [metrics].
  final int records;
  final Map<String, int> metrics;
  final DateTime lastSyncedAt;
}

/// What has landed, per provider and day, and what is still processing.
class Coverage {
  const Coverage({
    required this.pendingJobs,
    required this.parkedJobs,
    required this.rejectedRecords,
    required this.truncated,
    required this.days,
  });

  factory Coverage.fromJson(Map<String, Object?> json) => Coverage(
        pendingJobs: reqInt(json, 'pending_jobs'),
        parkedJobs: reqInt(json, 'parked_jobs'),
        rejectedRecords: reqInt(json, 'rejected_records'),
        truncated: optBool(json, 'truncated') ?? false,
        days: objectList(json, 'days', CoverageDay.fromJson),
      );

  /// ETL jobs of the user not finished yet. 0 once every upload is processed.
  final int pendingJobs;

  /// ETL jobs that exhausted their retries.
  final int parkedJobs;

  /// Lines the server could not store.
  final int rejectedRecords;

  /// True when more than 10000 days matched; [days] holds the earliest.
  final bool truncated;
  final List<CoverageDay> days;
}

/// The answer to one uploaded part.
class UploadResult {
  const UploadResult({required this.key, required this.duplicate});

  factory UploadResult.fromJson(Map<String, Object?> json) => UploadResult(
        key: reqString(json, 'key'),
        duplicate: reqBool(json, 'duplicate'),
      );

  /// The raw object key.
  final String key;

  /// True when the same bytes were already stored under this key.
  final bool duplicate;
}

/// One page of FHIR R4 Observations, a `searchset` Bundle.
///
/// [raw] is the Bundle as the server sent it; [observations] are the entry
/// resources and [nextCursor] the cursor of the `next` link, if any.
class FhirBundle {
  const FhirBundle({
    required this.raw,
    required this.observations,
    required this.nextCursor,
  });

  factory FhirBundle.fromJson(Map<String, Object?> json) {
    final raw = Map<String, Object?>.unmodifiable(json);
    final observations = <Map<String, Object?>>[];
    final entries = json['entry'];
    if (entries is List) {
      for (final e in entries) {
        final resource = asMap(e, 'a Bundle entry')['resource'];
        if (resource != null) {
          observations.add(Map.unmodifiable(asMap(resource, 'a resource')));
        }
      }
    }
    String? next;
    final links = json['link'];
    if (links is List) {
      for (final l in links) {
        final link = asMap(l, 'a Bundle link');
        if (link['relation'] == 'next' && link['url'] is String) {
          next = Uri.parse(link['url'] as String).queryParameters['cursor'];
        }
      }
    }
    return FhirBundle(
      raw: raw,
      observations: List.unmodifiable(observations),
      nextCursor: next,
    );
  }

  final Map<String, Object?> raw;
  final List<Map<String, Object?>> observations;

  /// Null on the last page.
  final String? nextCursor;

  /// The observations and cursor as a [Page], for `paginate`.
  Page<Map<String, Object?>> get page =>
      Page(data: observations, nextCursor: nextCursor);
}
