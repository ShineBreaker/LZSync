import { test, expect } from "bun:test";
import { runView, stageKey } from "./state";

test("stage keys keep kind and content key unambiguous", () => {
  expect(stageKey("ab", "c")).not.toBe(stageKey("a", "bc"));
});

test("runView does not expose mutable lane state", () => {
  const first = runView();
  first.lanes.test = { pull: 1, write: 1 };
  expect(runView().lanes.test).toBeUndefined();
});
