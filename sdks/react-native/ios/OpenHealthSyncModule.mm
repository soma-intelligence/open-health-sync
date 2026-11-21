// SPDX-License-Identifier: AGPL-3.0-only

#import "OpenHealthSyncModule.h"

#import <UIKit/UIKit.h>

#if __has_include("OpenHealthSyncReactNative-Swift.h")
#import "OpenHealthSyncReactNative-Swift.h"
#else
#import <OpenHealthSyncReactNative/OpenHealthSyncReactNative-Swift.h>
#endif

@implementation OpenHealthSyncModule

// HealthKit wakes the app in the background to deliver new samples, and only delivers to
// observer queries that are running again by the end of the launch. JavaScript loads this
// module too late for that, so background sync is resumed here, with no code in the app's
// AppDelegate.
+ (void)load
{
  [[NSNotificationCenter defaultCenter] addObserverForName:UIApplicationDidFinishLaunchingNotification
                                                    object:nil
                                                     queue:nil
                                                usingBlock:^(__unused NSNotification *note) {
                                                  [[OHSHealthBridge shared] resumeBackgroundSync];
                                                }];
}

+ (NSString *)moduleName
{
  return @"OpenHealthSync";
}

- (instancetype)init
{
  if (self = [super init]) {
    __weak OpenHealthSyncModule *weakSelf = self;
    [[OHSHealthBridge shared] setBackgroundSyncHandler:^(NSString *json) {
      [weakSelf emitOnBackgroundSync:@{@"json" : json}];
    }];
  }
  return self;
}

- (void)invalidate
{
  [[OHSHealthBridge shared] setBackgroundSyncHandler:nil];
}

- (std::shared_ptr<facebook::react::TurboModule>)getTurboModule:(const facebook::react::ObjCTurboModule::InitParams &)params
{
  return std::make_shared<facebook::react::NativeOpenHealthSyncSpecJSI>(params);
}

/// Settles a promise with what the Swift side answered: a value, or an error code and message.
static void (^settle(RCTPromiseResolveBlock resolve, RCTPromiseRejectBlock reject))(NSString *, NSString *, NSString *)
{
  return ^(NSString *value, NSString *errorCode, NSString *errorMessage) {
    if (errorCode != nil) {
      reject(errorCode, errorMessage, nil);
    } else {
      resolve(value);
    }
  };
}

- (void)availability:(RCTPromiseResolveBlock)resolve reject:(RCTPromiseRejectBlock)reject
{
  resolve([[OHSHealthBridge shared] availability]);
}

- (void)configure:(NSString *)baseUrl
        userToken:(NSString *)userToken
          resolve:(RCTPromiseResolveBlock)resolve
           reject:(RCTPromiseRejectBlock)reject
{
  [[OHSHealthBridge shared] configureWithBaseUrl:baseUrl userToken:userToken completion:settle(resolve, reject)];
}

- (void)clearCredentials:(RCTPromiseResolveBlock)resolve reject:(RCTPromiseRejectBlock)reject
{
  [[OHSHealthBridge shared] clearCredentials];
  resolve(nil);
}

- (void)isConfigured:(RCTPromiseResolveBlock)resolve reject:(RCTPromiseRejectBlock)reject
{
  resolve(@([[OHSHealthBridge shared] isConfigured]));
}

- (void)requestAuthorization:(NSArray *)types
                  background:(BOOL)background
                     history:(BOOL)history
                     resolve:(RCTPromiseResolveBlock)resolve
                      reject:(RCTPromiseRejectBlock)reject
{
  // `background` and `history` are Health Connect permissions; HealthKit has no such grants.
  [[OHSHealthBridge shared] requestAuthorizationWithTypes:types completion:settle(resolve, reject)];
}

- (void)sync:(NSArray *)types resolve:(RCTPromiseResolveBlock)resolve reject:(RCTPromiseRejectBlock)reject
{
  [[OHSHealthBridge shared] syncWithTypes:types completion:settle(resolve, reject)];
}

- (void)flush:(BOOL)force resolve:(RCTPromiseResolveBlock)resolve reject:(RCTPromiseRejectBlock)reject
{
  // `force` skips the retry wait of the Android queue; iOS has neither.
  [[OHSHealthBridge shared] flushWithCompletion:settle(resolve, reject)];
}

- (void)enableBackgroundSync:(NSArray *)types
             intervalMinutes:(double)intervalMinutes
     requireUnmeteredNetwork:(BOOL)requireUnmeteredNetwork
                   frequency:(NSString *)frequency
                     resolve:(RCTPromiseResolveBlock)resolve
                      reject:(RCTPromiseRejectBlock)reject
{
  // The interval and the network constraint are for the Android job; HealthKit pushes.
  [[OHSHealthBridge shared] enableBackgroundSyncWithTypes:types frequency:frequency completion:settle(resolve, reject)];
}

- (void)disableBackgroundSync:(RCTPromiseResolveBlock)resolve reject:(RCTPromiseRejectBlock)reject
{
  [[OHSHealthBridge shared] disableBackgroundSync];
  resolve(nil);
}

- (void)pendingParts:(RCTPromiseResolveBlock)resolve reject:(RCTPromiseRejectBlock)reject
{
  // The Swift SDK has no queue on disk: a failed upload leaves the anchor where it was.
  resolve(@0);
}

- (void)lastBackgroundSync:(RCTPromiseResolveBlock)resolve reject:(RCTPromiseRejectBlock)reject
{
  resolve([[OHSHealthBridge shared] lastBackgroundSync]);
}

@end
