// SPDX-License-Identifier: AGPL-3.0-only

module.exports = function (api) {
  api.cache(true);
  return {
    presets: ["babel-preset-expo"],
  };
};
