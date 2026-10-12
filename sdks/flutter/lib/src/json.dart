// SPDX-License-Identifier: MIT

/// Helpers that read typed values out of decoded JSON maps.
///
/// A missing or mistyped required field throws a [FormatException] naming the
/// field, never its value. Unknown fields are ignored.
library;

/// Parses an RFC 3339 timestamp into a UTC [DateTime].
///
/// Accepts `Z` or a numeric offset and any number of fractional digits. Dart
/// keeps microseconds at most, so digits after the sixth are dropped (the
/// server emits up to nine).
DateTime parseTimestamp(String value) {
  final match = _rfc3339.firstMatch(value);
  if (match == null) {
    throw FormatException('not an RFC 3339 timestamp');
  }
  var fraction = match.group(2) ?? '';
  if (fraction.length > 7) {
    // ".123456789" -> ".123456"
    fraction = fraction.substring(0, 7);
  }
  final normalized = '${match.group(1)}$fraction${match.group(3)}';
  return DateTime.parse(normalized).toUtc();
}

/// Formats [time] as an RFC 3339 timestamp in UTC, for `start` and `end`
/// query parameters and upload lines.
String formatTimestamp(DateTime time) => time.toUtc().toIso8601String();

final RegExp _rfc3339 = RegExp(
  r'^(\d{4}-\d{2}-\d{2}[Tt ]\d{2}:\d{2}:\d{2})(\.\d+)?([Zz]|[+-]\d{2}:?\d{2})$',
);

Map<String, Object?> asMap(Object? value, String what) {
  if (value is Map<String, Object?>) return value;
  if (value is Map) return value.cast<String, Object?>();
  throw FormatException('$what is not a JSON object');
}

Object? _required(Map<String, Object?> json, String key) {
  if (!json.containsKey(key) || json[key] == null) {
    throw FormatException('missing field "$key"');
  }
  return json[key];
}

String reqString(Map<String, Object?> json, String key) {
  final v = _required(json, key);
  if (v is String) return v;
  throw FormatException('field "$key" is not a string');
}

String? optString(Map<String, Object?> json, String key) {
  final v = json[key];
  if (v == null) return null;
  if (v is String) return v;
  throw FormatException('field "$key" is not a string');
}

int reqInt(Map<String, Object?> json, String key) {
  final v = _required(json, key);
  if (v is num) return v.toInt();
  throw FormatException('field "$key" is not a number');
}

int? optInt(Map<String, Object?> json, String key) {
  final v = json[key];
  if (v == null) return null;
  if (v is num) return v.toInt();
  throw FormatException('field "$key" is not a number');
}

double reqDouble(Map<String, Object?> json, String key) {
  final v = _required(json, key);
  if (v is num) return v.toDouble();
  throw FormatException('field "$key" is not a number');
}

double? optDouble(Map<String, Object?> json, String key) {
  final v = json[key];
  if (v == null) return null;
  if (v is num) return v.toDouble();
  throw FormatException('field "$key" is not a number');
}

bool reqBool(Map<String, Object?> json, String key) {
  final v = _required(json, key);
  if (v is bool) return v;
  throw FormatException('field "$key" is not a boolean');
}

bool? optBool(Map<String, Object?> json, String key) {
  final v = json[key];
  if (v == null) return null;
  if (v is bool) return v;
  throw FormatException('field "$key" is not a boolean');
}

DateTime reqTime(Map<String, Object?> json, String key) {
  try {
    return parseTimestamp(reqString(json, key));
  } on FormatException catch (e) {
    throw FormatException('field "$key": ${e.message}');
  }
}

DateTime? optTime(Map<String, Object?> json, String key) {
  final s = optString(json, key);
  if (s == null) return null;
  try {
    return parseTimestamp(s);
  } on FormatException catch (e) {
    throw FormatException('field "$key": ${e.message}');
  }
}

List<String> stringList(Map<String, Object?> json, String key) {
  final v = json[key];
  if (v == null) return const [];
  if (v is List) return List.unmodifiable(v.map((e) => e as String));
  throw FormatException('field "$key" is not an array');
}

/// A free-form JSON object; absent or null becomes an empty map.
Map<String, Object?> jsonObject(Map<String, Object?> json, String key) {
  final v = json[key];
  if (v == null) return const {};
  return Map.unmodifiable(asMap(v, 'field "$key"'));
}

Map<String, Object?>? optJsonObject(Map<String, Object?> json, String key) {
  final v = json[key];
  if (v == null) return null;
  return Map.unmodifiable(asMap(v, 'field "$key"'));
}

List<T> objectList<T>(
  Map<String, Object?> json,
  String key,
  T Function(Map<String, Object?>) fromJson,
) {
  final v = json[key];
  if (v == null) return const [];
  if (v is! List) throw FormatException('field "$key" is not an array');
  return List.unmodifiable(
    v.map((e) => fromJson(asMap(e, 'an item of "$key"'))),
  );
}
