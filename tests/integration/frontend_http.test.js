const test = require("node:test");
const assert = require("node:assert/strict");
const http = require("node:http");
const { ApiClient } = require("../../web/app.js");

test("frontend ApiClient performs real HTTP requests with stable IDs and structured errors", async (t) => {
  const calls = [];
  const server = http.createServer(async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = Buffer.concat(chunks).toString("utf8");
    calls.push({ method: req.method, url: req.url, headers: req.headers, body });
    res.setHeader("Content-Type", "application/json");
    if (req.method === "GET" && req.url === "/api/v1/tracks") {
      res.end(JSON.stringify({ tracks: [{ id: "track-1", title: "Demo", artist: "Test", duration_ms: 3000, url: "/audio/demo.mp3" }] }));
    } else if (req.method === "GET" && req.url === "/api/v1/rooms") {
      res.end(JSON.stringify({ rooms: [{ id: "bedroom", name: "Bedroom", availability: "online", state: { status: "stopped", current: null, position_ms: 0, history: [], queue: [], version: 0 } }] }));
    } else if (req.method === "POST" && req.url === "/api/v1/rooms/bedroom/commands") {
      const command = JSON.parse(body);
      if (req.headers["x-request-id"] !== command.request_id) {
        res.writeHead(400);
        res.end(JSON.stringify({ request_id: command.request_id, error: { code: "INVALID_PAYLOAD", message: "ID mismatch" } }));
        return;
      }
      if (command.type === "playback.next") {
        res.writeHead(504);
        res.end(JSON.stringify({ request_id: command.request_id, error: { code: "COMMAND_TIMEOUT", message: "Outcome unknown" } }));
        return;
      }
      res.end(JSON.stringify({ request_id: command.request_id, state: { status: "playing", version: 1, current: { id: "item-1", track_id: command.payload.track_id }, position_ms: 0, history: [], queue: [] } }));
    } else if (req.method === "POST" && req.url === "/api/v1/rooms/bedroom/queue") {
      if (req.headers["x-request-id"] !== "req-queue") {
        res.writeHead(400);
        res.end(JSON.stringify({ error: { code: "INVALID_PAYLOAD", message: "Missing request ID" } }));
        return;
      }
      res.end(JSON.stringify({ request_id: "req-queue", state: { status: "stopped", version: 1, current: null, position_ms: 0, history: [], queue: [{ id: "item-2", track_id: "track-1" }] } }));
    } else {
      res.writeHead(404);
      res.end(JSON.stringify({ error: { code: "ROOM_NOT_FOUND", message: "Unknown" } }));
    }
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));

  const api = new ApiClient(`http://127.0.0.1:${server.address().port}`);
  const rooms = await api.rooms();
  assert.equal(rooms.rooms[0].id, "bedroom");
  const catalog = await api.tracks();
  assert.equal(catalog.tracks[0].id, "track-1");

  const play = await api.command("bedroom", { request_id: "req-play", type: "playback.play", payload: { track_id: "track-1" } }, "req-play");
  assert.equal(play.request_id, "req-play");
  assert.equal(play.state.current.track_id, "track-1");

  const added = await api.queueAdd("bedroom", { track_id: "track-1" }, "req-queue");
  assert.equal(added.state.queue[0].id, "item-2");

  await assert.rejects(() => api.command("bedroom", { request_id: "req-next", type: "playback.next", payload: {} }, "req-next"), (error) => {
    assert.equal(error.code, "COMMAND_TIMEOUT");
    assert.equal(error.requestId, "req-next");
    assert.equal(error.status, 504);
    return true;
  });

  // A 504 must not trigger an automatic retry: only one network request with this ID exists.
  assert.equal(calls.filter((call) => call.body.includes("req-next")).length, 1);
  assert.equal(calls.find((call) => call.body.includes("req-play")).headers["x-request-id"], "req-play");
  assert.equal(calls.find((call) => call.body.includes("req-queue")).headers["x-request-id"], "req-queue");
});
