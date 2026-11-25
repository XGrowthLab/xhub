import { describe, expect, it } from "vitest";
import { checkHasResponse, sortSessionLogs } from "./utils";

const log = (id: string, startTime: string, endTime: string, request_duration_ms?: number) => ({
  request_id: id,
  startTime,
  endTime,
  request_duration_ms,
});

const ids = (rows: { request_id: string }[]) => rows.map((row) => row.request_id);

describe("sortSessionLogs", () => {
  const rows = [
    log("mid-duration", "2026-07-08T10:00:01.000Z", "2026-07-08T10:00:01.500Z", 2000),
    log("longest", "2026-07-08T10:00:02.000Z", "2026-07-08T10:00:02.500Z", 5000),
    log("shortest", "2026-07-08T10:00:03.000Z", "2026-07-08T10:00:03.500Z", 300),
    log("earliest-no-duration-field", "2026-07-08T10:00:00.000Z", "2026-07-08T10:00:04.000Z"),
  ];

  it("duration mode sorts longest call first, deriving duration from timestamps when the field is missing", () => {
    expect(ids(sortSessionLogs(rows, "duration"))).toEqual([
      "longest",
      "earliest-no-duration-field",
      "mid-duration",
      "shortest",
    ]);
  });

  it("start_time mode sorts calls in the order they started", () => {
    expect(ids(sortSessionLogs(rows, "start_time"))).toEqual([
      "earliest-no-duration-field",
      "mid-duration",
      "longest",
      "shortest",
    ]);
  });

  it("does not mutate the input array", () => {
    const input = [...rows];
    sortSessionLogs(input, "duration");
    expect(ids(input)).toEqual(ids(rows));
  });
});

/** 验证日志抽屉解析任意响应时可安全判断正文状态，覆盖 JSON 空值、标量、数组及非 JSON。使用内存夹具，无清理。 */
describe("checkHasResponse", () => {
  it.each([
    [null, false], [undefined, false], ["null", false], ['""', false], ["  ", false],
    [{}, false], ["[]", false], ["false", true], [0, true], ['"null"', true],
    ["upstream returned 502", true], [{ error: { message: "unavailable" } }, true], [[0], true],
  ])("checks response %j without throwing", (response, expected) => {
    expect(checkHasResponse(response)).toBe(expected);
  });
});
