# torpeek — архитектура

Дополняет [REQUIREMENTS.md](REQUIREMENTS.md): там зафиксировано *что* система делает,
здесь — *как* она устроена. Стек (Go + `anacrolix/torrent`) выбран в §6 требований.

## Контекст и задача

Достать N кадров из середины видеофайла, лежащего в торренте, не скачивая файл. Две
независимые механики, которые надо соединить: BitTorrent умеет отдавать произвольные куски,
а контейнер видео знает, какому таймкоду какой байт соответствует.

## Ключевое решение: контейнер разбирает ffprobe, а не мы

Изначально предполагалось написать парсеры MP4 `moov`, MKV `Cues` и AVI `idx1`. Проверка
показала, что это не нужно: `ffprobe` по запросу отдаёт **байтовую позицию** кейфрейма для
нужного момента времени —

```
$ ffprobe -select_streams v -show_entries packet=pts_time,pos,flags \
          -read_intervals 30%+#3 -of csv probe.mp4
packet,30.000000,234624,K__      # t=30.0s → байт 234624, keyframe
```

а `ffmpeg` умеет `http`/`https` и запрашивает диапазоны сам. Значит достаточно показать ему
файл по HTTP — и разбор контейнера, поиск индекса (включая `moov` в хвосте) и выбор
кейфрейма достаются бесплатно, для **всех** контейнеров, которые знает ffmpeg, а не для трёх.

**Цена решения:** какие именно байты читать, решает ffmpeg, а не мы. Управление трафиком
становится косвенным: мы ограничиваем его через `-probesize`/`-analyzeduration`, узкий
readahead и приоритеты кусков, но теоретического минимума не гарантируем. Если замеры не
уложатся в критерий «≤60 МБ в min-traffic» (§8.2 требований), точечный парсер под конкретный
контейнер остаётся открытой дверью — но пишется он тогда, когда доказан, а не заранее.

## Компоненты

```mermaid
graph TD
    CLI[cmd/torpeek<br/>флаги, выбор клиента]

    subgraph clients[Клиенты - только чтение событий]
        TUI[tui<br/>живой терминал]
        NDJSON[ndjson<br/>поток для кода]
        WEB[web<br/>сервер + вшитый фронтенд]
    end

    ENGINE[core.Engine<br/>оркестрация запуска,<br/>шина событий, бюджет]

    subgraph media[Работа с медиа - через мост]
        PROBE[probe<br/>обёртка ffprobe:<br/>дорожки, качество,<br/>позиция кейфрейма]
        FRAMES[frames<br/>планировщик точек,<br/>декод, отбраковка,<br/>сдвиг по доступности]
    end

    BRIDGE[bridge<br/>локальный HTTP Range-сервер<br/>поверх torrent-ридера]

    subgraph swarmpkg[swarm - обёртка anacrolix/torrent]
        SESSION[Session<br/>magnet/.torrent, BEP 9,<br/>private-флаг, отдача]
        AVAIL[Availability<br/>сумма битфилдов пиров]
        GOV[PieceGovernor<br/>приоритеты и окна]
        METER[Meter<br/>скачанные байты]
    end

    OUT[output<br/>кадры, лист, манифест,<br/>кеш, состояние запуска]
    FFMPEG([ffmpeg / ffprobe<br/>внешние процессы])

    CLI --> ENGINE
    CLI --> TUI & NDJSON & WEB
    ENGINE -.события.-> TUI & NDJSON & WEB
    ENGINE --> PROBE & FRAMES & OUT
    ENGINE --> SESSION
    PROBE & FRAMES --> FFMPEG
    FFMPEG -.HTTP Range.-> BRIDGE
    FRAMES --> AVAIL
    BRIDGE --> GOV
    GOV --> SESSION
    SESSION --> AVAIL & METER
    METER -.бюджет.-> ENGINE
```

Кто чем владеет:

- **`core`** — единственный, кто знает про запуск целиком: последовательность шагов, общий
  бюджет, параллелизм по файлам, отмена. Ничего не печатает (§3.1).
- **`bridge`** — переводит HTTP-запросы ffmpeg в чтения из торрента. Живёт ровно столько,
  сколько запуск; слушает `127.0.0.1` на порту из конфига (не случайном — §4.1).
- **`swarm`** — вся работа с anacrolix: сессия, приоритеты, доступность, счётчик байт.
  Единственное место, где импортируется `anacrolix/torrent`.
- **`probe` / `frames`** — единственные, кто запускает внешние процессы.
- **`output`** — единственный, кто пишет на диск.
- **клиенты** — только читают события, логики не содержат.

## Поток данных

```mermaid
sequenceDiagram
    participant E as core.Engine
    participant S as swarm.Session
    participant B as bridge
    participant P as probe (ffprobe)
    participant A as Availability
    participant F as frames (ffmpeg)
    participant O as output

    E->>S: добавить magnet
    S-->>E: метаданные (BEP 9), список файлов
    E-->>E: отобрать видеофайлы
    E->>B: поднять URL на файл
    E->>P: Inspect(url)
    P->>B: Range-запросы (заголовок, индекс)
    B->>S: приоритет кускам заголовка
    P-->>E: длительность, дорожки, качество
    E-->>E: план: 20 точек в 5-95%

    loop каждая точка
        E->>P: KeyframeAt(url, t)
        P-->>E: позиция байта, реальный pts
        E->>A: куски для этой позиции доступны?
        alt недоступны
            A-->>E: ближайшая доступная позиция
            E-->>E: сдвинуть t, пометить shifted
        end
        E->>F: Frame(url, t)
        F->>B: Range вокруг кейфрейма
        B->>S: приоритет, readahead по профилю
        F-->>E: кадр
        alt кадр пустой
            E->>F: соседний кейфрейм
        end
        E->>O: записать кадр
        E-->>E: событие frame_ready
    end

    E->>O: контактный лист + манифест
```

Существенная деталь: `KeyframeAt` вызывается **до** проверки доступности, а декод — после.
Позиция берётся из уже скачанного индекса и стоит почти ничего, а решение «сдвигаться или
нет» принимается до того, как потрачен трафик на само окно кадра.

## Жизненный цикл точки съёмки

Каждая из 20 точек проходит собственный автомат — здесь живёт вся логика сдвигов, из-за
которой набор кадров получается полным даже на дырявой раздаче:

```mermaid
stateDiagram-v2
    [*] --> Planned: планировщик разложил N точек

    Planned --> Locating: спросить позицию кейфрейма
    Locating --> Checking: известны pts и байт
    Locating --> Skipped: индекс не отвечает

    Checking --> Shifting: куски недоступны в сворме
    Checking --> Decoding: куски есть у пиров
    Shifting --> Checking: ближайшая доступная позиция
    Shifting --> Skipped: доступных позиций рядом нет

    Decoding --> Judging: кадр получен
    Decoding --> Widening: ffmpeg не смог декодировать
    Widening --> Decoding: расширить окно
    Widening --> Skipped: попытки исчерпаны

    Judging --> Stepping: кадр чёрный или однотонный
    Judging --> Done: кадр годный
    Stepping --> Decoding: соседний кейфрейм
    Stepping --> Done: попытки исчерпаны, берём как есть

    Done --> [*]
    Skipped --> [*]
```

Два разных сдвига, которые легко перепутать: **Shifting** — про сеть (куска нет у пиров,
двигаемся к доступному), **Stepping** — про картинку (кадр чёрный, двигаемся к соседнему
кейфрейму). Первый случается до траты трафика, второй — после. В манифест попадают оба,
но помечаются по-разному: `shifted` против `stepped`.

## Что и зачем скачивается

Ответ на главный вопрос — почему это дешевле, чем качать файл:

```
файл 10 ГБ
|-[ заголовок ]---------------------------------------------[ хвост ]-|
   moov / EBML                                              moov, если
   несколько МБ                                             без faststart
        |                                                        |
        +----- ffprobe: длительность, дорожки, качество ----------+

   20 окон вокруг кейфреймов, по одному на точку съёмки:
   [##]    [##]    [##]    [##]    [##]    [##]    [##]    [##]
---------------------------------------------------------------------
   5%                                                          95%

   каждое окно = 1-2 куска (кусок обычно 1-16 МБ)
   итого: заголовок + хвост + 20 окон ~ 50-150 МБ вместо 10 ГБ
```

Гранулярность задаёт торрент, а не мы: минимальная единица обмена — кусок, поэтому кадр
весом 200 КБ стоит как минимум один кусок. Отсюда и оценка в §7 требований, и то, почему
`min-traffic` экономит десятки процентов, а не порядки.

## Интерфейсы

Контракты между пакетами — достаточно конкретные, чтобы писать реализацию:

```go
// core: наружу — только события
type Event interface{ event() }

type MetadataReady struct{ Name string; Files []FileInfo }
type FileStarted   struct{ File int; Duration time.Duration; Media MediaInfo }
type FrameReady    struct{ File, Index int; Requested, Actual time.Duration; Path string; Shifted bool }
type BudgetWarning struct{ SpentBytes int64; LimitBytes int64; Elapsed, Limit time.Duration }
type FileDone      struct{ File int; Manifest string; Sheet string }
type Done          struct{ Reason StopReason } // completed | budget | cancelled
type Failed        struct{ Code ErrorCode; File int; Err error }

type Engine interface {
    Run(ctx context.Context, spec RunSpec) (<-chan Event, error)
}

// swarm: единственное место, знающее про anacrolix
type Session interface {
    Add(ctx context.Context, src Source) (Torrent, error)
    Close() error
}

type Torrent interface {
    Files() []FileInfo
    Reader(file int, profile Profile) io.ReadSeekCloser // profile задаёт readahead
    Availability() Availability
    Downloaded() int64
    Private() bool
}

type Availability interface {
    AtByte(file int, off int64) int              // сколько пиров держат этот кусок
    NearestAvailable(file int, off int64) int64  // ближайшее доступное смещение
    Map(file int, buckets int) []int             // для манифеста и TUI
}

// bridge: показывает файл торрента как обычный HTTP-ресурс
type Bridge interface {
    Publish(t Torrent, file int, profile Profile) (url string, release func())
}

// probe: обёртка ffprobe
type Prober interface {
    Inspect(url string) (MediaInfo, error)                    // длительность, дорожки, качество
    KeyframeAt(url string, at time.Duration) (Keyframe, error) // pts + байтовая позиция
}

// frames
type Extractor interface {
    Frame(url string, at time.Duration) (data []byte, actual time.Duration, err error)
}
```

`Profile` — то, чем различаются `min-time` и `min-traffic`: размер readahead, ширина окна
вокруг кейфрейма, `probesize`/`analyzeduration` для ffmpeg, число параллельных чтений.
Один тип, две константы — вместо двух веток кода по всему конвейеру.

## Данные на диске

```
<cache>/<infohash>/<paramsHash>/
├── run.json                    # состояние запуска: план точек и их статусы
├── <file-slug>/
│   ├── frames/000.jpg …        # кадры, оригинальное разрешение
│   ├── sheet.jpg               # контактный лист
│   └── manifest.json           # §2.8 требований
```

`run.json` — источник истины и для возобновления, и для кеша: точка со статусом `done` не
переснимается, а полностью готовый набор означает попадание в кеш и ноль сетевых запросов.
`paramsHash` покрывает только те параметры, которые меняют результат (число точек, окно,
профиль), но не бюджеты и не параллелизм.

## Отказы и деградация

| Что случилось | Поведение |
|---|---|
| Метаданные не пришли за таймаут | `Failed{no_metadata}`, ничего не скачано |
| Ноль пиров | `Failed{no_peers}` с числом сидов из трекера |
| Куски точки недоступны | сдвиг к ближайшему доступному, пометка `shifted` (§2.3) |
| Весь файл недоступен | файл пропускается, остальные обрабатываются |
| ffprobe не определил длительность | `Failed{unprobeable}`; с флагом — последовательный режим (§2.7) |
| ffmpeg упал на точке | повтор с расширенным окном, затем точка помечается пропущенной |
| Запрос к мосту не дождался кусков | таймаут запроса, чтобы ffmpeg не висел вечно; сам факт таймаута мост запоминает |
| Чтение через мост оборвалось по таймауту | `read_stalled`, а не `unprobeable`: ffprobe судит о файле, который недочитал, и его вердикт («нет длительности», «нет ключевого кадра с позицией») ничем не отличается от настоящей поломки контейнера. Знает об обрыве только мост, поэтому код берётся оттуда, и последовательный режим (§2.7) такой ошибкой не открывается |
| Бюджет исчерпан | корректная остановка, готовое сохранено, причина в манифесте (§2.6) |
| Отмена | то же самое; `run.json` позволяет добрать остаток (§2.10) |

Мост — единственное место, где чужой процесс может подвесить наш: у каждого запроса свой
таймаут и свой контекст, отменяемый вместе с запуском.

## Риски

1. **ffmpeg читает больше, чем нужно.** Главный риск выбранного подхода. Митигация:
   `-probesize`/`-analyzeduration`, `-ss` перед `-i` (быстрый seek), узкий readahead.
   Проверяется замером против критерия §8.2 — до того, как писать что-то поверх.
2. **Порт моста.** На управляемом хостинге (§4.1) даже localhost-порт должен быть из
   выделенного диапазона — значит порт моста настраиваемый, а не случайный.
3. **Приоритеты вместо дедлайнов.** У anacrolix нет piece deadline: точка, ждущая редкий
   кусок, может задержать соседние. Митигация — таймаут на точку и сдвиг.
4. **Параллелизм по файлам множит процессы ffmpeg.** На shared-слоте это бьёт по CPU
   и IO соседей; дефолт там 1–2 (§4.1).
