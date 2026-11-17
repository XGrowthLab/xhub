import { expect, it } from "vitest";
import { formatConsoleMoney } from "./money";

/** 双语金额格式保留正常、零、负值与微量消费，异常数不伪造零费用；纯函数无需清理。 */
it.each(["en", "zh-CN"])("formats recorded money without losing small charges in %s", (locale) => {
  expect(formatConsoleMoney(500, locale)).toBe("$500.00");
  expect(formatConsoleMoney(6.020123, locale)).toBe("$6.0201");
  expect(formatConsoleMoney(0, locale)).toBe("$0.00");
  expect(formatConsoleMoney(0.000024, locale)).toBe("$0.000024");
  expect(formatConsoleMoney(-0.000001, locale)).toBe("-$0.000001");
  expect(formatConsoleMoney(NaN, locale)).toBe("—");
  expect(formatConsoleMoney(Infinity, locale)).toBe("—");
});
