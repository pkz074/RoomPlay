const test = require("node:test");
const assert = require("node:assert/strict");
const {
  formatDuration,
  statePosition,
  isNewerState,
  makeRequestId
} = require("./app.js");

test("formatDuration renders metadata durations", () => {
  assert.equal(formatDuration(220000), "3:40");
  assert.equal(formatDuration(-100), "0:00");
});

test("statePosition interpolates only while playing", () => {
  const now = Date.now();
  assert.equal(statePosition({ status: "paused", position_ms: 45000 }, now - 5000), 45000);
  assert.equal(statePosition({ status: "stopped", position_ms: 0 }, now - 5000), 0);
  const position = statePosition({ status: "playing", position_ms: 45000 }, now - 5000);
  assert.ok(position >= 49900 && position <= 55000);
});

test("isNewerState ignores stale versions and accepts newer versions", () => {
  assert.equal(isNewerState(null, { version: 1 }, "instance-1"), true);
  assert.equal(isNewerState({ instanceId: "instance-1", version: 6 }, { version: 5 }, "instance-1"), false);
  assert.equal(isNewerState({ instanceId: "instance-1", version: 6 }, { version: 7 }, "instance-1"), true);
});

test("makeRequestId produces stable-shaped unique values", () => {
  const first = makeRequestId();
  const second = makeRequestId();
  assert.equal(typeof first, "string");
  assert.ok(first.length > 0);
  assert.notEqual(first, second);
});
