# RoomPlay Web Controller

Kaira owns this frontend directory. Use plain HTML, CSS and JavaScript.
See `docs/agents/KAIRA.md`, `docs/API.md`, and `AGENTS.md`.

The interface uses the coordinator REST API and one SSE stream. Do not connect the browser directly to NATS.

## Run

Serve this directory with a static HTTP server, for example:

```sh
python -m http.server 4173
```

Open `http://127.0.0.1:4173/?mock=1` for self-contained mock fixtures. Remove `?mock=1` to connect to the same-origin coordinator endpoints.

Run the frontend unit checks with Node.js:

```sh
node --test web/app.test.js
```
