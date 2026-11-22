const test = require("node:test");
const assert = require("node:assert/strict");
const {
  formatDuration, statePosition, isNewerState, applyStateSnapshot,
  makeRequestId, escapeHtml, MockApi, ApiClient
} = require("./app.js");

test("formatDuration handles positive and negative durations", () => {
  assert.equal(formatDuration(220000), "3:40");
  assert.equal(formatDuration(-100), "0:00");
});

test("statePosition advances only while playing", () => {
  const now = Date.now();
  assert.equal(statePosition({ status: "paused", position_ms: 45000 }, now - 5000), 45000);
  assert.equal(statePosition({ status: "stopped", position_ms: 0 }, now - 5000), 0);
  assert.ok(statePosition({ status: "playing", position_ms: 45000 }, now - 5000) >= 49900);
});

test("state versions reject stale updates and preserve current instance", () => {
  const current = { instanceId: "instance-1", version: 6 };
  assert.equal(isNewerState(null, { version: 1 }, "instance-1"), true);
  assert.equal(isNewerState(current, { version: 5 }, "instance-1"), false);
  assert.equal(isNewerState(current, { version: 7 }, "instance-1"), true);
  assert.equal(isNewerState(current, { version: 1 }, "instance-old"), false);
  assert.equal(isNewerState(current, { version: 1 }, "instance-new", true), true);
  assert.equal(isNewerState(current, null, "instance-1"), false);
});

test("authoritative snapshot establishes restart; delayed old instance events are ignored", () => {
  const room = { id: "bedroom", instance_id: "old", state_stale: true };
  const old = { instanceId: "old", version: 99, receivedAt: 1000 };
  const restarted = { status: "stopped", current: null, position_ms: 0, history: [], queue: [], version: 0 };
  const snapshot = applyStateSnapshot(room, old, restarted, "new", true, 2000);
  assert.equal(snapshot.room.instance_id, "new");
  assert.equal(snapshot.view.version, 0);
  assert.equal(snapshot.view.receivedAt, 2000);
  assert.equal(applyStateSnapshot(snapshot.room, snapshot.view, { version: 100 }, "old"), null);
});

test("new state event resets progress interpolation timestamp", () => {
  const room = { id: "bedroom", instance_id: "i" };
  const view = { instanceId: "i", version: 1, receivedAt: 1000 };
  const snapshot = applyStateSnapshot(room, view, { status: "playing", position_ms: 0, version: 2 }, "i", false, 8000);
  assert.equal(snapshot.view.receivedAt, 8000);
  assert.equal(snapshot.view.state.position_ms, 0);
});

test("equal-version state updates are valid for fresher playback positions", () => {
  const view = { instanceId: "i", version: 2 };
  assert.equal(isNewerState(view, { version: 2, position_ms: 55000 }, "i"), true);
});

test("HTML escaping neutralizes untrusted library metadata", () => {
  assert.equal(escapeHtml('<img src="x" onerror="evil()">'), '&lt;img src=&quot;x&quot; onerror=&quot;evil()&quot;&gt;');
  assert.equal(escapeHtml("&'"), "&amp;&#039;");
});

test("request IDs are nonempty and different", () => {
  const a = makeRequestId(), b = makeRequestId();
  assert.equal(typeof a, "string");
  assert.ok(a.length > 0);
  assert.notEqual(a, b);
});

test("ApiClient uses expected HTTP routes, payloads and stable request ID", async () => {
  const original = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, options = {}) => {
    calls.push({ url, options });
    return { ok: true, text: async () => JSON.stringify({ request_id: "cmd-1", state: { version: 1 } }) };
  };
  try {
    const api = new ApiClient("");
    await api.rooms();
    await api.room("bedroom");
    await api.tracks();
    await api.queue("bedroom");
    await api.command("bedroom", { request_id: "cmd-1", type: "playback.pause", payload: {} }, "cmd-1");
    await api.queueAdd("bedroom", { track_id: "t1" }, "cmd-1");
    await api.queueRemove("bedroom", "item-1", "cmd-1");
    await api.queueClear("bedroom", "cmd-1");
    assert.deepEqual(calls.map(x => x.url), [
      "/api/v1/rooms", "/api/v1/rooms/bedroom", "/api/v1/tracks", "/api/v1/rooms/bedroom/queue",
      "/api/v1/rooms/bedroom/commands", "/api/v1/rooms/bedroom/queue",
      "/api/v1/rooms/bedroom/queue/item-1", "/api/v1/rooms/bedroom/queue"
    ]);
    assert.equal(calls[4].options.method, "POST");
    assert.equal(calls[4].options.headers["X-Request-ID"], "cmd-1");
    assert.deepEqual(JSON.parse(calls[4].options.body), { request_id: "cmd-1", type: "playback.pause", payload: {} });
    assert.equal(calls[5].options.headers["X-Request-ID"], "cmd-1");
    assert.equal(calls[6].options.method, "DELETE");
    assert.equal(calls[7].options.method, "DELETE");
  } finally { globalThis.fetch = original; }
});

test("ApiClient encodes user-controlled room and queue IDs", async () => {
  const original = globalThis.fetch;
  const paths = [];
  globalThis.fetch = async (url) => {
    paths.push(url);
    return { ok: true, text: async () => "{}" };
  };
  try {
    const api = new ApiClient("");
    await api.room("room/one");
    await api.queueRemove("room/one", "a/b", "req");
    assert.equal(paths[0], "/api/v1/rooms/room%2Fone");
    assert.equal(paths[1], "/api/v1/rooms/room%2Fone/queue/a%2Fb");
  } finally { globalThis.fetch = original; }
});

test("ApiClient propagates structured errors with timeout uncertainty", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => ({
    ok: false, status: 504,
    text: async () => JSON.stringify({ request_id: "cmd-1", error: { code: "COMMAND_TIMEOUT", message: "Outcome unknown" } })
  });
  try {
    await assert.rejects(() => new ApiClient("").command("bedroom", { request_id: "cmd-1", type: "playback.next", payload: {} }, "cmd-1"), (error) => {
      assert.equal(error.code, "COMMAND_TIMEOUT");
      assert.equal(error.status, 504);
      assert.equal(error.requestId, "cmd-1");
      return true;
    });
  } finally { globalThis.fetch = original; }
});

test("MockApi lists rooms and keeps rooms independent", async () => {
  const api = new MockApi();
  const { rooms } = await api.rooms();
  assert.equal(rooms.length, 2);
  assert.equal(rooms[1].availability, "offline");
  const kitchenBefore = (await api.room("kitchen")).state.version;
  await api.command("bedroom", { type: "playback.pause", payload: {} }, "req-1");
  const kitchenAfter = (await api.room("kitchen")).state.version;
  assert.equal(kitchenAfter, kitchenBefore);
  await assert.rejects(() => api.queueAdd("kitchen", { track_id: "track-001" }, "req-2"), (error) => error.code === "ROOM_OFFLINE");
});

test("MockApi uses occurrence IDs and clear leaves current intact", async () => {
  const api = new MockApi();
  const before = (await api.room("bedroom")).state.current.id;
  const added = await api.queueAdd("bedroom", { track_id: "track-001" }, "req-1");
  assert.ok(added.state.queue.length >= 1);
  await api.queueClear("bedroom", "req-2");
  const after = (await api.room("bedroom")).state;
  assert.equal(after.queue.length, 0);
  assert.equal(after.current.id, before);
});

test("MockApi next/previous and pause navigation", async () => {
  const api = new MockApi();
  await api.command("bedroom", { type: "playback.pause", payload: {} }, "req-1");
  let result = await api.command("bedroom", { type: "playback.next", payload: {} }, "req-2");
  assert.equal(result.state.status, "playing");
  result = await api.command("bedroom", { type: "playback.previous", payload: {} }, "req-3");
  assert.equal(result.state.status, "playing");
  assert.equal(result.state.current.track_id, "track-001");
});
