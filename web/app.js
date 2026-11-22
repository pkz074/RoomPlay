(function () {
  "use strict";

  function formatDuration(ms) {
    const totalSeconds = Math.max(0, Math.floor(Number(ms || 0) / 1000));
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = String(totalSeconds % 60).padStart(2, "0");
    return `${minutes}:${seconds}`;
  }

  function makeRequestId() {
    if (globalThis.crypto && typeof globalThis.crypto.randomUUID === "function") {
      return globalThis.crypto.randomUUID();
    }
    return `cmd-${Date.now()}-${Math.random().toString(36).slice(2)}`;
  }

  function statePosition(state, receivedAt = Date.now()) {
    if (!state) return 0;
    if (state.status !== "playing") return Number(state.position_ms || 0);
    return Number(state.position_ms || 0) + Math.max(0, Date.now() - receivedAt);
  }

  function isNewerState(current, incoming, incomingInstanceId) {
    if (!incoming) return false;
    if (!current) return true;
    if (incomingInstanceId && current.instanceId && incomingInstanceId !== current.instanceId) {
      return incomingInstanceId === incomingInstanceId;
    }
    return Number(incoming.version || 0) >= Number(current.version || 0);
  }

  function escapeHtml(value) {
    return String(value ?? "")
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#039;");
  }

  function MockApi() {
    const tracks = [
      { id: "track-001", title: "Lost in a Dream", artist: "Eternity Arcade", duration_ms: 220000, url: "/assets/music/Eternity_Arcade_-_Lost_in_a_Dream_(vocal_retro_pop_music_made_with_AI).mp3" }
    ];
    const rooms = [
      {
        id: "bedroom", name: "Bedroom", instance_id: "mock-bedroom",
        availability: "online", last_seen: new Date().toISOString(), state_stale: false,
        state: { status: "playing", current: { id: "item-001", track_id: "track-001" }, position_ms: 45000, history: [], queue: [{ id: "item-002", track_id: "track-001" }], version: 4 }
      },
      {
        id: "kitchen", name: "Kitchen", instance_id: "mock-kitchen",
        availability: "offline", last_seen: new Date(Date.now() - 12000).toISOString(), state_stale: true,
        state: { status: "paused", current: { id: "item-010", track_id: "track-001" }, position_ms: 60000, history: [], queue: [], version: 2 }
      }
    ];

    function room(id) { return rooms.find((entry) => entry.id === id); }
    this.rooms = async () => ({ rooms: rooms.map((entry) => ({ ...entry, state: entry.state && { ...entry.state } })) });
    this.tracks = async () => ({ tracks });
    this.room = async (id) => ({ ...room(id) });
    this.queue = async (id) => ({ room_id: id, queue: room(id)?.state.queue || [] });
    this.command = async (id, body, requestId) => {
      const current = room(id);
      if (!current || current.availability !== "online") throw { code: "ROOM_OFFLINE", message: `${current?.name || id} is unavailable.`, requestId };
      current.state = current.state || { status: "stopped", current: null, position_ms: 0, history: [], queue: [], version: 0 };
      current.state.version += 1;
      if (body.type === "playback.play") current.state = { ...current.state, status: "playing", current: { id: `item-${Date.now()}`, track_id: body.payload.track_id }, position_ms: 0 };
      if (body.type === "playback.pause") current.state.status = "paused";
      if (body.type === "playback.resume") current.state.status = "playing";
      if (body.type === "playback.stop") current.state = { ...current.state, status: "stopped", position_ms: 0 };
      if (body.type === "playback.next") {
        if (current.state.queue.length) {
          const next = current.state.queue.shift();
          if (current.state.current) current.state.history.push(current.state.current);
          current.state.current = next;
          current.state.status = "playing";
          current.state.position_ms = 0;
        } else {
          if (current.state.current) current.state.history.push(current.state.current);
          current.state.current = null;
          current.state.status = "stopped";
          current.state.position_ms = 0;
        }
      }
      if (body.type === "playback.previous") {
        if (current.state.history.length) {
          const previous = current.state.history.pop();
          if (current.state.current) current.state.queue.unshift(current.state.current);
          current.state.current = previous;
          current.state.status = "playing";
          current.state.position_ms = 0;
        } else if (current.state.current) current.state.position_ms = 0;
      }
      if (body.type === "queue.clear") current.state.queue = [];
      return { request_id: requestId, state: current.state };
    };
    this.queueAdd = async (id, body, requestId) => {
      const current = room(id);
      if (!current || current.availability !== "online") throw { code: "ROOM_OFFLINE", message: `${current?.name || id} is unavailable.`, requestId };
      current.state.queue.push({ id: `item-${Date.now()}`, track_id: body.track_id });
      current.state.version += 1;
      return { request_id: requestId, state: current.state };
    };
    this.queueRemove = async (id, itemId, requestId) => {
      const current = room(id);
      if (!current || current.availability !== "online") throw { code: "ROOM_OFFLINE", message: `${current?.name || id} is unavailable.`, requestId };
      const before = current.state.queue.length;
      current.state.queue = current.state.queue.filter((item) => item.id !== itemId);
      if (before !== current.state.queue.length) current.state.version += 1;
      return { request_id: requestId, state: current.state };
    };
    this.queueClear = async (id, requestId) => this.command(id, { type: "queue.clear", payload: {} }, requestId);
  }

  function ApiClient(baseUrl) {
    async function request(path, options = {}) {
      const response = await fetch(`${baseUrl}${path}`, {
        ...options,
        headers: { Accept: "application/json", ...(options.headers || {}) }
      });
      let data = null;
      const text = await response.text();
      if (text) {
        try { data = JSON.parse(text); } catch { data = null; }
      }
      if (!response.ok) {
        const error = data?.error || { code: "SERVICE_UNAVAILABLE", message: "The service could not complete the request." };
        throw { code: error.code, message: error.message, requestId: data?.request_id || null, status: response.status };
      }
      return data;
    }

    this.rooms = () => request("/api/v1/rooms");
    this.room = (id) => request(`/api/v1/rooms/${encodeURIComponent(id)}`);
    this.tracks = () => request("/api/v1/tracks");
    this.queue = (id) => request(`/api/v1/rooms/${encodeURIComponent(id)}/queue`);
    this.command = (id, body, requestId) => request(`/api/v1/rooms/${encodeURIComponent(id)}/commands`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Request-ID": requestId },
      body: JSON.stringify(body)
    });
    this.queueAdd = (id, body, requestId) => request(`/api/v1/rooms/${encodeURIComponent(id)}/queue`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Request-ID": requestId },
      body: JSON.stringify(body)
    });
    this.queueRemove = (id, itemId, requestId) => request(`/api/v1/rooms/${encodeURIComponent(id)}/queue/${encodeURIComponent(itemId)}`, {
      method: "DELETE",
      headers: { "X-Request-ID": requestId }
    });
    this.queueClear = (id, requestId) => request(`/api/v1/rooms/${encodeURIComponent(id)}/queue`, {
      method: "DELETE",
      headers: { "X-Request-ID": requestId }
    });
  }

  const appExports = { formatDuration, makeRequestId, statePosition, isNewerState, escapeHtml, MockApi, ApiClient };
  if (typeof module !== "undefined" && module.exports) module.exports = appExports;

  function startApp() {
    const mockMode = new URLSearchParams(globalThis.location?.search || "").get("mock") === "1";
    const api = mockMode ? new MockApi() : new ApiClient("");
    const dom = {
      roomList: document.getElementById("roomList"),
      selectedRoomName: document.getElementById("selectedRoomName"),
      playerContent: document.getElementById("playerContent"),
      historyList: document.getElementById("historyList"),
      trackList: document.getElementById("trackList"),
      primaryButton: document.getElementById("primaryButton"),
      previousButton: document.getElementById("previousButton"),
      nextButton: document.getElementById("nextButton"),
      stopButton: document.getElementById("stopButton"),
      clearQueueButton: document.getElementById("clearQueueButton"),
      refreshRooms: document.getElementById("refreshRooms"),
      secondaryMenu: document.getElementById("secondaryMenu"),
      connectionBadge: document.getElementById("connectionBadge"),
      toastRegion: document.getElementById("toastRegion")
    };

    let rooms = [];
    let tracks = [];
    let selectedRoomId = null;
    let eventSource = null;
    let pending = new Set();
    const roomView = new Map();

    function toast(message, kind = "error") {
      const node = document.createElement("div");
      node.className = `toast ${kind}`;
      node.textContent = message;
      dom.toastRegion.appendChild(node);
      setTimeout(() => node.remove(), 6500);
    }

    function currentRoom() { return rooms.find((room) => room.id === selectedRoomId) || null; }
    function trackById(id) { return tracks.find((track) => track.id === id) || null; }
    function queueTracks(queue) { return queue.map((item) => ({ item, track: trackById(item.track_id) })); }

    function updateRoomView(room) {
      if (!room) return;
      const old = roomView.get(room.id);
      if (room.state) {
        const view = { instanceId: room.instance_id || old?.instanceId || null, version: room.state.version || 0, state: room.state, receivedAt: old?.instanceId === room.instance_id ? (old.receivedAt || Date.now()) : Date.now(), instanceChanged: old?.instanceChanged || false };
        roomView.set(room.id, view);
      }
    }

    function mergeRoomSummary(room) {
      const existing = rooms.find((entry) => entry.id === room.id);
      if (existing) Object.assign(existing, room);
      else rooms.push(room);
      updateRoomView(room);
    }

    function roomStatus(room) {
      if (room.availability === "offline") return { text: "Offline", className: "offline", dot: "offline" };
      if (room.state_stale) return { text: "Stale / unknown", className: "stale", dot: "stale" };
      return { text: "Online", className: "online", dot: "online" };
    }

    function renderRooms() {
      if (!rooms.length) {
        dom.roomList.innerHTML = '<div class="empty-state">No rooms are registered.</div>';
        return;
      }
      dom.roomList.innerHTML = rooms.map((room) => {
        const status = roomStatus(room);
        return `<button class="room-card ${room.id === selectedRoomId ? "selected" : ""}" type="button" data-room-id="${escapeHtml(room.id)}" aria-pressed="${room.id === selectedRoomId}">
          <strong>${escapeHtml(room.name || room.id)}</strong>
          <span class="room-meta"><span><i class="status-dot ${status.dot}" aria-hidden="true"></i>${status.text}</span><span>${room.state?.status ? escapeHtml(room.state.status) : "Unknown"}</span></span>
        </button>`;
      }).join("");
      dom.roomList.querySelectorAll("[data-room-id]").forEach((button) => {
        button.addEventListener("click", () => selectRoom(button.dataset.roomId));
      });
    }

    function renderPlayer() {
      const room = currentRoom();
      if (!room) {
        dom.selectedRoomName.textContent = "No room selected";
        dom.playerContent.innerHTML = '<div class="empty-state">Select an online room to begin.</div>';
        dom.historyList.innerHTML = '<div class="empty-state">No history.</div>';
        setControlsDisabled(true);
        return;
      }
      dom.selectedRoomName.textContent = room.name || room.id;
      const view = roomView.get(room.id);
      const state = room.state || view?.state || null;
      const status = roomStatus(room);
      const offline = room.availability !== "online";
      const stale = room.state_stale || !state;
      setControlsDisabled(offline || stale);

      if (!state) {
        dom.playerContent.innerHTML = `<div class="empty-state ${offline ? "offline-state" : "stale-state"}">${offline ? "This room is offline. Controls are disabled." : "State is stale or unavailable."}</div>`;
        dom.historyList.innerHTML = '<div class="empty-state">No history.</div>';
        return;
      }

      const track = state.current ? trackById(state.current.track_id) : null;
      const title = track?.title || (state.current ? state.current.track_id : "Nothing selected");
      const artist = track?.artist || "Unknown track";
      const duration = track?.duration_ms || 0;
      const position = statePosition(state, view?.receivedAt || Date.now());
      const progress = duration > 0 ? Math.min(100, position / duration * 100) : 0;
      const statusLabel = state.status === "playing" ? "Playing" : state.status === "paused" ? "Paused" : "Stopped";
      dom.playerContent.innerHTML = `<div class="track-hero">
        <div class="cover" aria-hidden="true">${escapeHtml((title[0] || "?").toUpper())}</div>
        <div class="track-info">
          ${offline ? '<div class="stale-state">Offline · controls disabled</div>' : stale ? '<div class="stale-state">Stale state · refresh to verify</div>' : ""}
          <div class="playback-status">${statusLabel}</div>
          <h3>${escapeHtml(title)}</h3>
          <p class="artist">${escapeHtml(artist)}</p>
          <div class="progress-row">
            <div class="progress" role="progressbar" aria-label="Track position" aria-valuemin="0" aria-valuemax="${duration}" aria-valuenow="${Math.floor(position)}">
              <div class="progress-bar" style="width:${progress}%"></div>
            </div>
            <span class="time-label">${formatDuration(position)} / ${duration ? formatDuration(duration) : "--:--"}</span>
          </div>
        </div>
      </div>`;

      dom.primaryButton.textContent = state.status === "playing" ? "Ⅱ" : "▶";
      dom.primaryButton.setAttribute("aria-label", state.status === "paused" ? "Resume playback" : state.status === "playing" ? "Pause playback" : "Play selected track");
      dom.primaryButton.disabled = offline || stale;

      const history = state.history || [];
      dom.historyList.innerHTML = history.length ? history.slice().reverse().map((item, index) => {
        const trackItem = trackById(item.track_id);
        return `<div class="queue-item"><span class="queue-index">${history.length - index}</span><div class="item-copy"><strong>${escapeHtml(trackItem?.title || item.track_id)}</strong><span>${escapeHtml(trackItem?.artist || "Unknown track")} · ${item.id}</span></div></div>`;
      }).join("") : '<div class="empty-state">No history.</div>';

      renderQueue(state.queue || []);
    }

    function renderQueue(queue) {
      const existing = document.getElementById("queueList");
      if (existing) existing.innerHTML = queueEntries(queue);
      else {
        const queueCard = document.createElement("section");
        queueCard.className = "history-card";
        queueCard.innerHTML = `<div class="section-heading compact"><h2>Upcoming queue</h2></div><div id="queueList" class="item-list"></div>`;
        dom.historyList.parentElement.insertAdjacentElement("afterend", queueCard);
        document.getElementById("queueList").innerHTML = queueEntries(queue);
      }
    }

    function queueEntries(queue) {
      if (!queue.length) return '<div class="empty-state">Queue is empty.</div>';
      return queueTracks(queue).map(({ item, track }, index) => `<div class="queue-item">
        <span class="queue-index">${index + 1}</span>
        <div class="item-copy"><strong>${escapeHtml(track?.title || item.track_id)}</strong><span>${escapeHtml(track?.artist || "Unknown track")} · ${escapeHtml(item.id)}</span></div>
        <button class="remove-button" type="button" data-item-id="${escapeHtml(item.id)}" aria-label="Remove ${escapeHtml(track?.title || item.track_id)}">×</button>
      </div>`).join("");
    }

    function bindQueueRemoveButtons() {
      document.querySelectorAll("[data-item-id]").forEach((button) => {
        if (button.dataset.bound === "true") return;
        button.dataset.bound = "true";
        button.addEventListener("click", () => removeQueueItem(button.dataset.itemId));
      });
    }

    function setControlsDisabled(disabled) {
      const room = currentRoom();
      const unavailable = disabled || !room || room.availability !== "online" || room.state_stale || !room.state;
      [dom.primaryButton, dom.previousButton, dom.nextButton, dom.stopButton, dom.clearQueueButton].forEach((button) => {
        button.disabled = unavailable || pending.size > 0;
      });
    }

    function renderTracks() {
      dom.trackList.innerHTML = tracks.length ? tracks.map((track) => `<article class="track-card">
        <h3>${escapeHtml(track.title)}</h3>
        <p>${escapeHtml(track.artist)} · ${formatDuration(track.duration_ms)}</p>
        <div class="track-actions">
          <button type="button" data-play-track="${escapeHtml(track.id)}">Play now</button>
          <button type="button" data-add-track="${escapeHtml(track.id)}">Add to queue</button>
        </div>
      </article>`).join("") : '<div class="empty-state">No licensed tracks available.</div>';
      dom.trackList.querySelectorAll("[data-play-track]").forEach((button) => button.addEventListener("click", () => playTrack(button.dataset.playTrack)));
      dom.trackList.querySelectorAll("[data-add-track]").forEach((button) => button.addEventListener("click", () => addTrack(button.dataset.addTrack)));
    }

    async function selectRoom(roomId) {
      selectedRoomId = roomId;
      renderRooms();
      renderPlayer();
      try {
        const detail = await api.room(roomId);
        mergeRoomSummary(detail);
        renderRooms();
        renderPlayer();
      } catch (error) {
        handleError(error, "Could not refresh the selected room.");
      }
    }

    async function loadInitialSnapshots() {
      dom.connectionBadge.className = "connection-badge";
      dom.connectionBadge.textContent = "Refreshing";
      try {
        const [roomResponse, trackResponse] = await Promise.all([api.rooms(), api.tracks()]);
        rooms = roomResponse.rooms || [];
        tracks = trackResponse.tracks || [];
        rooms.forEach(updateRoomView);
        if (!selectedRoomId || !rooms.some((room) => room.id === selectedRoomId)) {
          selectedRoomId = rooms.find((room) => room.availability === "online")?.id || rooms[0]?.id || null;
        }
        renderRooms();
        renderPlayer();
        renderTracks();
        dom.connectionBadge.className = "connection-badge live";
        dom.connectionBadge.textContent = "REST connected";
      } catch (error) {
        dom.connectionBadge.className = "connection-badge error";
        dom.connectionBadge.textContent = "REST unavailable";
        handleError(error, "Could not load RoomPlay data.");
      }
    }

    function handleError(error, fallbackMessage) {
      if (error?.code === "COMMAND_TIMEOUT") {
        toast("Command outcome unknown. Room state was refreshed, not retried.", "warning");
        refreshAfterUnknownOutcome();
      } else {
        toast(error?.message ? `${error.code ? `${error.code}: ` : ""}${error.message}` : fallbackMessage, "error");
      }
    }

    async function refreshAfterUnknownOutcome() {
      try {
        await loadInitialSnapshots();
        if (selectedRoomId) {
          const detail = await api.room(selectedRoomId);
          mergeRoomSummary(detail);
          renderRooms();
          renderPlayer();
        }
      } catch (_) {
        // The original timeout and visible refresh warning remain authoritative.
      }
    }

    async function runMutation(key, operation, successMessage) {
      if (pending.has(key)) return;
      pending.add(key);
      setControlsDisabled(true);
      try {
        const result = await operation();
        if (result?.state) {
          const room = currentRoom();
          if (room) {
            room.state = result.state;
            room.state_stale = false;
            updateRoomView(room);
          }
        }
        if (successMessage) toast(successMessage, "success");
        renderRooms();
        renderPlayer();
        bindQueueRemoveButtons();
      } catch (error) {
        handleError(error, "The command failed.");
      } finally {
        pending.delete(key);
        setControlsDisabled(false);
        bindQueueRemoveButtons();
      }
    }

    function requireRoom() {
      const room = currentRoom();
      if (!room) {
        toast("Select a room first.", "warning");
        return null;
      }
      if (room.availability !== "online") {
        toast(`${room.name || room.id} is offline.`, "warning");
        return null;
      }
      return room;
    }

    function commandMutation(type, payload = {}) {
      const room = requireRoom();
      if (!room) return;
      const requestId = makeRequestId();
      runMutation(`command:${room.id}:${requestId}`, () => api.command(room.id, { request_id: requestId, type, payload }, requestId));
    }

    function playTrack(trackId) {
      commandMutation("playback.play", { track_id: trackId });
    }

    function addTrack(trackId) {
      const room = requireRoom();
      if (!room) return;
      const requestId = makeRequestId();
      runMutation(`queue-add:${room.id}:${requestId}`, () => api.queueAdd(room.id, { track_id: trackId }, requestId), "Added to queue.");
    }

    function removeQueueItem(itemId) {
      const room = requireRoom();
      if (!room) return;
      const requestId = makeRequestId();
      runMutation(`queue-remove:${room.id}:${requestId}`, () => api.queueRemove(room.id, itemId, requestId), "Queue item removed.");
    }

    function handlePrimaryControl() {
      const room = currentRoom();
      if (!room?.state) return;
      if (room.state.status === "playing") commandMutation("playback.pause");
      else if (room.state.status === "paused") commandMutation("playback.resume");
      else if (room.state.status === "stopped") {
        if (!room.state.current) {
          toast("Choose a track from the catalog first.", "warning");
        } else {
          commandMutation("playback.play", { track_id: room.state.current.track_id });
        }
      }
    }

    dom.primaryButton.addEventListener("click", handlePrimaryControl);
    dom.previousButton.addEventListener("click", () => commandMutation("playback.previous"));
    dom.nextButton.addEventListener("click", () => commandMutation("playback.next"));
    dom.stopButton.addEventListener("click", () => commandMutation("playback.stop"));
    dom.clearQueueButton.addEventListener("click", () => {
      const room = requireRoom();
      if (!room) return;
      const requestId = makeRequestId();
      runMutation(`queue-clear:${room.id}:${requestId}`, () => api.queueClear(room.id, requestId), "Queue cleared.");
    });
    dom.refreshRooms.addEventListener("click", loadInitialSnapshots);

    function setupEventSource() {
      if (mockMode || typeof EventSource === "undefined") return;
      eventSource = new EventSource("/api/v1/events");
      dom.connectionBadge.className = "connection-badge";
      dom.connectionBadge.textContent = "SSE connecting";

      eventSource.onopen = () => {
        dom.connectionBadge.className = "connection-badge live";
        dom.connectionBadge.textContent = "Live";
        loadInitialSnapshots();
      };
      eventSource.onerror = () => {
        dom.connectionBadge.className = "connection-badge error";
        dom.connectionBadge.textContent = "SSE disconnected";
        loadInitialSnapshots();
      };

      eventSource.addEventListener("room.state", (event) => {
        let payload;
        try { payload = JSON.parse(event.data); } catch { return; }
        const room = rooms.find((entry) => entry.id === payload.room_id);
        if (!room) return;
        const view = roomView.get(room.id);
        if (payload.instance_id && view?.instanceId && payload.instance_id !== view.instanceId) {
          if (view.instanceChanged) return;
          room.instance_id = payload.instance_id;
          view.instanceChanged = true;
        }
        if (view?.instanceId === payload.instance_id && Number(payload.state?.version || 0) < Number(view.version || 0)) return;
        room.instance_id = payload.instance_id;
        room.state = payload.state;
        room.state_stale = false;
        updateRoomView(room);
        renderRooms();
        renderPlayer();
        bindQueueRemoveButtons();
      });

      eventSource.addEventListener("room.availability", (event) => {
        let payload;
        try { payload = JSON.parse(event.data); } catch { return; }
        const room = rooms.find((entry) => entry.id === payload.room_id);
        if (!room) return;
        room.availability = payload.availability;
        if (payload.availability === "offline") room.state_stale = true;
        renderRooms();
        renderPlayer();
      });
    }

    setInterval(() => {
      const room = currentRoom();
      if (!room?.state) return;
      if (room.availability !== "online" || room.state_stale) return;
      const view = roomView.get(room.id);
      const position = statePosition(room.state, view?.receivedAt || Date.now());
      const track = room.state.current ? trackById(room.state.current.track_id) : null;
      const bar = dom.playerContent.querySelector(".progress-bar");
      const now = dom.playerContent.querySelector(".time-label");
      if (bar && track?.duration_ms) bar.style.width = `${Math.min(100, position / track.duration_ms * 100)}%`;
      if (now) now.textContent = `${formatDuration(position)} / ${track?.duration_ms ? formatDuration(track.duration_ms) : "--:--"}`;
    }, 1000);

    loadInitialSnapshots().finally(() => {
      setupEventSource();
      bindQueueRemoveButtons();
    });
  }

  if (typeof document !== "undefined") {
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", startApp);
    else startApp();
  }
})();
