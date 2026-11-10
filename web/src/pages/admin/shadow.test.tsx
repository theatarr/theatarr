import { expect, it } from "vitest";
import { agreementTone, MIN_COMPARISONS } from "./shadow";

it("keeps the agreement neutral below the go-live minimum of comparisons", () => {
  expect(MIN_COMPARISONS).toBe(20);
  expect(agreementTone(1, 0.95, 3)).toBeUndefined();
  expect(agreementTone(0.5, 0.95, 19)).toBeUndefined();
  expect(agreementTone(null, 0.95, 50)).toBeUndefined();
  expect(agreementTone(0.96, 0.95, 20)).toBe("ok");
  expect(agreementTone(0.9, 0.95, 40)).toBe("warn");
});
