// SPDX-License-Identifier: AGPL-3.0-only

/// The names shared with the native sides
/// (`OpenHealthSyncHealthPlugin.kt` and `OpenHealthSyncHealthPlugin.swift`).
library;

/// The method channel: one call per method of `OpenHealthSyncHealth`.
const String methodChannelName = 'org.openhealthsync/health';

/// The event channel: one event per sync that ran in the background.
const String eventChannelName = 'org.openhealthsync/health/background';
