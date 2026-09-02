// The whole UI: submit a magnet or drop a .torrent, watch the events arrive,
// watch the frames land. It reads the event stream and does nothing else -
// the run lives in the core, and a client that started deciding things would
// be a second place where the run is defined (ARCHITECTURE.md).
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
  dropzone: document.getElementById("dropzone"),
  fileInput: document.getElementById("file-input"),
  dropOverlay: document.getElementById("drop-overlay"),
  torrentSummary: document.getElementById("torrent-summary"),
  files: document.getElementById("files"),
  log: document.getElementById("log"),
  lightbox: document.getElementById("lightbox"),
  lightboxImg: document.getElementById("lightbox-img"),
  lightboxCaption: document.getElementById("lightbox-caption"),
  lightboxClose: document.getElementById("lightbox-close"),
};

// state.files maps a torrent's file index to the DOM for that file's summary
// panel and frame grid, built the first time file_started names it.
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
  el.files.replaceChildren();
  el.log.textContent = "";
  el.torrentSummary.hidden = true;
  el.torrentSummary.textContent = "";
  state.files.clear();
  showError("");
  if (el.lightbox.open) el.lightbox.close();
}

function seconds(ms) {
  if (!ms && ms !== 0) return "";
  return (ms / 1000).toFixed(1) + "s";
}

function bitrateLabel(bps) {
  if (!bps) return "";
  if (bps >= 1e6) return (bps / 1e6).toFixed(1) + " Mbps";
  if (bps >= 1e3) return Math.round(bps / 1e3) + " kbps";
  return bps + " bps";
}

function bytesLabel(n) {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = n, i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return (i === 0 ? value : value.toFixed(1)) + " " + units[i];
}

function channelsLabel(n) {
  switch (n) {
    case 1: return "mono";
    case 2: return "stereo";
    case 6: return "5.1";
    case 8: return "7.1";
    default: return n ? n + " ch" : "";
  }
}

function langLabel(code) {
  return code && code !== "und" ? code : "unknown language";
}

// fileBlock returns the section for one torrent file, building it the first
// time it is needed. Each video file gets its own summary panel and its own
// frame grid, since a multi-file torrent should not mix their frames or
// their tracks in one place.
function fileBlock(index) {
  let entry = state.files.get(index);
  if (entry) return entry;

  const article = document.createElement("article");
  article.className = "file";
  article.innerHTML =
    '<header class="file-header">' +
      '<h2 class="file-title"></h2>' +
      '<dl class="specs"></dl>' +
      '<div class="tracks"></div>' +
      '<p class="file-links" hidden></p>' +
    "</header>" +
    '<p class="file-progress" hidden></p>' +
    '<div class="grid"></div>';
  el.files.append(article);

  entry = {
    article,
    title: article.querySelector(".file-title"),
    specs: article.querySelector(".specs"),
    tracks: article.querySelector(".tracks"),
    links: article.querySelector(".file-links"),
    progress: article.querySelector(".file-progress"),
    grid: article.querySelector(".grid"),
  };
  state.files.set(index, entry);
  return entry;
}

function addSpec(dl, label, value) {
  const dt = document.createElement("dt");
  dt.textContent = label;
  const dd = document.createElement("dd");
  dd.textContent = value || "unknown";
  dl.append(dt, dd);
}

function audioLine(t) {
  const parts = [langLabel(t.language), t.codec, channelsLabel(t.channels), bitrateLabel(t.bitrate)];
  if (t.title) parts.push('"' + t.title + '"');
  if (t.default) parts.push("default");
  return parts.filter(Boolean).join(" · ");
}

function subtitleLine(t) {
  const parts = [langLabel(t.language), t.codec];
  if (t.title) parts.push('"' + t.title + '"');
  if (t.forced) parts.push("forced");
  if (t.default) parts.push("default");
  return parts.filter(Boolean).join(" · ");
}

function trackGroup(label, tracks, formatter) {
  const section = document.createElement("div");
  section.className = "track-group";
  const h3 = document.createElement("h3");
  h3.textContent = tracks.length ? label : label + " — none";
  section.append(h3);
  if (tracks.length) {
    const ul = document.createElement("ul");
    for (const t of tracks) {
      const li = document.createElement("li");
      li.textContent = formatter(t);
      ul.append(li);
    }
    section.append(ul);
  }
  return section;
}

// The summary panel this task requires: audio tracks, subtitles, bitrate,
// resolution, filled the moment the file's media is known - before a single
// frame exists.
function onFileStarted(ev) {
  const entry = fileBlock(ev.file);
  entry.title.textContent = ev.path;

  entry.specs.replaceChildren();
  addSpec(entry.specs, "Resolution", ev.width && ev.height ? ev.width + "×" + ev.height : "");
  addSpec(entry.specs, "Video",
    [ev.codec, ev.profile, ev.fps ? ev.fps.toFixed(2) + " fps" : "", bitrateLabel(ev.video_bitrate)]
      .filter(Boolean).join(" · "));
  addSpec(entry.specs, "Overall bitrate", bitrateLabel(ev.bitrate));
  addSpec(entry.specs, "Duration", seconds(ev.duration_ms));

  entry.tracks.replaceChildren(
    trackGroup("Audio", ev.audio || [], audioLine),
    trackGroup("Subtitles", ev.subtitles || [], subtitleLine),
  );

  log("file " + ev.file + ": " + ev.path + " — " + seconds(ev.duration_ms) +
      ", " + ev.width + "x" + ev.height + " " + ev.codec + ", " + ev.planned + " points");
}

// A deliberately plain grid grew here in TOR-24 as proof that a frame reaches
// the browser the moment it is written. This replaces it with the real
// screen: frames grouped by file, click for full size.
function addFrame(ev) {
  const entry = fileBlock(ev.file);

  const figure = document.createElement("figure");
  figure.tabIndex = 0;
  figure.className = "thumb";

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
  entry.grid.append(figure);

  const open = () => openLightbox(img.src, caption.textContent);
  figure.addEventListener("click", open);
  figure.addEventListener("keydown", (event) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      open();
    }
  });

  log("frame " + ev.index + " at " + seconds(ev.actual_ms) + (ev.shift ? " (" + ev.shift + ")" : ""));
}

function openLightbox(src, caption) {
  el.lightboxImg.src = src;
  el.lightboxCaption.textContent = caption;
  el.lightbox.showModal();
}

el.lightboxClose.addEventListener("click", () => el.lightbox.close());
el.lightbox.addEventListener("click", (event) => {
  // A click that lands on the dialog element itself, rather than anything
  // inside it, is a click on the backdrop.
  if (event.target === el.lightbox) el.lightbox.close();
});

function onFileDone(ev) {
  const entry = fileBlock(ev.file);
  entry.progress.hidden = true;

  const links = [];
  if (ev.sheet_url) links.push(link(ev.sheet_url, "contact sheet"));
  if (ev.manifest_url) links.push(link(ev.manifest_url, "manifest"));
  if (links.length) {
    entry.links.replaceChildren(...links);
    entry.links.hidden = false;
  }

  log("file " + ev.file + " done: " + ev.frames + " frames, " + ev.skipped + " skipped");
}

function link(href, text) {
  const a = document.createElement("a");
  a.href = url(href);
  a.textContent = text;
  a.target = "_blank";
  a.rel = "noopener";
  return a;
}

function apply(ev) {
  switch (ev.type) {
    case "run_state":
      if (ev.reset) clearRun();
      setActive(ev.active);
      if (ev.source) el.source.value = ev.source;
      break;

    case "metadata_ready":
      el.torrentSummary.hidden = false;
      el.torrentSummary.textContent =
        ev.name + " — " + ev.selected.length + " of " + ev.videos + " video file(s) selected";
      log("metadata: " + ev.name + " (" + ev.infohash + ")");
      break;

    case "file_started":
      onFileStarted(ev);
      break;

    case "frame_ready":
      addFrame(ev);
      break;

    case "frame_skipped":
      log("frame " + ev.index + " skipped: " + ev.code + " " + ev.reason);
      break;

    case "progress": {
      const entry = fileBlock(ev.file);
      entry.progress.hidden = false;
      entry.progress.textContent =
        ev.frames_done + " / " + ev.frames_total + " frames · " +
        bytesLabel(ev.downloaded) + " downloaded · " + ev.peers + " peer(s)";
      log("progress: " + ev.frames_done + "/" + ev.frames_total +
          ", " + ev.downloaded + " bytes, " + ev.peers + " peers");
      break;
    }

    case "budget_warning":
      log("warning: " + ev.spent + " of " + ev.limit + " bytes used");
      break;

    case "file_done":
      onFileDone(ev);
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

// A dropped .torrent is bytes, not a string, so it takes a different request
// than the magnet path - but both end at the same server-side StartRun, and
// from here on the run is indistinguishable from one started by pasting a
// magnet: the same events, the same screens.
async function uploadTorrent(file) {
  showError("");
  const body = new FormData();
  body.append("torrent", file);
  body.append("mode", el.mode.value);
  try {
    const response = await fetch(url("runs/upload"), { method: "POST", body });
    if (!response.ok) {
      const detail = await response.json().catch(() => ({}));
      throw new Error(detail.error || response.statusText);
    }
  } catch (err) {
    showError(String(err.message || err));
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

el.fileInput.addEventListener("change", async () => {
  const file = el.fileInput.files[0];
  el.fileInput.value = ""; // lets the same file be picked again later
  if (file) await uploadTorrent(file);
});

// Drag-and-drop works anywhere on the page, not only over the small input
// row - a stranger dragging a .torrent in from Finder or Explorer should not
// have to find a pixel-precise target first. A depth counter is what makes
// dragenter/dragleave over child elements not flicker the overlay off.
function isFileDrag(event) {
  return event.dataTransfer && Array.from(event.dataTransfer.types || []).includes("Files");
}

let dragDepth = 0;

document.addEventListener("dragenter", (event) => {
  if (!isFileDrag(event)) return;
  dragDepth++;
  el.dropOverlay.hidden = false;
});

document.addEventListener("dragleave", () => {
  dragDepth = Math.max(0, dragDepth - 1);
  if (dragDepth === 0) el.dropOverlay.hidden = true;
});

document.addEventListener("dragover", (event) => {
  if (isFileDrag(event)) event.preventDefault();
});

document.addEventListener("drop", async (event) => {
  if (!isFileDrag(event)) return;
  event.preventDefault();
  dragDepth = 0;
  el.dropOverlay.hidden = true;
  const file = event.dataTransfer.files[0];
  if (file) await uploadTorrent(file);
});

setActive(false);
connect();
