# SPDX-License-Identifier: AGPL-3.0-only

# The iOS side of open-health-sync-react-native: a Turbo Native Module over the Swift SDK
# (the OpenHealthSync and OpenHealthSyncHealthKit libraries of the Swift package at the root
# of the repository). React Native's autolinking finds this file.

require "json"

package = JSON.parse(File.read(File.join(__dir__, "package.json")))
repository = package["repository"]["url"]

# Where the Swift SDK comes from. It is a Swift package, not a pod, so it is added with
# React Native's spm_dependency:
#
# 1. From a checkout on disk, when there is one: OHS_SWIFT_SDK_PATH (the repository root,
#    where Package.swift is), else the repository this package sits in when it was
#    installed from the folder (npm links sdks/react-native into node_modules).
# 2. Else from the repository URL: the version in OHS_SWIFT_SDK_VERSION, or the branch in
#    OHS_SWIFT_SDK_BRANCH (main when neither is set).
local_sdk = ENV["OHS_SWIFT_SDK_PATH"].to_s
if local_sdk.empty?
  root = File.expand_path("../..", File.realpath(__dir__))
  if File.exist?(File.join(root, "Package.swift")) && File.directory?(File.join(root, "sdks/swift/Sources/OpenHealthSyncHealthKit"))
    local_sdk = root
  end
end

Pod::Spec.new do |s|
  s.name         = "OpenHealthSyncReactNative"
  s.version      = package["version"]
  s.summary      = "Syncs HealthKit to an Open Health Sync server from React Native."
  s.homepage     = repository.sub(/\.git\z/, "")
  s.license      = package["license"]
  s.authors      = "Open Health Sync contributors"

  s.platforms    = { :ios => min_ios_version_supported }
  s.source       = { :git => repository, :tag => "v#{s.version}" }

  s.source_files = "ios/**/*.{h,m,mm,swift}"
  s.private_header_files = "ios/**/*.h"
  s.frameworks   = "HealthKit"
  s.swift_version = "5.9"

  install_modules_dependencies(s)

  products = ["OpenHealthSync", "OpenHealthSyncHealthKit"]
  if !local_sdk.empty?
    spm_dependency(s, url: File.expand_path(local_sdk), requirement: {}, products: products)
  elsif !ENV["OHS_SWIFT_SDK_VERSION"].to_s.empty?
    spm_dependency(s, url: repository, requirement: { kind: "upToNextMajorVersion", minimumVersion: ENV["OHS_SWIFT_SDK_VERSION"] }, products: products)
  else
    branch = ENV["OHS_SWIFT_SDK_BRANCH"].to_s
    spm_dependency(s, url: repository, requirement: { kind: "branch", branch: branch.empty? ? "main" : branch }, products: products)
  end
end
