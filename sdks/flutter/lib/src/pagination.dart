// SPDX-License-Identifier: AGPL-3.0-only

import 'models.dart';

/// Walks every page of a list call and emits each record.
///
/// [fetchPage] is called with `null` first, then with the `nextCursor` of the
/// previous page, until a page has no `nextCursor`. Pages are fetched one at
/// a time, as the stream is listened to.
///
/// ```dart
/// await for (final s in paginate((c) =>
///     client.listSamples(userId, type: 'heart_rate', cursor: c))) {
///   print(s.startDate);
/// }
/// ```
Stream<T> paginate<T>(
  Future<Page<T>> Function(String? cursor) fetchPage,
) async* {
  String? cursor;
  do {
    final page = await fetchPage(cursor);
    yield* Stream.fromIterable(page.data);
    cursor = page.nextCursor;
  } while (cursor != null);
}
