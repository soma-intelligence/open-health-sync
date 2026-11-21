// SPDX-License-Identifier: AGPL-3.0-only

// The few names src/health takes from React Native, so this package typechecks and builds
// without installing React Native (it is an optional peer: the main entry runs in Node and
// in browsers). An app compiles against the real types of its own React Native.

declare module "react-native" {
  export interface TurboModule {}

  export const TurboModuleRegistry: {
    get<T extends TurboModule>(name: string): T | null | undefined;
    getEnforcing<T extends TurboModule>(name: string): T;
  };

  export const Platform: { readonly OS: string };

  export interface EventSubscription {
    remove(): void;
  }

  export namespace CodegenTypes {
    type EventEmitter<T> = (handler: (payload: T) => void | Promise<void>) => EventSubscription;
  }
}
