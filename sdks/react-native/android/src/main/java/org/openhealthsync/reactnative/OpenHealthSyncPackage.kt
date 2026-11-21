// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.reactnative

import com.facebook.react.BaseReactPackage
import com.facebook.react.bridge.NativeModule
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.module.model.ReactModuleInfo
import com.facebook.react.module.model.ReactModuleInfoProvider

/** Registers [OpenHealthSyncModule]; found by React Native's autolinking. */
class OpenHealthSyncPackage : BaseReactPackage() {
    override fun getModule(name: String, reactContext: ReactApplicationContext): NativeModule? =
        if (name == OpenHealthSyncModule.NAME) OpenHealthSyncModule(reactContext) else null

    override fun getReactModuleInfoProvider() = ReactModuleInfoProvider {
        mapOf(
            OpenHealthSyncModule.NAME to ReactModuleInfo(
                name = OpenHealthSyncModule.NAME,
                className = OpenHealthSyncModule.NAME,
                canOverrideExistingModule = false,
                needsEagerInit = false,
                isCxxModule = false,
                isTurboModule = true,
            ),
        )
    }
}
