import { absTime, formatAgo, formatBytes, formatCount, formatTime, formatWindow, humanize, initials, providerLabel, relTime, splitFigure } from "./format";

test("formatAgo", () => {
  expect(formatAgo(null)).toBe("-");
  expect(formatAgo(undefined)).toBe("-");
  expect(formatAgo(0)).toBe("0 s");
  expect(formatAgo(59)).toBe("59 s");
  expect(formatAgo(60)).toBe("1 min");
  expect(formatAgo(3599)).toBe("59 min");
  expect(formatAgo(3600)).toBe("1 h");
  expect(formatAgo(3 * 3600 + 5 * 60)).toBe("3 h 5 min");
  expect(formatAgo(86400)).toBe("1 d");
  expect(formatAgo(2 * 86400 + 4 * 3600)).toBe("2 d 4 h");
  expect(formatAgo(-120)).toBe("in 2 min");
});

test("formatTime", () => {
  const now = Date.parse("2026-10-13T12:00:00Z");
  expect(formatTime(null, now)).toBe("-");
  expect(formatTime("2026-10-13T09:00:00Z", now)).toBe("2026-10-13 09:00 UTC (3 h ago)");
  expect(formatTime("2026-10-13T12:30:00Z", now)).toBe("2026-10-13 12:30 UTC (in 30 min)");
  expect(formatTime("not a date", now)).toBe("not a date");
});

test("absTime, relTime and formatWindow", () => {
  const now = Date.parse("2026-10-13T12:00:00Z");
  expect(absTime("2026-10-13T09:00:00Z")).toBe("2026-10-13 09:00 UTC");
  expect(absTime(null)).toBe("-");
  expect(relTime("2026-10-13T11:59:40Z", now)).toBe("just now");
  expect(relTime("2026-10-13T11:48:00Z", now)).toBe("12 min ago");
  expect(relTime("2026-10-13T09:00:00Z", now)).toBe("3 h ago");
  expect(relTime("2026-10-09T12:00:00Z", now)).toBe("4 d ago");
  expect(relTime("2026-10-13T12:05:00Z", now)).toBe("in 5 min");
  expect(relTime("2026-08-01T12:00:00Z", now)).toBe("2026-08-01");
  expect(relTime("soon", now)).toBe("soon");
  expect(formatWindow("2026-10-16T03:33:00Z", "2026-10-16T04:33:00Z")).toBe("2026-10-16 03:33 to 04:33");
  expect(formatWindow("2026-10-16T22:00:00Z", "2026-10-17T00:00:00Z")).toBe("2026-10-16 22:00 to 2026-10-17 00:00");
});

test("humanize, initials and providerLabel", () => {
  expect(humanize("needs_reauth")).toBe("Needs reconnecting");
  expect(humanize("rate_limited")).toBe("Rate limited");
  expect(humanize("succeeded")).toBe("Succeeded");
  expect(humanize(null)).toBe("-");
  expect(humanize("constructor"), "no prototype lookup").toBe("Constructor");
  expect(initials("Apple Health")).toBe("AH");
  expect(initials("intervals.icu")).toBe("I");
  expect(initials("self_report")).toBe("SR");
  expect(initials("")).toBe("?");
  expect(providerLabel({ oura: "Oura" }, "oura")).toBe("Oura");
  expect(providerLabel({}, "apple_health")).toBe("Apple health");
  expect(providerLabel(null, "toString")).toBe("ToString");
});

test("formatCount and formatBytes", () => {
  expect(formatCount(0)).toBe("0");
  expect(formatCount(1234567)).toBe("1,234,567");
  expect(formatCount(null)).toBe("-");
  expect(formatBytes(0)).toBe("0 B");
  expect(formatBytes(950)).toBe("950 B");
  expect(formatBytes(1500)).toBe("1.5 kB");
  expect(formatBytes(12_000_000)).toBe("12 MB");
  expect(formatBytes(undefined)).toBe("-");
});

test("splitFigure", () => {
  expect(splitFigure("3 of 9")).toEqual({ figure: "3", unit: "of 9", word: false });
  expect(splitFigure("1.2 MB")).toEqual({ figure: "1.2", unit: "MB", word: false });
  expect(splitFigure("1,234")).toEqual({ figure: "1,234", unit: "", word: false });
  expect(splitFigure("12 min ago")).toEqual({ figure: "12", unit: "min ago", word: false });
  expect(splitFigure("-")).toEqual({ figure: "-", unit: "", word: false });
  expect(splitFigure(null)).toEqual({ figure: "-", unit: "", word: false });
  expect(splitFigure("Never")).toEqual({ figure: "Never", unit: "", word: true });
  expect(splitFigure("just now")).toEqual({ figure: "just now", unit: "", word: true });
});
