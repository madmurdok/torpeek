// The whole UI: submit a source, watch the events arrive, watch the frames
// land. It reads the event stream and does nothing else - the run lives in
// the core, and a client that started deciding things would be a second place
// where the run is defined (ARCHITECTURE.md).
//
// Every URL is resolved against document.baseURI rather than written from the
// site root, so the page works unchanged under /torpeek behind a proxy.

const el = {
  status: document.getElementById("status"),
  form: document.getElementById("start"),
  source: document.getElementById("source"),
  mode: document.getElementById("mode"),
  go: document.getElementById("go"),
  cancel: document.getElementById("cancel"),
  error: document.getElementById("error"),
  summary: document.getElementById("summary"),
  frames: document.getElementById("frames"),
  log: document.getElementById("log"),
};

const state = { active: false, files: new Map() };

function url(path) {
  return new URL(path, document.baseURI);
}

function setStatus(text, kind) {
  el.status.textContent = text;
  el.status.dataset.state = kind;
}

function setActive(active) {
  state.active = active;
  el.go.disabled = active;
  el.cancel.disabled = !active;
}

function showError(message) {
  el.error.textContent = message || "";
  el.error.hidden = !message;
}

function log(line) {
  el.log.textContent += line + "\n";
  el.log.scrollTop = el.log.scrollHeight;
}

function clearRun() {
  el.frames.replaceChildren();
  el.log.textContent = "";
  el.summary.textContent = "";
  state.files.clear();
  showError("");
}

function seconds(ms) {
  return (ms / 1000).toFixed(1) + "s";
}

// A deliberately plain grid: proof that a frame reaches the browser the moment
// it is written. The real screens - drag and drop, click for full size, the
// track summary panel - are TOR-25's, and are meant to replace this, not to
// grow out of it.
function addFrame(ev) {
  const figure = document.createElement("figure");

  const img = document.createElement("img");
  img.src = url(ev.url);
  img.alt = "frame " + ev.index;
  img.loading = "lazy";

  const caption = document.createElement("figcaption");
  caption.textContent = "#" + ev.index + "  " + seconds(ev.actual_ms);
  if (ev.shift) {
    caption.append(" ");
    const note = document.createElement("span");
    note.className = "shifted";
    note.textContent = ev.shift;
    caption.append(note);
  }

  figure.append(img, caption);
  el.frames.append(figure);
}

function apply(ev) {
  switch (ev.type) {
    case "run_state":
      if (ev.reset) clearRun();
      setActive(ev.active);
      if (ev.source) el.source.value = ev.source;
      break;

    case "metadata_ready":
      el.summary.textContent =
        ev.name + " — " + ev.videos + " video file(s), " + ev.selected.length + " selected";
      log("metadata: " + ev.name + " (" + ev.infohash + ")");
      break;

    case "file_started":
      log("file " + ev.file + ": " + ev.path + " — " + seconds(ev.duration_ms) +
          ", " + ev.width + "x" + ev.height + " " + ev.codec +
          ", " + ev.planned + " points");
      break;

    case "frame_ready":
      addFrame(ev);
      log("frame " + ev.index + " at " + seconds(ev.actual_ms) + (ev.shift ? " (" + ev.shift + ")" : ""));
      break;

    case "frame_skipped":
      log("frame " + ev.index + " skipped: " + ev.code + " " + ev.reason);
      break;

    case "progress":
      log("progress: " + ev.frames_done + "/" + ev.frames_total +
          ", " + ev.downloaded + " bytes, " + ev.peers + " peers");
      break;

    case "budget_warning":
      log("warning: " + ev.spent + " of " + ev.limit + " bytes used");
      break;

    case "file_done":
      log("file " + ev.file + " done: " + ev.frames + " frames, " + ev.skipped + " skipped");
      break;

    case "done":
      log("done: " + ev.reason + ", " + ev.frames + " frames from " + ev.files +
          " file(s), " + ev.downloaded + " bytes in " + seconds(ev.elapsed_ms));
      break;

    case "failed":
      showError(ev.code + ": " + ev.error);
      log("failed: " + ev.code + " " + ev.error);
      break;
  }
}

// The socket carries events only. Reconnecting replays the run from the start,
// so a dropped connection costs nothing but a redraw.
function connect() {
  const address = url("events");
  address.protocol = address.protocol === "https:" ? "wss:" : "ws:";

  const socket = new WebSocket(address);

  socket.onopen = () => setStatus("live", "live");
  socket.onclose = () => {
    setStatus("reconnecting", "lost");
    setTimeout(connect, 1000);
  };
  socket.onmessage = (message) => {
    try {
      apply(JSON.parse(message.data));
    } catch (err) {
      log("unreadable event: " + err);
    }
  };
}

async function post(path, body) {
  const response = await fetch(url(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
  if (!response.ok) {
    const detail = await response.json().catch(() => ({}));
    throw new Error(detail.error || response.statusText);
  }
}

el.form.addEventListener("submit", async (event) => {
  event.preventDefault();
  showError("");
  try {
    await post("runs", { source: el.source.value, mode: el.mode.value });
  } catch (err) {
    showError(String(err.message || err));
  }
});

el.cancel.addEventListener("click", async () => {
  try {
    await post("runs/cancel");
  } catch (err) {
    showError(String(err.message || err));
  }
});

setActive(false);
connect();
