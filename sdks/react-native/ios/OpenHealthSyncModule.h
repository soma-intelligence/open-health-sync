// SPDX-License-Identifier: AGPL-3.0-only

#import <OpenHealthSyncSpec/OpenHealthSyncSpec.h>

/// The Turbo Native Module `OpenHealthSync` on iOS. The work is in OHSHealthBridge.swift,
/// which drives the Swift SDK's HealthKitSync; this class is the Objective-C++ face React
/// Native's codegen asks for.
@interface OpenHealthSyncModule : NativeOpenHealthSyncSpecBase <NativeOpenHealthSyncSpec>
@end
