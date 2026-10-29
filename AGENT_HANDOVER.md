# Semantic Navigator — Полное руководство по архитектуре и кодовой базе (Agent Handover)

> **Назначение документа:** Полное, детальное описание устройства проекта, архитектурных решений, внутренних протоколов, структуры директорий и ключевых компонентов для разработчиков и AI-агентов, продолжающих развитие проекта.

---

## 1. Контекст и бизнес-цель проекта

- **Код проекта:** P2609-382 «Семантический навигатор».
- **Заказчик/Партнёр:** Контур.
- **Проблема:** Разработчикам часто нужно искать код по смыслу («где у нас проверяется JWT токен», «как сохраняется счет на оплату», «расчет налога по ставке НДС»), но обычный grep/текстовый поиск бессилен, если точные имена методов неизвестны или запрос сформулирован на русском языке.
- **Продукт:** Расширение для VS Code, обеспечивающее семантический поиск по открытому рабочему пространству (локально на машине разработчика) с использованием гибридного ранжирования (векторы + BM25) и бесшовной интеграцией в UI редактора.
- **Лицензия:** MIT License.

---

## 2. Общая архитектура системы

Проект разделен на два изолированных слоя:
1. **Frontend (VS Code Extension):** Написан на **TypeScript**. Отвечает за взаимодействие с редактором, QuickPick, боковую панель, статус-бар и настройки.
2. **Backend Engine (Поисковый движок):** Написан на **Go**. Отвечает за обход файлов, AST-чанкинг кода, получение эмбеддингов от внешнего API, хранение индекса в SQLite и гибридный поиск (RRF).

### Канал взаимодействия: stdio JSON-RPC 2.0
- Расширение запускает Go-бинарник как дочерний процесс через `child_process.spawn`.
- Обмен сообщениями идет через **`stdin`** (входящие запросы) и **`stdout`** (ответы и нотификации) в формате **строк JSON, разделенных `\n`**.
- **Критическое правило:** Все логи и отладочная информация движка пишутся **исключительно в `stderr`**, чтобы поток `stdout` оставался на 100% валидным JSON-RPC каналом.

```
┌────────────────────────────────────────────────────────┐
│                   VS Code Extension                    │
│  - QuickPick Search (Cmd+Alt+F)                        │
│  - Sidebar TreeView                                    │
│  - Status Bar Progress Indicator                       │
└───────────────────────────▲────────────────────────────┘
                            │ JSON-RPC 2.0 (stdio)
                            │ \n-delimited JSON
┌───────────────────────────▼────────────────────────────┐
│                    Go Search Engine                    │
│                                                        │
│  ┌──────────────────┐    ┌──────────────────────────┐  │
│  │  Language-Aware  │    │      Embeddings Client   │  │
│  │      Chunker     │    │   (OpenAI-compatible)    │  │
│  └────────┬─────────┘    └────────────┬─────────────┘  │
│           │                           │                │
│           ▼                           ▼                │
│  ┌──────────────────────────────────────────────────┐  │
│  │                  SQLite Storage                  │  │
│  │   - Files (SHA-256 incremental hashes)           │  │
│  │   - Chunks (code, metadata, float32 vectors)     │  │
│  │   - FTS5 Virtual Table (BM25 lexical index)      │  │
│  └────────────────────────┬─────────────────────────┘  │
│                           │                            │
│                           ▼                            │
│  ┌──────────────────────────────────────────────────┐  │
│  │              Hybrid Searcher (RRF)               │  │
│  │   RRF = W_vec/(k + rank_vec) + W_bm25/(k + rank) │  │
│  └──────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────┘
```

---

## 3. Спецификация протокола JSON-RPC 2.0

Реализована в [`engine/internal/protocol/jsonrpc.go`](file:///Users/vladislav/Documents/SemanticNavigator/engine/internal/protocol/jsonrpc.go) и [`extension/src/types.ts`](file:///Users/vladislav/Documents/SemanticNavigator/extension/src/types.ts).

### 3.1. Запрос `initialize`
Отправляется клиентом сразу после запуска процесса:
```json
// Request
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "workspacePath": "/Users/.../MyProject",
    "storagePath": "/Users/.../Library/Application Support/Code/User/workspaceStorage/...",
    "provider": {
      "type": "openai-compatible",
      "baseUrl": "https://api.openai.com/v1",
      "apiKey": "sk-...",
      "model": "text-embedding-3-small"
    }
  }
}

// Response
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "engineVersion": "0.1.0",
    "supportedLanguages": ["csharp", "typescript", "javascript", "go", "python"],
    "storagePath": "/path/to/semantic_index.db"
  }
}
```

### 3.2. Запрос `index/start`
Запускает фоновое сканирование и индексацию рабочей области:
```json
// Request
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "index/start",
  "params": {
    "forceReindex": false
  }
}

// Response (мгновенный, так как индексация идет в фоновом воркере)
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "status": "started",
    "message": "Indexing started in background"
  }
}
```

### 3.3. Нотификация `index/progress` (Server -> Client)
Одностороннее уведомление о прогрессе (без `id`):
```json
{
  "jsonrpc": "2.0",
  "method": "index/progress",
  "params": {
    "phase": "embedding", // "scanning" | "chunking" | "embedding" | "done" | "error"
    "totalFiles": 45,
    "processedFiles": 20,
    "totalChunks": 180,
    "processedChunks": 80,
    "currentFile": "src/BillingService.cs",
    "percentage": 44.4,
    "message": "Indexed src/BillingService.cs"
  }
}
```

### 3.4. Запрос `search`
Выполняет гибридный поиск по запросу пользователя:
```json
// Request
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "search",
  "params": {
    "query": "валидация токена пользователя",
    "topK": 10
  }
}

// Response
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "query": "валидация токена пользователя",
    "durationMs": 42,
    "matches": [
      {
        "filePath": "/Users/.../src/AuthService.cs",
        "relativePath": "src/AuthService.cs",
        "startLine": 25,
        "endLine": 45,
        "snippet": "public bool ValidateToken(string jwtToken) { ... }",
        "symbolName": "ValidateToken",
        "score": 0.0312,
        "matchReason": "semantic (vector)" // "semantic (vector)" | "exact keyword (BM25)" | "hybrid"
      }
    ]
  }
}
```

### 3.5. Запрос `status`
Возвращает текущее состояние движка:
```json
// Response
{
  "state": "ready", // "idle" | "indexing" | "ready" | "error"
  "indexedFiles": 45,
  "indexedChunks": 180,
  "lastError": ""
}
```

---

## 4. Устройство поискового движка на Go (`engine/`)

### Структура пакетов:
```
engine/
├── cmd/
│   └── engine/
│       └── main.go           # Точка входа: stdio JSON-RPC цикл, роутинг, фоновая индексация
├── internal/
│   ├── protocol/
│   │   ├── jsonrpc.go        # Спецификация JSON-RPC 2.0 (Request, Response, Error, типы)
│   │   └── jsonrpc_test.go   # Тесты сериализации
│   ├── provider/
│   │   ├── openai.go         # HTTP-клиент к OpenAI-compatible API эмбеддингов
│   │   └── openai_test.go    # In-memory тесты мок-транспорта
│   ├── storage/
│   │   ├── sqlite.go         # Хранилище SQLite: таблицы, BLOB векторов, FTS5 BM25
│   │   └── sqlite_test.go    # Тесты БД во временной директории
│   ├── chunker/
│   │   ├── chunker.go        # Нарезка C#, TS, Go, XML Doc комментарии, лимит токенов
│   │   └── chunker_test.go   # Тесты извлечения методов и docstrings
│   └── search/
│       ├── vector.go         # Косинусное сходство векторов
│       ├── hybrid.go         # Reciprocal Rank Fusion (слияние рангов векторов и BM25)
│       └── search_test.go    # Тесты RRF и сходства
├── go.mod
└── go.sum
```

### Ключевые детали реализации движка:

1. **Pure-Go SQLite (`modernc.org/sqlite`):**
   - Используется pure-Go реализация SQLite без CGO (`github.com/mattn/go-sqlite3` намеренно отвергнут).
   - **Преимущество:** Бинарник движка компилируется нативно под любую ОС/архитектуру (macOS arm64/x64, Linux, Windows) без необходимости внешних GCC/Clang компиляторов.
   - База данных размещается в `semantic_index.db` внутри `storagePath`, предоставляемого VS Code.

2. **Векторное хранилище и FTS5:**
   - Таблица `chunks`: вектор эмбеддинга (`[]float32`) сериализуется в бинарный `BLOB` (IEEE-754 little-endian, 4 байта на число). Это дает компактный размер и мгновенную распаковку без JSON-оверхеда.
   - Таблица `chunks_fts`: виртуальная таблица SQLite **FTS5** с токенизатором `unicode61`. Позволяет вычислять нативный `bm25(chunks_fts, 10.0, 5.0, 1.0)` с весами: имя метода — 10.0, контекст неймспейса/класса — 5.0, тело — 1.0.

3. **Инкрементальная индексация:**
   - Таблица `files` хранит SHA-256 хеш содержимого файла.
   - При индексации файл читается, вычисляется SHA-256. Если хеш совпадает с БД — файл пропускается. Запросы к API эмбеддингов отправляются только для новых или измененных файлов.

4. **Language-aware чанкер (`chunker.go`):**
   - Для C# (основной фокус Контура) чанкер извлекает неймспейс, класс, методы, сигнатуры и XML doc-комментарии (`/// <summary>`).
   - Формирует обогащенный контекст: `// File: Service.cs\n// Context: Kontur.Billing > PaymentProcessor > ProcessPayment\n// Doc: Проверяет срок действия...\n<code>`.
   - `EnforceTokenBudget`: ограничивает размер текста чанка (по умолчанию 1024 токена), аккуратно усекая тело метода, но сохраняя сигнатуру и контекст.
   - Для остальных файлов используется скользящее окно с перекрытием (sliding window fallback).

5. **Гибридный поиск RRF (`hybrid.go`):**
   - Слияние рангов векторного поиска (Dense) и нативного SQLite BM25 (Sparse):
     $$RRF(d) = \frac{W_{vec}}{k + \text{rank}_{vec}} + \frac{W_{bm25}}{k + \text{rank}_{bm25}}$$
   - Позволяет находить как семантически похожий код (даже если слова не совпадают), так и точные совпадения идентификаторов (`ValidateJwtToken`), выводя их в топ выдачи.

---

## 5. Устройство VS Code расширения (`extension/`)

### Структура исходников:
```
extension/
├── src/
│   ├── extension.ts      # Активация/деактивация, регистрация команд, связь компонентов
│   ├── engineClient.ts   # Жизненный цикл Go-процесса, stdio JSON-RPC клиент
│   ├── statusBar.ts      # StatusBarManager: фоновая индикация без блокировки редактора
│   ├── quickPick.ts      # QuickPickSearch: быстрый клавиатурный поиск и Live Preview
│   ├── resultsView.ts    # TreeDataProvider: постоянная боковая панель результатов
│   └── types.ts          # Интерфейсы протокола JSON-RPC 2.0
├── package.json          # Манифест расширения, настройки, команды, шорткаты
├── tsconfig.json         # Strict TypeScript конфигурация
└── out/                  # Скомпилированный JavaScript
```

### Ключевые компоненты UI/UX:

1. **`engineClient.ts`:**
   - Находит бинарник: проверяет настройку `semanticNavigator.enginePath`, затем `engine/bin/semantic-engine`.
   - Поддерживает мапу `pendingRequests: Map<number, {resolve, reject}>`. При получении строки ответа сопоставляет её по `id`.
   - Перехватывает `index/progress` нотификации и передает их подписчикам.
   - Логи `stderr` пишет в OutputChannel `Semantic Navigator`.

2. **`statusBar.ts` (Ненавязчивая индикация):**
   - Правый нижний угол редактора.
   - При индексации: вращающаяся иконка и процент `$(sync~spin) Indexing: 45%`. В тултипе имя текущего файла. Редактор **не блокируется**.
   - В готовом состоянии: `$(check) Index ready (180 chunks)`. Клик по статус-бару открывает семантический поиск.

3. **`quickPick.ts` (Быстрый поиск с клавиатуры):**
   - Вызывается по шорткату `Cmd+Alt+F` (macOS) / `Ctrl+Alt+F` (Windows/Linux).
   - Задержка ввода (debounce 350 мс).
   - **Live Preview при перемещении стрелками:** когда пользователь идет по результатам клавишами `Вверх`/`Вниз`, редактор в фоне открывает документ на нужной строке в режиме превью (`preview: true, preserveFocus: true`).
   - Нажатие `Enter` фиксирует выбор и центрирует курсор на диапазоне строк.

4. **`resultsView.ts` (Боковая панель):**
   - `SearchResultsProvider` реализует `vscode.TreeDataProvider`.
   - Сохраняет результаты последнего поиска в Activity Bar, чтобы разработчик мог работать с кодом и возвращаться к найденным методам мышью.

---

## 6. Сборка, тестирование и запуск

### 6.1. Единый скрипт сборки
Из корня репозитория:
```bash
./scripts/build.sh
```
Скрипт собирает:
1. Go-бинарник в `engine/bin/semantic-engine`.
2. TypeScript расширение в `extension/out/`.

### 6.2. Запуск тестов Go-движка
```bash
cd engine
go test -v ./...
```
Все пакеты покрыты модульными тестами (JSON-RPC, Chunker, OpenAI-клиент с in-memory моком, SQLite с FTS5 BM25, Hybrid RRF).

### 6.3. Запуск расширения в режиме отладки
1. Откройте корень репозитория в VS Code.
2. Откройте файл `extension/src/extension.ts`.
3. Нажмите `F5` (Launch Extension). Откроется окно `[Extension Development Host]`.
4. В новом окне откройте тестовый C#/TS проект.
5. Нажмите `Cmd+Alt+F` для семантического поиска.

---

## 7. Вопросы экспертов и архитектурные развилки (для встречи)

Детальный бриф зафиксирован в файле [`CONSULTATION_BRIEF.md`](file:///Users/vladislav/Documents/SemanticNavigator/CONSULTATION_BRIEF.md).

### Кратко по темам экспертов:
1. **ML / IR эксперт:**
   - **Кросс-языковой поиск (RU → EN код):** Достаточно ли мультиязычной семантики модели эмбеддингов или нужен шаг HyDE (генерация гипотетического кода через LLM)?
   - **AST-чанкинг:** Оптимальное распределение бюджета токенов (512–1024 токена) между контекстным заголовком (сигнатура, docstring, неймспейс) и телом метода.
   - **Веса RRF:** Балансировка $W_{vec}$ и $W_{bm25}$, константа сглаживания $k$.
   - **Метрики:** Методика составления тестового набора запросов и замера Recall@10, MRR.
2. **UX / DX эксперт:**
   - **Сценарии поиска:** QuickPick (легковесный) vs Боковая панель (постоянная) — какой сценарий сделать основным по умолчанию.
   - **Превью сниппетов:** Оптимальный формат сниппета для мгновенной оценки релевантности.
   - **Фоновая индикация:** Ненавязчивая обратная связь без прерывания работы разработчика.
   - **Ошибки провайдера:** Презентация проблем с API-ключом / квотой в один клик.

---

## 8. Чек-лист для дальнейшего развития (Next Steps)

- [ ] **Tree-sitter интеграция:** Замена регулярок в `chunker.go` на полноразмерный синтаксический разбор через tree-sitter (C#, TypeScript, Python).
- [ ] **Оценка метрик (Eval Pipeline):** Скрипт прогона тестового датасета запросов с подсчетом Recall@5/10 и MRR.
- [ ] **Поддержка локальных LLM:** Проверка работы с Ollama (`ollama run nomic-embed-text` или `bge-m3`) без внешних API-ключей.
- [ ] **Серверный кэш (по стратегическому плану Sprint 1):** Проверка возможности выноса индекса на общий сервер команды для ускорения первого старта на машинах разработчиков.
