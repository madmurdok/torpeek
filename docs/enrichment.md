# Knowing what a torrent is: names, metadata, faces

Nothing here is built. This is the record of an idea and of the decisions it
already obliges, written before any code so that the order of the work and the
reasons for it survive the gap between now and whenever it is picked up.

The idea, in the owner's words: a module that, alongside a fetch, tries to
learn who is in a film and what it is about from the torrent's own name — and
then grows into a library, so that typing *"films with Jolie and Pitt"* returns
the torrents and the frames already on disk. Plus a second question asked in
the same breath: can face recognition run on a local machine, or is the model
too heavy?

The short answers are yes and yes. The useful answer is that **the two ideas
are one idea, and doing them in the wrong order makes the second one look like
a failure.**

## Three steps, and why the order is the whole design

### 1. Parse the name. Offline.

`Lupin III - S03 E26 (1985) WEBRip 1080p x264 EAC3 ITA - Lullozzo` carries a
title, a year, a season, an episode, a source, a resolution, a codec, an audio
format and a release group. Getting them out is a solved problem with mature
solutions — `guessit` is the most complete, `parse-torrent-title` the most
embedded, `go-parse-torrent-name` the one in this language. Writing a fourth
would be the wrong instinct: these libraries are mature because torrent names
are a folklore rather than a format, and their value is the accumulated
exceptions.

This step needs no network, no API key and no third party, and it already
yields tags. That is why it is first: it is the only step that can be
tested against every name already in the results tree without asking anybody
for anything.

### 2. Enrich from a metadata service. With consent.

Title and year go to a service; cast, genres, keywords and cast photographs
come back. TMDB is the obvious candidate — free key, generous limits, and the
one every media manager already leans on, which means it has been tested
against millions of dirty names. OMDb and Wikidata are alternatives; Wikidata
is the only one that could be mirrored locally.

Cached beside the run record. The results tree is already keyed by infohash
and params, already survives being copied from a seedbox to a laptop
(TOR-60), and already holds `run.json`. A metadata sidecar there is durable
by construction, and **the library falls out of it for free**.

### 3. Faces. Last, and only against the cast step 2 named.

An embedding says *these two faces are the same person*. It never says *this
is Angelina Jolie*. Naming requires a gallery of labelled faces, and where
that gallery comes from decides whether the whole feature works:

- **Open-set** — match against all of humanity. Hard, unreliable, and needs a
  corpus nobody here has.
- **Closed-set** — verify *is this one of the fifteen people the metadata says
  are billed in this film*. Small, accurate, and the gallery is the cast
  photographs step 2 already fetched.

So step 3 is easy **only if step 2 exists**. Building faces first would
produce something that works badly, and it would look like the idea failing
rather than the order being wrong. That is the single most important sentence
in this document.

## What this obliges

### Sending a name to a third party is telling them what someone is watching

This project already has a position on this class of thing, and it is a
requirement rather than a preference. Acceptance criterion 5 —
REQUIREMENTS.md's list, item 5 — is that a `private: 1` torrent emits *not one*
DHT or PEX request, verified by tracing; TOR-129 gave a private torrent a
client of its own with DHT off specifically so that criterion stays true by
construction rather than by care.

Enrichment is the same shape one layer out: a lookup tells a third party what
this person is looking at, and a private torrent's name can itself identify
them. Nothing in criterion 5 covers an HTTP request to a metadata service —
which is exactly why the position has to be taken here deliberately, instead of
assumed to be inherited.

So it is opt-in, with a plain statement of what leaves the machine and to
whom — and the offline parse of step 1 must remain useful on its own, so that
declining costs the tags but not the feature. For a private torrent the
default should be the stricter one: criterion 5's whole point is that such a
torrent announces itself nowhere.

### A wrong answer is worse than no answer

Torrent names are dirty. A mis-parse names the wrong film, and a wrong cast
list presented as fact is worse than an empty one: it is confidently wrong, in
a place a person has no reason to doubt.

This project has drawn one line for exactly this, and `internal/web/listing.go`
keeps the tally rather than anybody's memory: six times over — TOR-119 for
claimed pieces, TOR-111 for reach, TOR-135 for availability, TOR-134 for rates,
TOR-147 on the progress event and TOR-136 at the listing. It applies here
unchanged: **absent is not zero, and a guess is not a fact.** A low-confidence
match reads as unknown, or as a candidate marked as one, never as an answer.

### The cgo constraint decides how a model ships

torpeek ships `CGO_ENABLED=0`, and that is load-bearing rather than
convenient: with cgo off, piece completion is bbolt rather than sqlite, and
`make check` tests that build specifically because it is the one that goes
out. Any ONNX runtime is cgo.

So a face model almost certainly does **not** link into the binary. It rides
beside it, the way ffmpeg already does — an optional companion the archive
carries and the main process invokes. That is a decision to take deliberately,
not a discovery to make half way through.

### Weight is not the obstacle

Worth stating plainly, because "too heavy for a laptop" is the intuition and
the numbers do not support it:

| part | size | cost |
| --- | --- | --- |
| detector (YuNet, SCRFD) | single-digit MB | milliseconds per image |
| embedding (MobileFaceNet) | ~4 MB | tens of ms per face, CPU |
| embedding (ArcFace / buffalo_l) | ~100–300 MB | tens of ms per face, CPU |

Against what already ships, read off the 1.2.0 packaging run rather than
recalled: the darwin-arm64 archive is 69.4 MiB packed and 156.5 MiB unpacked,
of which the torpeek binary is 30 MB — so roughly 126 MiB of it is bundled
ffmpeg and ffprobe, and the linux archive is larger again (108.1 MiB packed,
252.5 MiB unpacked). A whole face stack is smaller than the media tooling this
project already downloads and checksums. **The obstacle is labels, not
weight** — see step 3.

### The library needs no index worth the name

The search is over the results tree: tens or hundreds of entries on a normal
machine, not millions. A scan is enough; SQLite FTS is enough if a scan ever
is not. Nobody should reach for a search engine here, and this paragraph
exists so that nobody does.

### The ordinary failures are ordinary

All three of these are the normal case, not the exception, and each needs an
answer before code:

- the service is down or rate-limited — the parse still stands, the enrichment
  is absent, and absent reads as absent;
- the service answers with the **wrong film** — a confident wrong answer is the
  dangerous one, so a match needs a confidence and a way for a person to
  correct or clear it;
- the name is unparseable — a great many are, and the row must remain perfectly
  usable with no tags at all.

## What is deliberately not decided here

- Which parser. It wants measuring against the names actually in the results
  tree rather than choosing by reputation.
- Which service, and whether a local Wikidata mirror is worth the weight for
  someone who will not send a name anywhere.
- Whether the library is a view over the results tree or its own store. The
  tree is durable and copyable, which argues for a view; a store would let a
  person keep the library after clearing the frames.
- Whether faces are ever run automatically, or only when asked. Automatic
  means every frame set is analysed, which is CPU nobody asked for.

## Order of work, if this is picked up

1. Step 1 alone, measured against existing names. It is testable offline and
   it is the only part that can be finished without a decision about privacy.
2. The library over whatever step 1 yields — tags, and a search across the
   tree. This is where the feature becomes visible, and it still sends nothing
   anywhere.
3. Step 2, opt-in, cached in the tree.
4. Step 3, closed-set, against step 2's cast. Optional companion binary.

Steps 1 and 2 are `internal/enrich`. Step 3 is not: it is a separate process
with a separate build, and pretending otherwise inside one package would put a
cgo dependency where the whole project has decided it cannot go.
