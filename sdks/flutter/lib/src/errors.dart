// SPDX-License-Identifier: AGPL-3.0-only

/// A non-2xx answer of the API.
///
/// Carries the RFC 9457 problem fields when the body has them. The message
/// never holds a token or a request body.
class OhsApiException implements Exception {
  const OhsApiException({
    required this.status,
    this.title,
    this.detail,
    this.type,
    this.requestId,
    this.retryAfterSeconds,
  });

  /// The HTTP status code.
  final int status;
  final String? title;
  final String? detail;

  /// The problem `type`, often `about:blank`.
  final String? type;

  /// The `request_id` of the problem body, else the `X-Request-Id` header.
  /// Quote it when reporting the error.
  final String? requestId;

  /// The `Retry-After` header in seconds, when the server sent one.
  final int? retryAfterSeconds;

  @override
  String toString() {
    final b = StringBuffer('OhsApiException($status');
    if (title != null) b.write(' $title');
    b.write(')');
    if (detail != null) b.write(': $detail');
    if (requestId != null) b.write(' [request_id $requestId]');
    return b.toString();
  }
}
